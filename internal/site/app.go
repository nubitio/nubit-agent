package site

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		return AppInstallResult{App: request.App, Installed: false, Reason: "already-present"}, nil
	}

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

	// The site is now a WordPress site: re-render its vhost with the hardened
	// template (xmlrpc/wp-config blocked, PHP denied under uploads, static
	// assets cached) and mark the profile on state so later domain edits and
	// drift checks keep it.
	if err := p.applyWordPressVhost(state); err != nil {
		return AppInstallResult{}, fmt.Errorf("apply wordpress vhost: %w", err)
	}

	return AppInstallResult{
		App:           request.App,
		Installed:     true,
		Version:       request.Version,
		URL:           request.SiteURL,
		AdminUser:     request.AdminUser,
		AdminPassword: password,
	}, nil
}

// applyWordPressVhost re-renders the site's Caddy vhost with the hardened
// WordPress template, validates it, activates it and reloads Caddy. It is the
// final step of a successful install; on any failure the previous vhost is put
// back. State is saved with App="wordpress" so applyDomains and Reconcile
// regenerate the same template from then on.
func (p Provisioner) applyWordPressVhost(state State) error {
	layout := p.layoutFor(state.PHPVersion)
	path := filepath.Join(layout.CaddyConfigDir, state.Domain+".caddy")
	if state.Status == "suspended" {
		path = filepath.Join(layout.CaddyDisabledDir, state.Domain+".caddy")
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read current vhost: %w", err)
	}
	if err := os.MkdirAll(layout.StagingDir, 0o700); err != nil {
		return err
	}

	state.App = "wordpress"
	staged, err := stageFile(layout.StagingDir, "caddy-wordpress-", caddyConfigFor(state))
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

func downloadArgs(request AppInstallRequest) []string {
	args := []string{"core", "download"}
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
