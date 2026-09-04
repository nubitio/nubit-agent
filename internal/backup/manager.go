package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nubitio/nubit-agent/internal/objectstore"
	"github.com/nubitio/nubit-agent/internal/site"
)

// Archive is one stored backup of a site.
type Archive struct {
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	CreatedAt string `json:"createdAt"`
}

// VerifyResult is what site.backup.verify reports back to Control (folded onto
// the Service by RecordBackupOutcome). A structurally bad archive is
// Verified:false with a Reason, never an error — a failed rehearsal is data.
type VerifyResult struct {
	Verified        bool   `json:"verified"`
	DurationSeconds int    `json:"durationSeconds"`
	Files           int    `json:"files"`
	Databases       int    `json:"databases"`
	Archive         string `json:"archive"`
	Reason          string `json:"reason,omitempty"`
}

// BlobStore is the subset of object storage the manager needs.
// *objectstore.Store satisfies it; tests substitute an in-memory fake.
type BlobStore interface {
	Put(ctx context.Context, key string, body io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]objectstore.Object, error)
	Delete(ctx context.Context, key string) error
}

// manifest travels inside every archive so a restore can sanity-check it and
// know which databases to re-import.
type manifest struct {
	SiteID    string   `json:"siteId"`
	CreatedAt string   `json:"createdAt"`
	Databases []string `json:"databases"`
}

const (
	filesPrefix    = "files/"
	databasePrefix = "databases/"
	manifestName   = "manifest.json"
	// keepArchives is the floor: prune never drops a site below this many
	// archives, even when a short retention window would. It is also the exact
	// behaviour when the plan supplies no retentionDays.
	keepArchives  = 7
	archiveLayout = "20060102T150405Z"
)

// Manager creates, lists and restores per-site backups. Every archive is a
// gzip tarball holding the document root and a SQL dump of each of the site's
// databases, stored only in the configured S3 bucket — there is no local copy.
type Manager struct {
	Sites site.StateStore
	Blobs BlobStore

	// TempDir is where an archive is assembled before upload / after download.
	TempDir string
	// DumpBin / RestoreBin default to mariadb-dump / mariadb, run as the agent
	// (root) against the local socket, which is how it already administers the
	// databases it provisions.
	DumpBin    string
	RestoreBin string
}

