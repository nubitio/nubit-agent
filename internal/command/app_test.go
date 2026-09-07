package command

import (
	"encoding/json"
	"testing"
)

func validAppInstallPayload() map[string]any {
	return map[string]any{
		"siteId":     "example.com",
		"app":        "wordpress",
		"version":    "6.6.2",
		"locale":     "es_ES",
		"adminUser":  "admin",
		"adminEmail": "owner@example.com",
		"siteTitle":  "Mi tienda",
		"siteUrl":    "https://example.com",
		"dbName":     "example_wp",
		"dbUser":     "example_wp",
		"dbPassword": "secret",
	}
}

func parsePayload(t *testing.T, body map[string]any) (SiteAppInstallPayload, error) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return parseSiteAppInstall(raw)
}

func TestParseSiteAppInstallAcceptsAValidPayload(t *testing.T) {
	request, err := parsePayload(t, validAppInstallPayload())
	if err != nil {
		t.Fatal(err)
	}
	if request.toRequest().SiteURL != "https://example.com" {
		t.Fatalf("unexpected request: %#v", request)
	}
}

func TestParseSiteAppInstallAcceptsAnEmailOrMixedCaseAdminUser(t *testing.T) {
	for _, user := range []string{"owner@example.com", "Admin", "admin.user-1"} {
		body := validAppInstallPayload()
		body["adminUser"] = user
		if _, err := parsePayload(t, body); err != nil {
			t.Fatalf("admin user %q should be accepted: %v", user, err)
		}
	}
}

func TestParseSiteAppInstallDefaultsTheURLFromTheSiteID(t *testing.T) {
	body := validAppInstallPayload()
	delete(body, "siteUrl")
	request, err := parsePayload(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if got := request.toRequest().SiteURL; got != "https://example.com" {
		t.Fatalf("want https://example.com, got %q", got)
	}
}

func TestParseSiteAppUpdateAcceptsABareSiteId(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"siteId": "example.com", "plugins": true, "dryRun": true})
	request, err := parseSiteAppUpdate(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := request.toRequest()
	if !got.Plugins || got.Core || !got.DryRun {
		t.Fatalf("unexpected request: %#v", got)
	}
}

func TestParseSiteAppUpdateRejectsABadSiteId(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"siteId": "not a domain"})
	if _, err := parseSiteAppUpdate(raw); err == nil {
		t.Fatal("expected a bad site id to be rejected")
	}
}

// A misspelled component key must not silently fall through to "update all".
func TestParseSiteAppUpdateRejectsUnknownFields(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"siteId": "example.com", "plugin": true})
	if _, err := parseSiteAppUpdate(raw); err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
}

func TestParseSiteAppInstallRejectsBadInput(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"non-wordpress app": func(m map[string]any) { m["app"] = "joomla" },
		"bad site id":       func(m map[string]any) { m["siteId"] = "not a domain" },
		"bad version":       func(m map[string]any) { m["version"] = "6.x" },
		"bad locale":        func(m map[string]any) { m["locale"] = "spanish" },
		"bad admin user":    func(m map[string]any) { m["adminUser"] = "admin user!" },
		"bad admin email":   func(m map[string]any) { m["adminEmail"] = "owner" },
		"missing title":     func(m map[string]any) { delete(m, "siteTitle") },
		"missing db":        func(m map[string]any) { delete(m, "dbName") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			body := validAppInstallPayload()
			mutate(body)
			if _, err := parsePayload(t, body); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}
