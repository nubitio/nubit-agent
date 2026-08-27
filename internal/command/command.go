package command

import (
	"encoding/json"
	"errors"
	"strings"
)

const SystemPing = "system.ping"
const SiteCreate = "site.create"
const SiteInspect = "site.inspect"
const SiteSuspend = "site.suspend"
const SiteResume = "site.resume"
const SiteAddDomain = "site.add-domain"
const SiteRemoveDomain = "site.remove-domain"
const SiteDelete = "site.delete"
const PHPSetVersion = "php.set-version"
const PHPRuntimeInspect = "php.runtime.inspect"
const PHPRuntimeRemove = "php.runtime.remove"
const SystemReconcile = "system.reconcile"
const SFTPCreate = "sftp.create"
const SFTPUpdateKey = "sftp.update-key"
const SFTPRevoke = "sftp.revoke"
const DatabaseCreate = "database.create"
const DatabaseRotatePassword = "database.rotate-password"
const DatabaseDelete = "database.delete"
const SiteFilesList = "site.files.list"
const SiteFilesMkdir = "site.files.mkdir"
const SiteFilesWrite = "site.files.write"
const SiteFilesRead = "site.files.read"
const SiteFilesDelete = "site.files.delete"
const SiteFilesUnzip = "site.files.unzip"
const SiteFilesRename = "site.files.rename"
const SiteUsage = "site.usage"
const SiteLogsRead = "site.logs.read"
const SiteCronList = "site.cron.list"
const SiteCronReplace = "site.cron.replace"
const SiteBackupList = "site.backup.list"
const SiteBackupCreate = "site.backup.create"
const SiteBackupRestore = "site.backup.restore"

type Command struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Version        int             `json:"version"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Payload        json.RawMessage `json:"payload"`
}

func (command Command) Validate() error {
	if strings.TrimSpace(command.ID) == "" {
		return errors.New("command id is required")
	}
	if strings.TrimSpace(command.Type) == "" {
		return errors.New("command type is required")
	}
	if command.Version != 1 {
		return errors.New("unsupported command version")
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" {
		return errors.New("idempotency key is required")
	}
	if !json.Valid(command.Payload) {
		return errors.New("command payload must be valid JSON")
	}

	return nil
}
