package site

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nubitio/nubit-agent/internal/php"
)

type Runner interface {
	Run(name string, args ...string) error
}
type OSRunner struct{}

func (OSRunner) Run(name string, args ...string) error {
	command := exec.Command(name, args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

type Layout struct {
	SitesDir, CaddyConfigDir, CaddyDisabledDir, CaddyMainConfig, PHPConfigRoot, PHPConfigDir, PHPBinaryDir, StagingDir string
}

func DefaultLayout(phpVersion string) Layout {
	return Layout{"/srv/nubit/sites", "/etc/caddy/sites-enabled", "/etc/caddy/sites-disabled", "/etc/caddy/Caddyfile", "/etc/php", filepath.Join("/etc/php", phpVersion, "fpm/pool.d"), "/usr/sbin", "/var/lib/nubit-agent/staging"}
}

type CreateResult struct{ SiteID, DocumentRoot, PHPSocket, CaddyConfigHash, PHPConfigHash string }
type Provisioner struct {
	Runner Runner
	Layout Layout
	Store  StateStore
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

func (p Provisioner) Create(domain, systemUser, phpVersion string, resources Resources) (result CreateResult, err error) {
	if p.Runner == nil {
		return result, errors.New("site provisioner runner is required")
	}
	if p.Store == nil {
		return result, errors.New("site state store is required")
	}
	if !safeName.MatchString(domain) || filepath.Base(domain) != domain || domain == "." || domain == ".." {
		return result, errors.New("site domain is invalid")
	}
	if !safeName.MatchString(systemUser) || filepath.Base(systemUser) != systemUser {
		return result, errors.New("site system user is invalid")
	}
	if validateErr := php.ValidateInstalled(phpVersion, time.Now().UTC()); validateErr != nil {
		return result, validateErr
	}
	resources = resources.WithDefaults()
	if validateErr := resources.Validate(); validateErr != nil {
		return result, validateErr
	}
	layout := p.layoutFor(phpVersion)
	siteRoot := filepath.Join(layout.SitesDir, domain)
	documentRoot := filepath.Join(siteRoot, "public")
	socketName := systemUser + ".sock"
	socket := filepath.Join("/run/php", socketName)
	caddyPath := filepath.Join(layout.CaddyConfigDir, domain+".caddy")
	phpPath := filepath.Join(layout.PHPConfigDir, systemUser+".conf")
	for _, path := range []string{siteRoot, caddyPath, phpPath} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			if statErr != nil {
				return result, fmt.Errorf("inspect %s: %w", path, statErr)
			}
			return result, fmt.Errorf("site resource already exists: %s", path)
		}
	}
	caddy := []byte(CaddyConfig(domain, documentRoot, socket))
	php := []byte(PHPFPMConfig(systemUser, siteRoot, socketName, resources))
	if err = os.MkdirAll(layout.StagingDir, 0o700); err != nil {
		return result, fmt.Errorf("create staging directory: %w", err)
	}
	caddyStage, err := stageFile(layout.StagingDir, "caddy-", caddy)
	if err != nil {
		return result, err
	}
	defer os.Remove(caddyStage)
	phpStage, err := stageFile(layout.StagingDir, "php-fpm-", php)
	if err != nil {
		return result, err
	}
	defer os.Remove(phpStage)

	userCreated, siteDirectoryCreated, configsActivated := false, false, false
	defer func() {
		if err == nil {
			return
		}
		if configsActivated {
			_ = os.Remove(caddyPath)
			_ = os.Remove(phpPath)
			_ = p.Runner.Run("systemctl", "reload", "caddy")
			_ = p.Runner.Run("systemctl", "reload", "php"+phpVersion+"-fpm")
		}
		if userCreated {
			_ = p.Runner.Run("userdel", "--remove", systemUser)
		}
		if siteDirectoryCreated {
			_ = os.RemoveAll(siteRoot)
		}
	}()
	if err = p.run("useradd", "--system", "--create-home", "--shell", "/usr/sbin/nologin", systemUser); err != nil {
		return result, err
	}
	userCreated = true
	// useradd --system locks the account ("!" in shadow). sshd refuses even
	// public-key logins on a locked account; deleting the password unlocks
	// it without enabling password auth (sshd PasswordAuthentication is off).
	if err = p.run("passwd", "-d", systemUser); err != nil {
		return result, err
	}
	siteDirectoryCreated = true
	// Owned by the tenant, group-owned by the web server, closed to everyone
	// else. Caddy has to read what it serves, and 0750 without the group would
	// answer every request for a static file with a 403.
	if err = p.run("install", "-d", "-o", systemUser, "-g", WebServerUser, "-m", "0750", documentRoot); err != nil {
		return result, err
	}
	// Sessions and uploads land here instead of in a directory shared with every
	// other tenant. The web server never reads it, so it stays closed to the group.
	if err = p.run("install", "-d", "-o", systemUser, "-g", systemUser, "-m", "0700", filepath.Join(siteRoot, "tmp")); err != nil {
		return result, err
	}
	index := filepath.Join(documentRoot, "index.html")
	if writeErr := os.WriteFile(index, []byte(DefaultIndexHTML(domain)), 0o644); writeErr != nil {
		return result, writeErr
	}
	if err = p.run("chown", systemUser+":"+WebServerUser, index); err != nil {
		return result, err
	}
	if err = p.run("caddy", "validate", "--adapter", "caddyfile", "--config", caddyStage); err != nil {
		return result, err
	}
	if err = p.run("php-fpm"+phpVersion, "--test", "--fpm-config", phpStage); err != nil {
		return result, err
	}
	if err = activateFile(caddyStage, caddyPath); err != nil {
		return result, err
	}
	if err = activateFile(phpStage, phpPath); err != nil {
		_ = os.Remove(caddyPath)
		return result, err
	}
	configsActivated = true
	if layout.CaddyMainConfig != "" {
		if err = p.run("caddy", "validate", "--adapter", "caddyfile", "--config", layout.CaddyMainConfig); err != nil {
			return result, err
		}
	}
	if err = p.run("systemctl", "reload", "caddy"); err != nil {
		return result, err
	}
	if err = p.run("systemctl", "reload", "php"+phpVersion+"-fpm"); err != nil {
		return result, err
	}
	result = CreateResult{domain, documentRoot, socket, fmt.Sprintf("sha256:%x", sha256.Sum256(caddy)), fmt.Sprintf("sha256:%x", sha256.Sum256(php))}
	if err = p.Store.Save(State{SiteID: domain, Domain: domain, SystemUser: systemUser, PHPVersion: phpVersion, DocumentRoot: documentRoot, PHPSocket: socket, Status: "active", Domains: []string{domain}, Resources: resources}); err != nil {
		return CreateResult{}, fmt.Errorf("persist site state: %w", err)
	}
	return result, nil
}

type DomainResult struct {
	SiteID  string   `json:"siteId"`
	Domains []string `json:"domains"`
}

func (p Provisioner) AddDomain(siteID, domain string) (DomainResult, error) {
	if !safeName.MatchString(domain) || !strings.Contains(domain, ".") {
		return DomainResult{}, errors.New("site domain is invalid")
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return DomainResult{}, errors.New("site not found")
	}
	for _, existingSite := range p.Store.List() {
		for _, existingDomain := range domainsFor(existingSite) {
			if existingDomain == domain {
				if existingSite.SiteID == siteID {
					return DomainResult{siteID, domainsFor(state)}, nil
				}
				return DomainResult{}, errors.New("domain belongs to another site")
			}
		}
	}
	state.Domains = append(domainsFor(state), domain)
	sort.Strings(state.Domains)
	if err := p.applyDomains(state); err != nil {
		return DomainResult{}, err
	}
	return DomainResult{siteID, state.Domains}, nil
}

func (p Provisioner) RemoveDomain(siteID, domain string) (DomainResult, error) {
	state, found := p.Store.Get(siteID)
	if !found {
		return DomainResult{}, errors.New("site not found")
	}
	if domain == state.Domain {
		return DomainResult{}, errors.New("primary domain cannot be removed")
	}
	current := domainsFor(state)
	next := make([]string, 0, len(current))
	foundDomain := false
	for _, existing := range current {
		if existing == domain {
			foundDomain = true
			continue
		}
		next = append(next, existing)
	}
	if !foundDomain {
		return DomainResult{siteID, current}, nil
	}
	state.Domains = next
	if err := p.applyDomains(state); err != nil {
		return DomainResult{}, err
	}
	return DomainResult{siteID, next}, nil
}

func (p Provisioner) applyDomains(state State) error {
	if p.Runner == nil || p.Store == nil {
		return errors.New("site provisioner runner and state store are required")
	}
	layout := p.layoutFor(state.PHPVersion)
	path := filepath.Join(layout.CaddyConfigDir, state.Domain+".caddy")
	if state.Status == "suspended" {
		path = filepath.Join(layout.CaddyDisabledDir, state.Domain+".caddy")
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.StagingDir, 0o700); err != nil {
		return err
	}
	config := []byte(CaddyConfig(strings.Join(domainsFor(state), ", "), state.DocumentRoot, state.PHPSocket))
	staged, err := stageFile(layout.StagingDir, "caddy-domains-", config)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err := p.run("caddy", "validate", "--adapter", "caddyfile", "--config", staged); err != nil {
		return err
	}
	if err := activateFile(staged, path); err != nil {
		return err
	}
	rollback := func() { _ = activateContents(previous, path) }
	if state.Status != "suspended" {
		if layout.CaddyMainConfig != "" {
			if err := p.run("caddy", "validate", "--adapter", "caddyfile", "--config", layout.CaddyMainConfig); err != nil {
				rollback()
				return err
			}
		}
		if err := p.run("systemctl", "reload", "caddy"); err != nil {
			rollback()
			return err
		}
	}
	if err := p.Store.Save(state); err != nil {
		rollback()
		return err
	}
	return nil
}

func domainsFor(state State) []string {
	if len(state.Domains) == 0 {
		return []string{state.Domain}
	}
	return append([]string(nil), state.Domains...)
}

type LifecycleResult struct {
	SiteID string `json:"siteId"`
	Status string `json:"status"`
}

type DeleteResult struct {
	SiteID      string `json:"siteId"`
	Status      string `json:"status"`
	RecoveryDir string `json:"recoveryDir"`
}

func (p Provisioner) Suspend(siteID string) (LifecycleResult, error) {
	return p.setSiteStatus(siteID, "suspended")
}

func (p Provisioner) Resume(siteID string) (LifecycleResult, error) {
	return p.setSiteStatus(siteID, "active")
}

func (p Provisioner) Delete(siteID string, confirmed bool) (result DeleteResult, err error) {
	if !confirmed {
		return result, errors.New("site deletion requires explicit confirmation")
	}
	if p.Runner == nil || p.Store == nil {
		return result, errors.New("site provisioner runner and state store are required")
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return DeleteResult{SiteID: siteID, Status: "absent"}, nil
	}
	if state.Status != "suspended" {
		return result, errors.New("site must be suspended before deletion")
	}
	if state.SFTPEnabled || len(state.Databases) != 0 {
		return result, errors.New("site access and databases must be revoked before deletion")
	}
	layout := p.layoutFor(state.PHPVersion)
	siteRoot := filepath.Dir(state.DocumentRoot)
	recoveryDir := filepath.Join(layout.SitesDir, ".trash", siteID+"-"+time.Now().UTC().Format("20060102T150405Z"))
	if err = os.MkdirAll(recoveryDir, 0o700); err != nil {
		return result, err
	}
	caddyPath := filepath.Join(layout.CaddyDisabledDir, state.Domain+".caddy")
	phpPath := filepath.Join(layout.PHPConfigDir, state.SystemUser+".conf")
	caddyConfig, err := os.ReadFile(caddyPath)
	if err != nil {
		return result, err
	}
	phpConfig, err := os.ReadFile(phpPath)
	if err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(recoveryDir, "caddy.conf"), caddyConfig, 0o600); err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(recoveryDir, "php-fpm.conf"), phpConfig, 0o600); err != nil {
		return result, err
	}
	if err = os.Rename(siteRoot, filepath.Join(recoveryDir, "site")); err != nil {
		return result, fmt.Errorf("archive site directory: %w", err)
	}
	rollback := func() {
		_ = os.Rename(filepath.Join(recoveryDir, "site"), siteRoot)
		_ = activateContents(caddyConfig, caddyPath)
		_ = activateContents(phpConfig, phpPath)
		_ = p.Store.Save(state)
		_ = p.Runner.Run("systemctl", "reload", "php"+state.PHPVersion+"-fpm")
	}
	if err = os.Remove(caddyPath); err != nil {
		rollback()
		return result, err
	}
	if err = os.Remove(phpPath); err != nil {
		rollback()
		return result, err
	}
	if err = p.run("systemctl", "reload", "php"+state.PHPVersion+"-fpm"); err != nil {
		rollback()
		return result, err
	}
	if err = p.Store.Delete(siteID); err != nil {
		rollback()
		return result, err
	}
	if err = p.run("userdel", "--remove", state.SystemUser); err != nil {
		rollback()
		return result, err
	}
	return DeleteResult{SiteID: siteID, Status: "deleted", RecoveryDir: recoveryDir}, nil
}

