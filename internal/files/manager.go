package files

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nubitio/nubit-agent/internal/site"
)

const maxReadBytes = 50 << 20

type Manager struct {
	Sites site.StateStore
}

type Entry struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modifiedAt"`
	// Owner is the login name of the file's owning uid (falling back to the
	// numeric uid when it has no passwd entry); Mode is the octal permission
	// string, e.g. "0644". Both are what the panel shows in its file table.
	Owner string `json:"owner,omitempty"`
	Mode  string `json:"mode,omitempty"`
}

type ListResult struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type ReadResult struct {
	Name    string `json:"name"`
	Size    int    `json:"size"`
	Content []byte `json:"-"`
}

type UsageResult struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
}

func (manager Manager) List(siteID, rel string) (ListResult, error) {
	state, target, err := manager.resolve(siteID, rel, true)
	if err != nil {
		return ListResult{}, err
	}
	_ = state
	entries, err := os.ReadDir(target)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Path: rel, Entries: []Entry{}}
	owners := map[uint32]string{}
	for i, entry := range entries {
		if i >= 500 {
			break
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		result.Entries = append(result.Entries, Entry{
			Name:       entry.Name(),
			Type:       kind,
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
			Owner:      ownerName(info, owners),
			Mode:       fmt.Sprintf("%04o", info.Mode().Perm()),
		})
	}
	return result, nil
}

// ownerName resolves the owning uid of a listed entry to a login name, reusing
// the per-listing cache so a folder of a thousand files does one passwd lookup,
// not a thousand. It returns "" when the platform does not expose a uid.
func ownerName(info os.FileInfo, cache map[uint32]string) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if name, seen := cache[stat.Uid]; seen {
		return name
	}
	name := strconv.FormatUint(uint64(stat.Uid), 10)
	if account, err := user.LookupId(name); err == nil && account.Username != "" {
		name = account.Username
	}
	cache[stat.Uid] = name
	return name
}

func (manager Manager) Mkdir(siteID, rel string) error {
	state, target, err := manager.resolve(siteID, rel, false)
	if err != nil {
		return err
	}
	if rel == "" {
		return errors.New("a folder name is required")
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return err
	}
	return chownTo(state.SystemUser, target)
}

func (manager Manager) Write(siteID, rel string, content []byte) error {
	if len(content) > maxReadBytes {
		return errors.New("file is too large")
	}
	state, target, err := manager.resolve(siteID, rel, false)
	if err != nil {
		return err
	}
	if rel == "" || strings.HasSuffix(rel, "/") {
		return errors.New("a file name is required")
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return err
	}
	return chownTo(state.SystemUser, target)
}

func (manager Manager) Read(siteID, rel string) (ReadResult, error) {
	_, target, err := manager.resolve(siteID, rel, true)
	if err != nil {
		return ReadResult{}, err
	}
	if rel == "" {
		return ReadResult{}, errors.New("a file name is required")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return ReadResult{}, err
	}
	if info.IsDir() {
		return ReadResult{}, errors.New("cannot download a folder")
	}
	if info.Size() > maxReadBytes {
		return ReadResult{}, errors.New("file is too large to download here")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{Name: filepath.Base(target), Size: len(content), Content: content}, nil
}

func (manager Manager) Rename(siteID, from, to string) error {
	state, src, err := manager.resolve(siteID, from, true)
	if err != nil {
		return err
	}
	_, dst, err := manager.resolve(siteID, to, false)
	if err != nil {
		return err
	}
	if from == "" || to == "" {
		return errors.New("both names are required")
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	return chownTo(state.SystemUser, dst)
}

func (manager Manager) Unzip(siteID, rel string) error {
	state, zipPath, err := manager.resolve(siteID, rel, true)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".zip") {
		return errors.New("only .zip files can be extracted")
	}
	root, err := filepath.Abs(state.DocumentRoot)
	if err != nil {
		return err
	}
	destDir := filepath.Dir(zipPath)
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return errors.New("zip contains an unsafe path")
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return errors.New("zip would extract outside the site")
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			_ = chownTo(state.SystemUser, target)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractZipFile(file, target); err != nil {
			return err
		}
		_ = chownTo(state.SystemUser, target)
	}
	return nil
}

func extractZipFile(file *zip.File, target string) error {
	in, err := file.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (manager Manager) Usage(siteID string) (UsageResult, error) {
	state, root, err := manager.resolve(siteID, "", true)
	if err != nil {
		return UsageResult{}, err
	}
	_ = state
	var bytes int64
	var files int64
	err = filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !info.IsDir() {
			bytes += info.Size()
			files++
		}
		return nil
	})
	return UsageResult{Bytes: bytes, Files: files}, err
}

func (manager Manager) Delete(siteID, rel string) error {
	_, target, err := manager.resolve(siteID, rel, true)
	if err != nil {
		return err
	}
	if rel == "" {
		return errors.New("the site root cannot be deleted")
	}
	return os.RemoveAll(target)
}

func (manager Manager) resolve(siteID, rel string, mustExist bool) (site.State, string, error) {
	if manager.Sites == nil {
		return site.State{}, "", errors.New("file manager is not configured")
	}
	state, found := manager.Sites.Get(siteID)
	if !found {
		return site.State{}, "", errors.New("site not found")
	}
	clean, err := normalizeRel(rel)
	if err != nil {
		return site.State{}, "", err
	}
	root, err := filepath.Abs(state.DocumentRoot)
	if err != nil {
		return site.State{}, "", err
	}
	target := root
	if clean != "" {
		target = filepath.Join(root, filepath.FromSlash(clean))
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return site.State{}, "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return site.State{}, "", errors.New("path is outside the site")
	}
	if mustExist {
		if _, err := os.Lstat(target); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return site.State{}, "", errors.New("file not found")
			}
			return site.State{}, "", err
		}
	}
	return state, target, nil
}

func normalizeRel(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return "", nil
	}
	if strings.ContainsRune(rel, 0) {
		return "", errors.New("path is invalid")
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path is outside the site")
	}
	return clean, nil
}

func chownTo(username, target string) error {
	account, err := user.Lookup(username)
	if err != nil {
		return nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	err = os.Chown(target, uid, gid)
	if err != nil && os.Geteuid() != 0 {
		return nil
	}
	return err
}
