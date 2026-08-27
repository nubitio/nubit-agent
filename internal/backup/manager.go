package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nubitio/nubit-agent/internal/site"
)

type Archive struct {
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	CreatedAt string `json:"createdAt"`
}

type Manager struct {
	Sites site.StateStore
	Dir   string
}

func (manager Manager) List(siteID string) ([]Archive, error) {
	if _, err := manager.site(siteID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(manager.dir(siteID))
	if errors.Is(err, os.ErrNotExist) {
		return []Archive{}, nil
	}
	if err != nil {
		return nil, err
	}
	var archives []Archive
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		archives = append(archives, Archive{Name: entry.Name(), Bytes: info.Size(), CreatedAt: info.ModTime().UTC().Format(time.RFC3339)})
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Name > archives[j].Name })
	return archives, nil
}

func (manager Manager) Create(siteID string) (Archive, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return Archive{}, err
	}
	if err := os.MkdirAll(manager.dir(siteID), 0o700); err != nil {
		return Archive{}, err
	}
	name := time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	path := filepath.Join(manager.dir(siteID), name)
	file, err := os.Create(path)
	if err != nil {
		return Archive{}, err
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(state.DocumentRoot, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(state.DocumentRoot, p)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		_ = in.Close()
		return copyErr
	})
	_ = tw.Close()
	_ = gz.Close()
	_ = file.Close()
	if err != nil {
		_ = os.Remove(path)
		return Archive{}, err
	}
	manager.prune(siteID)
	info, _ := os.Stat(path)
	bytes := int64(0)
	if info != nil {
		bytes = info.Size()
	}
	return Archive{Name: name, Bytes: bytes, CreatedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

func (manager Manager) Restore(siteID, name string, confirmed bool) error {
	if !confirmed {
		return errors.New("restore requires confirmation")
	}
	state, err := manager.site(siteID)
	if err != nil {
		return err
	}
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".tar.gz") {
		return errors.New("invalid backup name")
	}
	file, err := os.Open(filepath.Join(manager.dir(siteID), name))
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
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
		if strings.Contains(header.Name, "..") {
			return errors.New("backup contains an unsafe path")
		}
		target := filepath.Join(state.DocumentRoot, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			_ = out.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
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

func (manager Manager) dir(siteID string) string {
	base := manager.Dir
	if base == "" {
		base = "/var/lib/nubit-agent/backups"
	}
	return filepath.Join(base, siteID)
}

func (manager Manager) prune(siteID string) {
	archives, err := manager.List(siteID)
	if err != nil || len(archives) <= 7 {
		return
	}
	for _, archive := range archives[7:] {
		_ = os.Remove(filepath.Join(manager.dir(siteID), archive.Name))
	}
}
