package command

import (
	"encoding/json"
	"errors"

	"github.com/nubitio/nubit-agent/internal/cron"
)

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