func (p Provisioner) setSiteStatus(siteID, target string) (LifecycleResult, error) {
	if p.Runner == nil || p.Store == nil {
		return LifecycleResult{}, errors.New("site provisioner runner and state store are required")
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return LifecycleResult{}, errors.New("site not found")
	}
	current := state.Status
	if current == "" {
		current = "active"
	}
	if current == target {
		return LifecycleResult{siteID, target}, nil
	}
	if (current != "active" && current != "suspended") || (target != "active" && target != "suspended") {
		return LifecycleResult{}, errors.New("invalid site lifecycle transition")
	}
	layout := p.layoutFor(state.PHPVersion)
	activePath := filepath.Join(layout.CaddyConfigDir, state.Domain+".caddy")
	disabledPath := filepath.Join(layout.CaddyDisabledDir, state.Domain+".caddy")
	from, to := activePath, disabledPath
	if target == "active" {
		from, to = disabledPath, activePath
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return LifecycleResult{}, err
	}
	if err := os.Rename(from, to); err != nil {
		return LifecycleResult{}, fmt.Errorf("move site configuration: %w", err)
	}
	rollback := func() {
		_ = os.Rename(to, from)
		_ = p.Runner.Run("systemctl", "reload", "caddy")
	}
	if layout.CaddyMainConfig != "" {
		if err := p.run("caddy", "validate", "--adapter", "caddyfile", "--config", layout.CaddyMainConfig); err != nil {
			rollback()
			return LifecycleResult{}, err
		}
	}
	if err := p.run("systemctl", "reload", "caddy"); err != nil {
		rollback()
		return LifecycleResult{}, err
	}
	state.Status = target
	if err := p.Store.Save(state); err != nil {
		rollback()
		return LifecycleResult{}, fmt.Errorf("persist site state: %w", err)
	}
	return LifecycleResult{siteID, target}, nil
}

