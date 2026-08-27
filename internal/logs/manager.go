package logs

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nubitio/nubit-agent/internal/site"
)

type Result struct {
	Source string   `json:"source"`
	Lines  []string `json:"lines"`
}

type Manager struct {
	Sites site.StateStore
}

func (manager Manager) Read(siteID, source string, limit int) (Result, error) {
	if manager.Sites == nil {
		return Result{}, errors.New("log manager is not configured")
	}
	state, found := manager.Sites.Get(siteID)
	if !found {
		return Result{}, errors.New("site not found")
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var path string
	switch source {
	case "php":
		path = filepath.Join("/var/log/nubit", state.SystemUser+".php.log")
	case "caddy", "web", "":
		source = "caddy"
		path = filepath.Join("/var/log/nubit", strings.ReplaceAll(state.Domain, "/", "_")+".caddy.log")
	default:
		return Result{}, errors.New("source must be php or caddy")
	}
	lines, err := tailFile(path, limit)
	if errors.Is(err, os.ErrNotExist) {
		return Result{Source: source, Lines: []string{}}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Source: source, Lines: lines}, nil
}

func tailFile(path string, limit int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return lines, nil
}
