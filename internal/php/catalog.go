package php

import (
	"errors"
	"time"
)

type Status string

const (
	Supported  Status = "supported"
	Deprecated Status = "deprecated"
	Blocked    Status = "blocked"
)

type Runtime struct {
	Version       string    `json:"version"`
	Status        Status    `json:"status"`
	Recommended   bool      `json:"recommended"`
	SecurityUntil time.Time `json:"securityUntil"`
}

var runtimes = map[string]Runtime{
	"8.3": {Version: "8.3", Status: Deprecated, SecurityUntil: date(2027, 12, 31)},
	"8.4": {Version: "8.4", Status: Supported, Recommended: true, SecurityUntil: date(2028, 12, 31)},
	"8.5": {Version: "8.5", Status: Supported, SecurityUntil: date(2029, 12, 31)},
}

func Lookup(version string, now time.Time) (Runtime, bool) {
	runtime, found := runtimes[version]
	if !found {
		return Runtime{}, false
	}
	if now.After(runtime.SecurityUntil) {
		runtime.Status = Blocked
	}
	return runtime, true
}

func List(now time.Time) []Runtime {
	versions := []string{"8.3", "8.4", "8.5"}
	result := make([]Runtime, 0, len(versions))
	for _, version := range versions {
		runtime, _ := Lookup(version, now)
		result = append(result, runtime)
	}
	return result
}

func ValidateInstalled(version string, now time.Time) error {
	runtime, found := Lookup(version, now)
	if !found || runtime.Status == Blocked {
		return errors.New("unsupported PHP version")
	}
	return nil
}

func ValidateNewSite(version string, now time.Time) error {
	runtime, found := Lookup(version, now)
	if !found || runtime.Status == Blocked {
		return errors.New("unsupported PHP version")
	}
	if runtime.Status == Deprecated {
		return errors.New("PHP version is deprecated for new sites")
	}
	return nil
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 23, 59, 59, 0, time.UTC)
}
