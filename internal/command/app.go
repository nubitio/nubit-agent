package command

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/nubitio/nubit-agent/internal/site"
)

// wpVersion accepts "6.6.2", "6.6", "latest", or "" (meaning latest).
var wpVersion = regexp.MustCompile(`^(latest|[0-9]+(\.[0-9]+){0,2})?$`)
var wpLocale = regexp.MustCompile(`^[a-z]{2}(_[A-Z]{2})?$`)
var emailAddress = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// wpAdminUser is the WordPress admin login, which has no relation to any Unix
// account: WordPress permits mixed case, spaces, and `_ . @ + -`, so an
// address like "owner+wp@example.com" or a name like "Site Admin" is accepted.
// It is passed to wp-cli as a single argv entry, never to a shell.
var wpAdminUser = regexp.MustCompile(`^[A-Za-z0-9._@+ -]{1,60}$`)

// SiteAppInstallPayload is the site.app.install command payload. `app` is
// restricted to "wordpress" (any other value fails closed); the database
// credentials are the ones database.create already provisioned for the site.
type SiteAppInstallPayload struct {
	SiteID     string `json:"siteId"`
	App        string `json:"app"`
	Version    string `json:"version"`
	Locale     string `json:"locale"`
	AdminUser  string `json:"adminUser"`
	AdminEmail string `json:"adminEmail"`
	SiteTitle  string `json:"siteTitle"`
	SiteURL    string `json:"siteUrl"`
	DBName     string `json:"dbName"`
	DBUser     string `json:"dbUser"`
	DBPassword string `json:"dbPassword"`
	DBHost     string `json:"dbHost"`
}

func parseSiteAppInstall(payload json.RawMessage) (SiteAppInstallPayload, error) {
	var request SiteAppInstallPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if request.App != "wordpress" {
		return request, errors.New(`site.app.install only supports app "wordpress"`)
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if !wpVersion.MatchString(request.Version) {
		return request, errors.New("wordpress version is invalid")
	}
	if request.Locale != "" && !wpLocale.MatchString(request.Locale) {
		return request, errors.New("locale is invalid")
	}
	if !wpAdminUser.MatchString(request.AdminUser) {
		return request, errors.New("admin user is invalid")
	}
	if !emailAddress.MatchString(request.AdminEmail) {
		return request, errors.New("admin email is invalid")
	}
	if request.SiteTitle == "" {
		return request, errors.New("site title is required")
	}
	if !domainName.MatchString(hostOf(request.siteURL())) {
		return request, errors.New("site url is invalid")
	}
	if request.DBName == "" || request.DBUser == "" {
		return request, errors.New("database credentials are required")
	}
	return request, nil
}

func (p SiteAppInstallPayload) toRequest() site.AppInstallRequest {
	return site.AppInstallRequest{
		App:        p.App,
		Version:    p.Version,
		Locale:     p.Locale,
		AdminUser:  p.AdminUser,
		AdminEmail: p.AdminEmail,
		SiteTitle:  p.SiteTitle,
		SiteURL:    p.siteURL(),
		DBName:     p.DBName,
		DBUser:     p.DBUser,
		DBPassword: p.DBPassword,
		DBHost:     p.DBHost,
	}
}

// SiteAppUpdatePayload is the site.app.update command payload. With no
// component flag set, all three (core, plugins, themes) are updated. ServiceID
// is Control's own routing field: accepted so the payload can stay strict
// about component keys without rejecting it, but unused here.
type SiteAppUpdatePayload struct {
	SiteID    string `json:"siteId"`
	Core      bool   `json:"core"`
	Plugins   bool   `json:"plugins"`
	Themes    bool   `json:"themes"`
	DryRun    bool   `json:"dryRun"`
	ServiceID int64  `json:"serviceId"`
}

func parseSiteAppUpdate(payload json.RawMessage) (SiteAppUpdatePayload, error) {
	var request SiteAppUpdatePayload
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	// A misspelled component key ("plugin" for "plugins") would otherwise leave
	// every flag false, which components() reads as "update all three" —
	// silently widening the blast radius. Reject unknown fields instead.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	return request, nil
}

func (p SiteAppUpdatePayload) toRequest() site.AppUpdateRequest {
	return site.AppUpdateRequest{
		Core:    p.Core,
		Plugins: p.Plugins,
		Themes:  p.Themes,
		DryRun:  p.DryRun,
	}
}

// SiteAppAdminPasswordPayload is the site.app.admin-password command payload.
// The agent always generates the new password and returns it once — the caller
// does not get to choose it. ServiceID is Control's routing field, accepted
// but unused here.
type SiteAppAdminPasswordPayload struct {
	SiteID    string `json:"siteId"`
	AdminUser string `json:"adminUser"`
	ServiceID int64  `json:"serviceId"`
}

func parseSiteAppAdminPassword(payload json.RawMessage) (SiteAppAdminPasswordPayload, error) {
	var request SiteAppAdminPasswordPayload
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if !wpAdminUser.MatchString(request.AdminUser) {
		return request, errors.New("admin user is invalid")
	}
	return request, nil
}

func (p SiteAppAdminPasswordPayload) toRequest() site.AppAdminPasswordRequest {
	return site.AppAdminPasswordRequest{AdminUser: p.AdminUser}
}

func (p SiteAppInstallPayload) siteURL() string {
	if p.SiteURL != "" {
		return p.SiteURL
	}
	return "https://" + p.SiteID
}

// hostOf strips a scheme and path so a bare domain or a full URL both validate.
func hostOf(value string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(value, "https://"), "http://")
	if slash := strings.IndexByte(trimmed, '/'); slash >= 0 {
		trimmed = trimmed[:slash]
	}
	return trimmed
}