type PHPVersionResult struct {
	SiteID, PreviousVersion, PHPVersion, PHPSocket, PHPConfigHash string
}

func (p Provisioner) Inspect(siteID string) (State, error) {
	if p.Store == nil {
		return State{}, errors.New("site state store is required")
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return State{}, errors.New("site not found")
	}
	return state, nil
}

func (p Provisioner) SetPHPVersion(siteID, version string) (result PHPVersionResult, err error) {
	if p.Runner == nil || p.Store == nil {
		return result, errors.New("site provisioner runner and state store are required")
	}
	if validateErr := php.ValidateNewSite(version, time.Now().UTC()); validateErr != nil {
		return result, validateErr
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return result, errors.New("site not found")
	}
	if state.PHPVersion == version {
		return PHPVersionResult{siteID, version, version, state.PHPSocket, ""}, nil
	}

	oldLayout := p.layoutFor(state.PHPVersion)
	newLayout := p.layoutFor(version)
	oldPath := filepath.Join(oldLayout.PHPConfigDir, state.SystemUser+".conf")
	newPath := filepath.Join(newLayout.PHPConfigDir, state.SystemUser+".conf")
	if oldPath == newPath {
		return result, errors.New("PHP configuration layout does not separate versions")
	}
	oldConfig, err := os.ReadFile(oldPath)
	if err != nil {
		return result, fmt.Errorf("read current PHP configuration: %w", err)
	}
	newConfig := []byte(PHPFPMConfig(state.SystemUser, filepath.Dir(state.DocumentRoot), filepath.Base(state.PHPSocket), state.Resources))
	if err = os.MkdirAll(newLayout.StagingDir, 0o700); err != nil {
		return result, fmt.Errorf("create staging directory: %w", err)
	}
	staged, err := stageFile(newLayout.StagingDir, "php-fpm-", newConfig)
	if err != nil {
		return result, err
	}
	defer os.Remove(staged)
	if err = p.run("php-fpm"+version, "--test", "--fpm-config", staged); err != nil {
		return result, err
	}
	if err = activateFile(staged, newPath); err != nil {
		return result, err
	}

	migrationStarted := true
	defer func() {
		if err == nil || !migrationStarted {
			return
		}
		_ = os.Remove(newPath)
		_ = activateContents(oldConfig, oldPath)
		_ = p.Runner.Run("systemctl", "reload", "php"+state.PHPVersion+"-fpm")
		_ = p.Runner.Run("systemctl", "reload", "php"+version+"-fpm")
	}()
	if err = os.Remove(oldPath); err != nil {
		return result, fmt.Errorf("deactivate old PHP pool: %w", err)
	}
	if err = p.run("systemctl", "reload", "php"+state.PHPVersion+"-fpm"); err != nil {
		return result, err
	}
	if err = p.run("systemctl", "reload", "php"+version+"-fpm"); err != nil {
		return result, err
	}
	previous := state.PHPVersion
	state.PHPVersion = version
	if err = p.Store.Save(state); err != nil {
		return result, fmt.Errorf("persist site state: %w", err)
	}
	migrationStarted = false
	return PHPVersionResult{siteID, previous, version, state.PHPSocket, fmt.Sprintf("sha256:%x", sha256.Sum256(newConfig))}, nil
}