func (manager Manager) List(siteID string) ([]Archive, error) {
	if _, err := manager.site(siteID); err != nil {
		return nil, err
	}
	if manager.Blobs == nil {
		return nil, errors.New("backup storage is not configured")
	}

	objects, err := manager.Blobs.List(context.Background(), siteID+"/")
	if err != nil {
		return nil, err
	}
	archives := make([]Archive, 0, len(objects))
	for _, object := range objects {
		name := filepath.Base(object.Key)
		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		archives = append(archives, Archive{
			Name:      name,
			Bytes:     object.Size,
			CreatedAt: object.LastModified.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Name > archives[j].Name })
	return archives, nil
}

// Create writes a new archive and prunes old ones. retentionDays comes from
// the plan (ADR-001 tiers: 7 / 30 / 30); 0 keeps the legacy "7 newest only"
// behaviour.
func (manager Manager) Create(siteID string, retentionDays int) (Archive, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return Archive{}, err
	}
	if manager.Blobs == nil {
		return Archive{}, errors.New("backup storage is not configured")
	}

	if err := os.MkdirAll(manager.tempDir(), 0o700); err != nil {
		return Archive{}, err
	}
	tmp, err := os.CreateTemp(manager.tempDir(), "backup-*.tar.gz")
	if err != nil {
		return Archive{}, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	now := time.Now().UTC()
	name := now.Format("20060102T150405Z") + ".tar.gz"

	if err := manager.writeArchive(tmp, state, now); err != nil {
		_ = tmp.Close()
		return Archive{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return Archive{}, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return Archive{}, err
	}

	if err := manager.Blobs.Put(context.Background(), siteID+"/"+name, tmp); err != nil {
		_ = tmp.Close()
		return Archive{}, fmt.Errorf("upload backup: %w", err)
	}
	_ = tmp.Close()

	info, _ := os.Stat(tmpPath)
	var bytes int64
	if info != nil {
		bytes = info.Size()
	}
	manager.prune(siteID, retentionDays, now)
	return Archive{Name: name, Bytes: bytes, CreatedAt: now.Format(time.RFC3339)}, nil
}

func (manager Manager) writeArchive(out io.Writer, state site.State, now time.Time) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	body, err := json.Marshal(manifest{
		SiteID:    state.SiteID,
		CreatedAt: now.Format(time.RFC3339),
		Databases: state.Databases,
	})
	if err != nil {
		return err
	}
	if err := writeTarBytes(tw, manifestName, body); err != nil {
		return err
	}

	walkErr := filepath.Walk(state.DocumentRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(state.DocumentRoot, path)
		if err != nil {
			return err
		}
		// Backups must never dereference a customer-controlled symlink: it may
		// point at another site's files or at host secrets readable by root.
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("site contains a symlink; refusing to archive it")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filesPrefix + filepath.ToSlash(rel)
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		_ = in.Close()
		return copyErr
	})
	if walkErr != nil {
		return walkErr
	}

	for _, database := range state.Databases {
		if err := manager.dumpDatabase(tw, database); err != nil {
			return fmt.Errorf("dump database %s: %w", database, err)
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func (manager Manager) dumpDatabase(tw *tar.Writer, database string) error {
	dump, err := os.CreateTemp(manager.tempDir(), "dump-*.sql")
	if err != nil {
		return err
	}
	dumpPath := dump.Name()
	defer func() { _ = os.Remove(dumpPath) }()

	cmd := exec.Command(manager.dumpBin(),
		"--single-transaction", "--add-drop-table", "--routines", "--events",
		"--databases", database,
	)
	cmd.Stdout = dump
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		_ = dump.Close()
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := dump.Sync(); err != nil {
		_ = dump.Close()
		return err
	}
	info, err := dump.Stat()
	if err != nil {
		_ = dump.Close()
		return err
	}
	if _, err := dump.Seek(0, io.SeekStart); err != nil {
		_ = dump.Close()
		return err
	}
	header := &tar.Header{
		Name:    databasePrefix + database + ".sql",
		Mode:    0o600,
		Size:    info.Size(),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		_ = dump.Close()
		return err
	}
	_, copyErr := io.Copy(tw, dump)
	_ = dump.Close()
	return copyErr
}

func (manager Manager) Restore(siteID, name string, confirmed bool) error {
	if !confirmed {
		return errors.New("restore requires confirmation")
	}
	state, err := manager.site(siteID)
	if err != nil {
		return err
	}
	if manager.Blobs == nil {
		return errors.New("backup storage is not configured")
	}
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".tar.gz") {
		return errors.New("invalid backup name")
	}

	if err := os.MkdirAll(manager.tempDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(manager.tempDir(), "restore-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	reader, err := manager.Blobs.Get(context.Background(), siteID+"/"+name)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("download backup: %w", err)
	}
	_, copyErr := io.Copy(tmp, reader)
	_ = reader.Close()
	if copyErr != nil {
		_ = tmp.Close()
		return copyErr
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return err
	}

	err = manager.applyArchive(tmp, state)
	_ = tmp.Close()
	return err
}

// Verify downloads the newest archive and extracts it to a throwaway directory
// to prove it is recoverable — it never touches the live document root or
// MariaDB. It is the agent side of Control's restore-rehearsal scheduler.
func (manager Manager) Verify(siteID string) (VerifyResult, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return VerifyResult{}, err
	}
	if manager.Blobs == nil {
		return VerifyResult{}, errors.New("backup storage is not configured")
	}
	archives, err := manager.List(siteID)
	if err != nil {
		return VerifyResult{}, err
	}
	if len(archives) == 0 {
		return VerifyResult{}, errors.New("no backups to verify")
	}
	newest := archives[0].Name

	start := time.Now()
	if err := os.MkdirAll(manager.tempDir(), 0o700); err != nil {
		return VerifyResult{}, err
	}
	tmp, err := os.CreateTemp(manager.tempDir(), "verify-*.tar.gz")
	if err != nil {
		return VerifyResult{}, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	reader, err := manager.Blobs.Get(context.Background(), siteID+"/"+newest)
	if err != nil {
		_ = tmp.Close()
		return VerifyResult{}, fmt.Errorf("download backup: %w", err)
	}
	_, copyErr := io.Copy(tmp, reader)
	_ = reader.Close()
	if copyErr != nil {
		_ = tmp.Close()
		return VerifyResult{}, copyErr
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return VerifyResult{}, err
	}

	scratch, err := os.MkdirTemp(manager.tempDir(), "verify-scratch-")
	if err != nil {
		_ = tmp.Close()
		return VerifyResult{}, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	result := manager.inspectArchive(tmp, scratch, state.SiteID)
	_ = tmp.Close()
	result.Archive = newest
	result.DurationSeconds = int(time.Since(start).Round(time.Second) / time.Second)
	return result, nil
}

// inspectArchive walks the tarball into scratch, counting recoverable content
// and checking the manifest. A structural problem is a Verified:false result
// with a Reason, not an error.
func (manager Manager) inspectArchive(archive io.Reader, scratch, siteID string) VerifyResult {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return VerifyResult{Reason: "archive is not valid gzip: " + err.Error()}
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var files, databases int
	manifestSeen := false
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return VerifyResult{Files: files, Databases: databases, Reason: "archive is truncated or corrupt: " + err.Error()}
		}
		switch {
		case header.Name == manifestName:
			manifestSeen = true
			if err := manager.checkManifest(tr, siteID); err != nil {
				return VerifyResult{Reason: err.Error()}
			}
		case strings.HasPrefix(header.Name, filesPrefix):
			if header.Typeflag != tar.TypeReg {
				continue
			}
			files++
			rel := strings.TrimPrefix(header.Name, filesPrefix)
			target, pathErr := restorePath(scratch, rel)
			if pathErr != nil {
				return VerifyResult{Files: files, Databases: databases, Reason: pathErr.Error()}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return VerifyResult{Files: files, Databases: databases, Reason: err.Error()}
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return VerifyResult{Files: files, Databases: databases, Reason: err.Error()}
			}
			_, copyErr := io.Copy(out, tr)
			_ = out.Close()
			if copyErr != nil {
				return VerifyResult{Files: files, Databases: databases, Reason: "file extract failed: " + copyErr.Error()}
			}
		case strings.HasPrefix(header.Name, databasePrefix):
			if header.Typeflag == tar.TypeReg {
				databases++
			}
		}
	}

	if !manifestSeen {
		return VerifyResult{Files: files, Databases: databases, Reason: "archive has no manifest.json"}
	}
	if files == 0 && databases == 0 {
		return VerifyResult{Files: files, Databases: databases, Reason: "archive has no files and no database dumps"}
	}
	return VerifyResult{Verified: true, Files: files, Databases: databases}
}

func (manager Manager) applyArchive(archive io.Reader, state site.State) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		switch {
		case header.Name == manifestName:
			if err := manager.checkManifest(tr, state.SiteID); err != nil {
				return err
			}
		case strings.HasPrefix(header.Name, filesPrefix):
			if err := manager.restoreFile(state, header, tr); err != nil {
				return err
			}
		case strings.HasPrefix(header.Name, databasePrefix):
			if header.Typeflag != tar.TypeReg {
				continue
			}
			if err := manager.restoreDatabase(tr); err != nil {
				return err
			}
		}
	}
}

