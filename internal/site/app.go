package site

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// AppInstallRequest is the validated payload of a site.app.install command.
// Only WordPress is supported today; App is carried so the command can grow a
// second application without a second command type.
type AppInstallRequest struct {
	App           string
	Version       string
	Locale        string
	AdminUser     string
	AdminEmail    string
	SiteTitle     string
	SiteURL       string
	AdminPassword string
	DBName        string
	DBUser        string
	DBPassword    string
	DBHost        string
}

// AppInstallResult reports what site.app.install did. AdminPassword is the only
// place the generated password is ever surfaced; it is never logged and never
// persisted in site state.
type AppInstallResult struct {
	App           string `json:"app"`
	Installed     bool   `json:"installed"`
	Reason        string `json:"reason,omitempty"`
	Version       string `json:"version,omitempty"`
	URL           string `json:"url,omitempty"`
	AdminUser     string `json:"adminUser,omitempty"`
	AdminPassword string `json:"adminPassword,omitempty"`
}

// AppInstall runs wp-cli, as the site's own Unix user, to download and install
// WordPress into the site's document root. It is idempotent: a site that
// already carries a WordPress install is left untouched and reported as
// {installed:false, reason:"already-present"}. The document-root files and the
// database credentials in the request are the only inputs; the database itself
// is provisioned separately (database.create).
func (p Provisioner) AppInstall(siteID string, request AppInstallRequest) (AppInstallResult, error) {
	if p.Runner == nil || p.Store == nil {
		return AppInstallResult{}, errors.New("site provisioner is not configured")
	}
	if request.App != "wordpress" {
		return AppInstallResult{}, fmt.Errorf("unsupported app %q", request.App)
	}

	state, found := p.Store.Get(siteID)
	if !found {
		return AppInstallResult{}, fmt.Errorf("site %q is not known to this node", siteID)
	}
	if state.SystemUser == "" || state.DocumentRoot == "" {
		return AppInstallResult{}, errors.New("site state is missing a system user or document root")
	}

	wp := func(args ...string) error {
		full := append([]string{"-u", state.SystemUser, "--", "wp", "--path=" + state.DocumentRoot}, args...)
		return p.Runner.Run("sudo", full...)
	}

	// Idempotency: a non-zero exit means "not installed", which is exactly
	// when there is work to do.
	if wp("core", "is-installed") == nil {
		result := AppInstallResult{App: request.App, Installed: false, Reason: "already-present"}
		// A WordPress site that predates this command — or one whose earlier
		// hardening step failed — still gets the hardened vhost.
		if state.App != "wordpress" {
			if err := p.ensureWordPressProfile(state); err != nil {
				result.Reason = "already-present; caddy hardening deferred: " + err.Error()
			} else {
				result.Reason = "already-present; caddy hardening applied"
			}
		}
		return result, nil
	}

	// core download / config create run with --force so a retry after a
	// partially completed install (files downloaded, wp-config written, then
	// `core install` failed) is not permanently wedged.
	if err := wp(downloadArgs(request)...); err != nil {
		return AppInstallResult{}, fmt.Errorf("wp core download: %w", err)
	}
	if err := wp(configArgs(request)...); err != nil {
		return AppInstallResult{}, fmt.Errorf("wp config create: %w", err)
	}

	password := request.AdminPassword
	if password == "" {
		password = generatePassword()
	}
	if err := wp(installArgs(request, password)...); err != nil {
		return AppInstallResult{}, fmt.Errorf("wp core install: %w", err)
	}

	// The install succeeded and its password is now the only copy; the vhost
	// swap must not be able to lose it. Record the profile and harden, but a
	// transient Caddy/reload failure only defers the hardening (drift detection
	// and a retry via the already-present path converge it) — it is not an
	// install failure.
	result := AppInstallResult{
		App:           request.App,
		Installed:     true,
		Version:       request.Version,
		URL:           request.SiteURL,
		AdminUser:     request.AdminUser,
		AdminPassword: password,
	}
	if err := p.ensureWordPressProfile(state); err != nil {
		result.Reason = "installed; caddy hardening deferred: " + err.Error()
	}
	return result, nil
}

// ensureWordPressProfile records app="wordpress" on the site and re-renders its
// Caddy vhost with the hardened template. It reuses applyDomains (stage → caddy
// validate → activate → reload, with rollback) so the vhost-swap protocol lives
// in exactly one place; caddyConfigFor picks the WordPress template once App is
// set. Saving the profile first means a later failure of the swap still shows
// as drift.
func (p Provisioner) ensureWordPressProfile(state State) error {
	state.App = "wordpress"
	if err := p.Store.Save(state); err != nil {
		return err
	}
	return p.applyDomains(state)
}

func downloadArgs(request AppInstallRequest) []string {
	args := []string{"core", "download", "--force"}
	if request.Version != "" {
		args = append(args, "--version="+request.Version)
	}
	if request.Locale != "" {
		args = append(args, "--locale="+request.Locale)
	}
	return args
}

func configArgs(request AppInstallRequest) []string {
	host := request.DBHost
	if host == "" {
		host = "127.0.0.1"
	}
	return []string{
		"config", "create",
		"--dbname=" + request.DBName,
		"--dbuser=" + request.DBUser,
		"--dbpass=" + request.DBPassword,
		"--dbhost=" + host,
		"--skip-check",
		"--force",
	}
}