type ResourcesResult struct {
	SiteID        string    `json:"siteId"`
	Previous      Resources `json:"previous"`
	Resources     Resources `json:"resources"`
	PHPConfigHash string    `json:"phpConfigHash"`
}

// SetResources reapplies a site's limits after its plan changes.
//
// The pool file is the only thing that moves, so unlike a version change there
// is no second runtime to fail over to: the previous contents are kept and put
// back if the new ones do not survive validation or the reload.
func (p Provisioner) SetResources(siteID string, resources Resources) (result ResourcesResult, err error) {
	if p.Runner == nil || p.Store == nil {
		return result, errors.New("site provisioner runner and state store are required")
	}
	resources = resources.WithDefaults()
	if validateErr := resources.Validate(); validateErr != nil {
		return result, validateErr
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return result, errors.New("site not found")
	}
	previous := state.Resources.WithDefaults()

	layout := p.layoutFor(state.PHPVersion)
	path := filepath.Join(layout.PHPConfigDir, state.SystemUser+".conf")
	config := []byte(PHPFPMConfig(state.SystemUser, filepath.Dir(state.DocumentRoot), filepath.Base(state.PHPSocket), resources))
	if previous == resources {
		return ResourcesResult{siteID, previous, resources, fmt.Sprintf("sha256:%x", sha256.Sum256(config))}, nil
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("read current PHP configuration: %w", err)
	}
	if err = os.MkdirAll(layout.StagingDir, 0o700); err != nil {
		return result, fmt.Errorf("create staging directory: %w", err)
	}
	staged, err := stageFile(layout.StagingDir, "php-fpm-", config)
	if err != nil {
		return result, err
	}
	defer os.Remove(staged)
	if err = p.run("php-fpm"+state.PHPVersion, "--test", "--fpm-config", staged); err != nil {
		return result, err
	}
	if err = activateFile(staged, path); err != nil {
		return result, err
	}

	applied := true
	defer func() {
		if err == nil || !applied {
			return
		}
		_ = activateContents(current, path)
		_ = p.Runner.Run("systemctl", "reload", "php"+state.PHPVersion+"-fpm")
	}()
	if err = p.run("systemctl", "reload", "php"+state.PHPVersion+"-fpm"); err != nil {
		return result, err
	}
	state.Resources = resources
	if err = p.Store.Save(state); err != nil {
		return result, fmt.Errorf("persist site state: %w", err)
	}
	applied = false

	return ResourcesResult{siteID, previous, resources, fmt.Sprintf("sha256:%x", sha256.Sum256(config))}, nil
}