func (manager Manager) checkManifest(r io.Reader, siteID string) error {
	var m manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return fmt.Errorf("archive manifest is unreadable: %w", err)
	}
	if m.SiteID != "" && m.SiteID != siteID {
		return fmt.Errorf("archive belongs to %q, not %q", m.SiteID, siteID)
	}
	return nil
}

func (manager Manager) restoreFile(state site.State, header *tar.Header, r io.Reader) error {
	rel := strings.TrimPrefix(header.Name, filesPrefix)
	if rel == "" {
		return nil
	}
	target, err := restorePath(state.DocumentRoot, rel)
	if err != nil {
		return err
	}
	if err := ensureNoSymlinkParents(state.DocumentRoot, target); err != nil {
		return err
	}
	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		chownTo(state.SystemUser, target)
		return nil
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, r)
		_ = out.Close()
		if copyErr != nil {
			return copyErr
		}
		// The agent runs as root; a restored file left root-owned would be
		// unwritable by the site's PHP-FPM pool.
		chownTo(state.SystemUser, target)
		return nil
	}
	return nil
}

// chownTo best-effort assigns target to the site's Unix user. A missing user
// (tests, a site not yet fully provisioned) or a non-root agent is not an error.
func chownTo(username, target string) {
	if username == "" {
		return
	}
	account, err := user.Lookup(username)
	if err != nil {
		return
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil {
		return
	}
	_ = os.Chown(target, uid, gid)
}

func (manager Manager) restoreDatabase(r io.Reader) error {
	cmd := exec.Command(manager.restoreBin())
	cmd.Stdin = r
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func writeTarBytes(tw *tar.Writer, name string, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(body)),
		ModTime: time.Now(),
	}); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func restorePath(documentRoot, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("backup contains an unsafe path")
	}
	target := filepath.Join(documentRoot, clean)
	rel, err := filepath.Rel(documentRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("backup contains an unsafe path")
	}
	return target, nil
}