func installArgs(request AppInstallRequest, password string) []string {
	return []string{
		"core", "install",
		"--url=" + request.SiteURL,
		"--title=" + request.SiteTitle,
		"--admin_user=" + request.AdminUser,
		"--admin_email=" + request.AdminEmail,
		"--admin_password=" + password,
		"--skip-email",
	}
}

func generatePassword() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail in practice; fall back rather than panic
		// inside a provisioning command.
		return strings.Repeat("x", 24)
	}
	return hex.EncodeToString(buf)
}

// OutputRunner is an optional Runner capability: it returns a command's stdout.
// AppUpdate uses it to read `wp core version` before and after so the result
// can report the version delta; a Runner that does not implement it degrades
// to exit-code-only reporting (CoreBefore/CoreAfter left empty).
type OutputRunner interface {
	Output(name string, args ...string) ([]byte, error)
}

// AppUpdateRequest selects which parts of a managed WordPress install to
// update. A zero value (no component set) means all three.
type AppUpdateRequest struct {
	Core    bool
	Plugins bool
	Themes  bool
	// DryRun passes --dry-run to every wp-cli update so control can preview
	// what a run would change without touching the site.
	DryRun bool
}

func (r AppUpdateRequest) components() (core, plugins, themes bool) {
	if !r.Core && !r.Plugins && !r.Themes {
		return true, true, true
	}
	return r.Core, r.Plugins, r.Themes
}

// AppComponentResult is the outcome of one component's update.
type AppComponentResult struct {
	Component string `json:"component"` // "core" | "plugins" | "themes"
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"` // error text when OK is false
}

// AppUpdateResult reports what site.app.update did. OK is true only when every
// requested component updated cleanly.
type AppUpdateResult struct {
	App        string               `json:"app"`
	DryRun     bool                 `json:"dryRun,omitempty"`
	CoreBefore string               `json:"coreBefore,omitempty"`
	CoreAfter  string               `json:"coreAfter,omitempty"`
	Components []AppComponentResult `json:"components"`
	OK         bool                 `json:"ok"`
}

// AppUpdate runs `wp core update` (+ `core update-db`), `wp plugin update
// --all` and `wp theme update --all` as the site's own Unix user, for the
// components the request selects. A component failure is recorded and the run
// continues with the others; the result's OK reflects whether every requested
// component succeeded. The site must already carry a managed WordPress install
// (app == "wordpress", `wp core is-installed`).
//
// Taking a backup first is the caller's responsibility: control sequences
// site.backup.create ahead of a scheduled site.app.update the same way it
// sequences its other backup jobs. Whether a site is updated on a schedule at
// all — the per-site auto-update toggle — is control-plane state; this command
// only executes.
func (p Provisioner) AppUpdate(siteID string, request AppUpdateRequest) (AppUpdateResult, error) {
	if p.Runner == nil || p.Store == nil {
		return AppUpdateResult{}, errors.New("site provisioner is not configured")
	}
	state, found := p.Store.Get(siteID)
	if !found {
		return AppUpdateResult{}, fmt.Errorf("site %q is not known to this node", siteID)
	}
	if state.SystemUser == "" || state.DocumentRoot == "" {
		return AppUpdateResult{}, errors.New("site state is missing a system user or document root")
	}
	if state.App != "wordpress" {
		return AppUpdateResult{}, fmt.Errorf("site %q has no managed WordPress install", siteID)
	}

	wpArgs := func(args ...string) []string {
		return append([]string{"-u", state.SystemUser, "--", "wp", "--path=" + state.DocumentRoot}, args...)
	}
	wp := func(args ...string) error { return p.Runner.Run("sudo", wpArgs(args...)...) }
	wpValue := func(args ...string) string {
		runner, ok := p.Runner.(OutputRunner)
		if !ok {
			return ""
		}
		out, err := runner.Output("sudo", wpArgs(args...)...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	if err := wp("core", "is-installed"); err != nil {
		return AppUpdateResult{}, fmt.Errorf("wordpress is not installed for site %q", siteID)
	}

	core, plugins, themes := request.components()
	result := AppUpdateResult{App: "wordpress", DryRun: request.DryRun, OK: true}
	result.CoreBefore = wpValue("core", "version")

	dry := func(args ...string) []string {
		if request.DryRun {
			return append(args, "--dry-run")
		}
		return args
	}
	record := func(component string, err error) {
		item := AppComponentResult{Component: component, OK: err == nil}
		if err != nil {
			item.Detail = err.Error()
			result.OK = false
		}
		result.Components = append(result.Components, item)
	}

	if core {
		err := wp(dry("core", "update")...)
		if err == nil && !request.DryRun {
			// The schema migration that a core update may require; harmless
			// when there is nothing to migrate.
			err = wp("core", "update-db")
		}
		record("core", err)
	}
	if plugins {
		record("plugins", wp(dry("plugin", "update", "--all")...))
	}
	if themes {
		record("themes", wp(dry("theme", "update", "--all")...))
	}

	result.CoreAfter = wpValue("core", "version")
	return result, nil
}