type RuntimeInfo struct {
	Version       string     `json:"version"`
	Status        php.Status `json:"status"`
	Recommended   bool       `json:"recommended"`
	SecurityUntil time.Time  `json:"securityUntil"`
	Installed     bool       `json:"installed"`
	SiteCount     int        `json:"siteCount"`
}

type RemoveRuntimeResult struct {
	Version string `json:"version"`
	Removed bool   `json:"removed"`
}

func (p Provisioner) RuntimeInventory() ([]RuntimeInfo, error) {
	if p.Store == nil {
		return nil, errors.New("site state store is required")
	}
	counts := make(map[string]int)
	for _, state := range p.Store.List() {
		counts[state.PHPVersion]++
	}
	now := time.Now().UTC()
	result := make([]RuntimeInfo, 0, 3)
	for _, runtime := range php.List(now) {
		layout := p.layoutFor(runtime.Version)
		_, statErr := os.Stat(filepath.Join(layout.PHPBinaryDir, "php-fpm"+runtime.Version))
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect PHP %s binary: %w", runtime.Version, statErr)
		}
		result = append(result, RuntimeInfo{runtime.Version, runtime.Status, runtime.Recommended, runtime.SecurityUntil, statErr == nil, counts[runtime.Version]})
	}
	return result, nil
}