// ensureNoSymlinkParents refuses an archive that would write through an
// existing symlink inside a site. It covers the normal hostile case; document
// roots are owned by the per-site Unix user, so operators should also restore
// only while the site is suspended (the control lifecycle already requires it).
func ensureNoSymlinkParents(documentRoot, target string) error {
	rel, err := filepath.Rel(documentRoot, target)
	if err != nil {
		return errors.New("backup contains an unsafe path")
	}
	current := documentRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("backup restore would follow a symlink")
		}
	}
	return nil
}

func (manager Manager) site(siteID string) (site.State, error) {
	if manager.Sites == nil {
		return site.State{}, errors.New("backup manager is not configured")
	}
	state, found := manager.Sites.Get(siteID)
	if !found {
		return site.State{}, errors.New("site not found")
	}
	return state, nil
}

func (manager Manager) tempDir() string {
	if manager.TempDir != "" {
		return manager.TempDir
	}
	return "/var/lib/nubit-agent/tmp"
}

func (manager Manager) dumpBin() string {
	if manager.DumpBin != "" {
		return manager.DumpBin
	}
	return "mariadb-dump"
}

func (manager Manager) restoreBin() string {
	if manager.RestoreBin != "" {
		return manager.RestoreBin
	}
	return "mariadb"
}

// prune keeps every archive inside the retention window and, below that, the
// keepArchives newest. With retentionDays <= 0 it falls back to "keepArchives
// newest only", which is what every site got before plans set retention.
func (manager Manager) prune(siteID string, retentionDays int, now time.Time) {
	archives, err := manager.List(siteID)
	if err != nil || len(archives) <= keepArchives {
		return
	}
	var cutoff time.Time
	if retentionDays > 0 {
		cutoff = now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	}
	for _, archive := range archives[keepArchives:] {
		if retentionDays > 0 && archiveTime(archive.Name).After(cutoff) {
			continue
		}
		_ = manager.Blobs.Delete(context.Background(), siteID+"/"+archive.Name)
	}
}

// archiveTime parses the "20060102T150405Z" prefix an archive name carries.
// A name that does not parse sorts as the zero time, i.e. always prunable.
func archiveTime(name string) time.Time {
	parsed, err := time.Parse(archiveLayout, strings.TrimSuffix(name, ".tar.gz"))
	if err != nil {
		return time.Time{}
	}
	return parsed
}
