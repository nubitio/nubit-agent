package php

import (
	"testing"
	"time"
)

func TestDeprecatedRuntimeRemainsInstalledButCannotHostNewSites(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	if err := ValidateInstalled("8.3", now); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNewSite("8.3", now); err == nil {
		t.Fatal("expected PHP 8.3 to be rejected for a new site")
	}
}

func TestRuntimeIsBlockedAfterSecuritySupportEnds(t *testing.T) {
	if runtime, _ := Lookup("8.4", time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC)); runtime.Status != Blocked {
		t.Fatalf("expected blocked runtime, got %q", runtime.Status)
	}
}