func (p Provisioner) RemoveRuntime(version string, confirmed bool) (RemoveRuntimeResult, error) {
	if p.Runner == nil || p.Store == nil {
		return RemoveRuntimeResult{}, errors.New("site provisioner runner and state store are required")
	}
	if !confirmed {
		return RemoveRuntimeResult{}, errors.New("runtime removal requires explicit confirmation")
	}
	runtime, found := php.Lookup(version, time.Now().UTC())
	if !found {
		return RemoveRuntimeResult{}, errors.New("unknown PHP version")
	}
	if runtime.Status == php.Supported {
		return RemoveRuntimeResult{}, errors.New("supported PHP runtime cannot be removed")
	}
	for _, state := range p.Store.List() {
		if state.PHPVersion == version {
			return RemoveRuntimeResult{}, fmt.Errorf("PHP %s is still used by site %s", version, state.SiteID)
		}
	}
	layout := p.layoutFor(version)
	binary := filepath.Join(layout.PHPBinaryDir, "php-fpm"+version)
	if _, err := os.Stat(binary); errors.Is(err, os.ErrNotExist) {
		return RemoveRuntimeResult{Version: version, Removed: false}, nil
	} else if err != nil {
		return RemoveRuntimeResult{}, fmt.Errorf("inspect PHP runtime: %w", err)
	}
	if err := p.run("apt-get", "remove", "--yes", "php"+version+"-fpm"); err != nil {
		return RemoveRuntimeResult{}, err
	}
	return RemoveRuntimeResult{Version: version, Removed: true}, nil
}

