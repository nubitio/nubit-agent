package cron

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nubitio/nubit-agent/internal/site"
)

type Task struct {
	Schedule string `json:"schedule"`
	Script   string `json:"script"`
}

type Manager struct {
	Sites site.StateStore
	Dir   string
}

func (manager Manager) List(siteID string) ([]Task, error) {
	if _, err := manager.site(siteID); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(manager.path(siteID))
	if errors.Is(err, os.ErrNotExist) {
		return []Task{}, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal(contents, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (manager Manager) Replace(siteID string, tasks []Task) ([]Task, error) {
	state, err := manager.site(siteID)
	if err != nil {
		return nil, err
	}
	if len(tasks) > 3 {
		return nil, errors.New("a site can have at most 3 scheduled tasks")
	}
	for i, task := range tasks {
		if err := validateTask(task); err != nil {
			return nil, err
		}
		tasks[i].Script = strings.TrimPrefix(task.Script, "/")
	}
	if err := os.MkdirAll(manager.Dir, 0o700); err != nil {
		return nil, err
	}
	body, err := json.Marshal(tasks)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(manager.path(siteID), body, 0o600); err != nil {
		return nil, err
	}
	if err := os.MkdirAll("/etc/cron.d", 0o755); err != nil {
		return nil, err
	}
	cronPath := filepath.Join("/etc/cron.d", "nubit-"+strings.ReplaceAll(state.SystemUser, ".", "-"))
	if len(tasks) == 0 {
		_ = os.Remove(cronPath)
		return tasks, nil
	}
	var b strings.Builder
	b.WriteString("MAILTO=\"\"\n")
	php := "/usr/bin/php" + state.PHPVersion
	if _, err := os.Stat(php); err != nil {
		php = "/usr/bin/php"
	}
	for _, task := range tasks {
		script := filepath.Join(state.DocumentRoot, filepath.FromSlash(task.Script))
		b.WriteString(cronSchedule(task.Schedule) + " " + state.SystemUser + " " + php + " " + script + " >/dev/null 2>&1\n")
	}
	return tasks, os.WriteFile(cronPath, []byte(b.String()), 0o644)
}

func (manager Manager) site(siteID string) (site.State, error) {
	if manager.Sites == nil {
		return site.State{}, errors.New("cron manager is not configured")
	}
	state, found := manager.Sites.Get(siteID)
	if !found {
		return site.State{}, errors.New("site not found")
	}
	if manager.Dir == "" {
		manager.Dir = "/var/lib/nubit-agent/cron"
	}
	return state, nil
}

func (manager Manager) path(siteID string) string {
	dir := manager.Dir
	if dir == "" {
		dir = "/var/lib/nubit-agent/cron"
	}
	return filepath.Join(dir, siteID+".json")
}

func cronSchedule(schedule string) string {
	switch schedule {
	case "@hourly":
		return "0 * * * *"
	case "@daily":
		return "0 0 * * *"
	case "@weekly":
		return "0 0 * * 0"
	default:
		return schedule
	}
}

func validateTask(task Task) error {
	switch task.Schedule {
	case "@hourly", "@daily", "@weekly", "*/5 * * * *":
	default:
		return errors.New("schedule must be every 5 minutes, hourly, daily, or weekly")
	}
	script := strings.TrimPrefix(strings.ReplaceAll(task.Script, "\\", "/"), "/")
	if script == "" || strings.Contains(script, "..") || !strings.HasSuffix(script, ".php") {
		return errors.New("script must be a .php file inside the site")
	}
	return nil
}
