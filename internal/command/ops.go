package command

import (
	"encoding/json"
	"errors"
	"regexp"

	"github.com/nubitio/nubit-agent/internal/cron"
)

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var sha256Pattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type SystemUpdatePayload struct {
	Tag    string `json:"tag"`
	SHA256 string `json:"sha256"`
}

func parseSystemUpdate(payload json.RawMessage) (SystemUpdatePayload, error) {
	var request SystemUpdatePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !releaseTagPattern.MatchString(request.Tag) {
		return request, errors.New("system update tag must be vMAJOR.MINOR.PATCH")
	}
	if !sha256Pattern.MatchString(request.SHA256) {
		return request, errors.New("system update sha256 must be 64 hexadecimal characters")
	}
	return request, nil
}

type CronReplacePayload struct {
	SiteID string      `json:"siteId"`
	Tasks  []cron.Task `json:"tasks"`
}

type LogsPayload struct {
	SiteID string `json:"siteId"`
	Source string `json:"source"`
	Lines  int    `json:"lines"`
}

type BackupRestorePayload struct {
	SiteID  string `json:"siteId"`
	Name    string `json:"name"`
	Confirm bool   `json:"confirm"`
}

// BackupCreatePayload carries the plan's retention window (ADR-001 tiers). A
// control plane that says nothing keeps the legacy "7 newest" pruning.
type BackupCreatePayload struct {
	SiteID        string `json:"siteId"`
	RetentionDays int    `json:"retentionDays"`
}

func parseCronReplace(payload json.RawMessage) (CronReplacePayload, error) {
	var request CronReplacePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	return request, nil
}

func parseLogs(payload json.RawMessage) (LogsPayload, error) {
	var request LogsPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	return request, nil
}

func parseSystemReset(payload json.RawMessage) (bool, error) {
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return false, err
	}
	if !request.Confirm {
		return false, errors.New("system reset requires explicit confirmation")
	}

	return true, nil
}

func parseBackupRestore(payload json.RawMessage) (BackupRestorePayload, error) {
	var request BackupRestorePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) || request.Name == "" {
		return request, errors.New("backup payload is invalid")
	}
	return request, nil
}

func parseBackupCreate(payload json.RawMessage) (BackupCreatePayload, error) {
	var request BackupCreatePayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return request, err
	}
	if !domainName.MatchString(request.SiteID) {
		return request, errors.New("site id is invalid")
	}
	if request.RetentionDays < 0 {
		request.RetentionDays = 0
	}
	return request, nil
}