type Drift struct {
	SiteID   string `json:"siteId"`
	Resource string `json:"resource"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

func (p Provisioner) Reconcile() ([]Drift, error) {
	if p.Store == nil {
		return nil, errors.New("site state store is required")
	}
	states := p.Store.List()
	sort.Slice(states, func(i, j int) bool { return states[i].SiteID < states[j].SiteID })
	drifts := make([]Drift, 0)
	for _, state := range states {
		layout := p.layoutFor(state.PHPVersion)
		caddyPath := filepath.Join(layout.CaddyConfigDir, state.Domain+".caddy")
		if state.Status == "suspended" {
			caddyPath = filepath.Join(layout.CaddyDisabledDir, state.Domain+".caddy")
		}
		for _, resource := range []struct {
			name string
			path string
		}{{"documentRoot", state.DocumentRoot}, {"caddyConfig", caddyPath}, {"phpConfig", filepath.Join(layout.PHPConfigDir, state.SystemUser+".conf")}, {"phpRuntime", filepath.Join(layout.PHPBinaryDir, "php-fpm"+state.PHPVersion)}} {
			if _, err := os.Stat(resource.path); errors.Is(err, os.ErrNotExist) {
				drifts = append(drifts, Drift{state.SiteID, resource.name, "present", "missing"})
			} else if err != nil {
				return nil, fmt.Errorf("inspect %s for %s: %w", resource.name, state.SiteID, err)
			}
		}
		if contents, err := os.ReadFile(caddyPath); err == nil {
			expected := []byte(CaddyConfig(strings.Join(domainsFor(state), ", "), state.DocumentRoot, state.PHPSocket))
			if sha256.Sum256(contents) != sha256.Sum256(expected) {
				drifts = append(drifts, Drift{state.SiteID, "caddyConfig", fmt.Sprintf("sha256:%x", sha256.Sum256(expected)), fmt.Sprintf("sha256:%x", sha256.Sum256(contents))})
			}
		}
		phpPath := filepath.Join(layout.PHPConfigDir, state.SystemUser+".conf")
		if contents, err := os.ReadFile(phpPath); err == nil {
			expected := []byte(PHPFPMConfig(state.SystemUser, filepath.Dir(state.DocumentRoot), filepath.Base(state.PHPSocket), state.Resources))
			if sha256.Sum256(contents) != sha256.Sum256(expected) {
				drifts = append(drifts, Drift{state.SiteID, "phpConfig", fmt.Sprintf("sha256:%x", sha256.Sum256(expected)), fmt.Sprintf("sha256:%x", sha256.Sum256(contents))})
			}
		}
	}
	return drifts, nil
}

func (p Provisioner) layoutFor(version string) Layout {
	layout := p.Layout
	if layout == (Layout{}) {
		return DefaultLayout(version)
	}
	if layout.PHPConfigRoot != "" {
		layout.PHPConfigDir = filepath.Join(layout.PHPConfigRoot, version, "fpm/pool.d")
	}
	if layout.PHPBinaryDir == "" {
		layout.PHPBinaryDir = "/usr/sbin"
	}
	if layout.CaddyDisabledDir == "" {
		layout.CaddyDisabledDir = filepath.Join(filepath.Dir(layout.CaddyConfigDir), "sites-disabled")
	}
	return layout
}

func (p Provisioner) run(name string, args ...string) error {
	if err := p.Runner.Run(name, args...); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
func stageFile(dir, pattern string, contents []byte) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", fmt.Errorf("create staged configuration: %w", err)
	}
	path := file.Name()
	if err := file.Chmod(0o600); err == nil {
		_, err = file.Write(contents)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write staged configuration: %w", err)
	}
	return path, nil
}
func activateFile(stagedPath, activePath string) error {
	if err := os.MkdirAll(filepath.Dir(activePath), 0o755); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	contents, err := os.ReadFile(stagedPath)
	if err != nil {
		return fmt.Errorf("read staged configuration: %w", err)
	}
	temporary, err := stageFile(filepath.Dir(activePath), ".nubit-", contents)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Chmod(temporary, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, activePath); err != nil {
		return fmt.Errorf("activate configuration: %w", err)
	}
	return nil
}

func activateContents(contents []byte, activePath string) error {
	if err := os.MkdirAll(filepath.Dir(activePath), 0o755); err != nil {
		return err
	}
	temporary, err := stageFile(filepath.Dir(activePath), ".nubit-rollback-", contents)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Chmod(temporary, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, activePath)
}
