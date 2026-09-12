package command

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nubitio/nubit-agent/internal/capacity"
	"github.com/nubitio/nubit-agent/internal/site"
	"github.com/nubitio/nubit-agent/internal/telemetry"
)

func (executor *Executor) admit(ctx context.Context, command Command) (func(bool), error) {
	if executor.capacity == nil {
		return func(bool) {}, nil
	}
	kind := operationKind(command.Type)
	release, err := executor.capacity.AcquireOperation(ctx, kind)
	if err != nil {
		telemetry.RecordAdmission(ctx, kind, "rejected")
		return nil, fmt.Errorf("%s operation admission: %w", kind, err)
	}
	if (kind == "backup" || kind == "restore" || kind == "archive") && !executor.capacity.ScratchAvailable("/") {
		release()
		telemetry.RecordAdmission(ctx, kind, "rejected")
		return nil, fmt.Errorf("%s operation admission: insufficient scratch free space", kind)
	}
	finished := false
	finish := func(_ bool) {
		if !finished {
			release()
			finished = true
		}
	}

	var siteID string
	var requested capacity.Resources
	var siteChange string
	var siteAdmission capacity.SiteAdmission
	if command.Type == SiteCreate {
		request, parseErr := parseSiteCreate(command.Payload)
		if parseErr != nil {
			finish(false)
			return nil, parseErr
		}
		siteID, requested, siteChange = request.Domain, siteResources(request.Resources), "create"
	} else if command.Type == SiteSetResources {
		request, parseErr := parseSiteResources(command.Payload)
		if parseErr != nil {
			finish(false)
			return nil, parseErr
		}
		siteID, requested, siteChange = request.SiteID, siteResources(request.Resources), "update"
	} else if command.Type == SiteDelete {
		request, parseErr := parseSiteDelete(command.Payload)
		if parseErr != nil {
			finish(false)
			return nil, parseErr
		}
		siteID, siteChange = request.SiteID, "delete"
	}
	if siteChange == "" {
		if siteID, ok := siteMutationID(command); ok && siteID != "" {
			admission, beginErr := executor.capacity.BeginCommand(siteID)
			if beginErr != nil {
				finish(false)
				return nil, fmt.Errorf("site %s admission: %w", siteID, beginErr)
			}
			return func(success bool) {
				executor.capacity.FinishCommand(admission)
				finish(success)
			}, nil
		}
		return finish, nil
	}
	if siteChange == "delete" {
		var admission capacity.SiteAdmission
		admission, err = executor.capacity.BeginCommand(siteID)
		if err != nil {
			finish(false)
			return nil, fmt.Errorf("site %s admission: %w", siteID, err)
		}
		return func(success bool) {
			if success {
				executor.capacity.ReleaseSite(siteID)
			} else {
				executor.capacity.FinishCommand(admission)
			}
			finish(success)
		}, nil
	}
	var beginErr error
	siteAdmission, beginErr = executor.capacity.BeginSite(siteID, requested)
	if beginErr != nil {
		finish(false)
		telemetry.RecordAdmission(ctx, "site", "rejected")
		return nil, fmt.Errorf("site %s admission: %w", siteID, beginErr)
	}
	telemetry.RecordAdmission(ctx, func() string {
		if siteChange != "" {
			return "site"
		}
		return kind
	}(), "accepted")
	return func(success bool) {
		if success {
			executor.capacity.FinishSite(siteAdmission, true)
			finish(true)
			return
		}
		executor.capacity.FinishSite(siteAdmission, false)
		finish(false)
	}, nil
}

func siteMutationID(command Command) (string, bool) {
	switch command.Type {
	case SiteCreate:
		var request SiteCreatePayload
		if json.Unmarshal(command.Payload, &request) == nil {
			return request.Domain, true
		}
	case SiteSetResources, RuntimeSetVersion, SiteSuspend, SiteResume, SiteAddDomain, SiteRemoveDomain,
		SiteAppInstall, SiteAppUpdate, SiteAppAdminPassword, SFTPCreate, SFTPUpdateKey, SFTPRevoke,
		SFTPUserCreate, SFTPUserUpdateKey, SFTPUserDelete, DatabaseCreate, DatabaseRotatePassword,
		DatabaseDelete, DatabaseUserCreate, DatabaseUserDelete, DatabaseGrant, DatabaseRevoke,
		SiteFilesMkdir, SiteFilesWrite, SiteFilesDelete, SiteFilesUnzip, SiteFilesRename,
		SiteCronReplace, SiteBackupCreate, SiteBackupRestore:
		var request struct {
			SiteID string `json:"siteId"`
		}
		if json.Unmarshal(command.Payload, &request) == nil {
			return request.SiteID, true
		}
	}
	return "", false
}

func siteResources(r site.Resources) capacity.Resources {
	r = r.WithDefaults()
	return capacity.Resources{CPUmilli: int64(r.Workers) * 50, MemoryBytes: int64(r.Workers) * int64(r.MemoryLimitMB) * 1024 * 1024, PHPWorkers: int64(r.Workers), PIDs: int64(r.Workers) + 8}
}

func operationKind(commandType string) string {
	switch commandType {
	case SiteBackupCreate, SiteBackupVerify:
		return "backup"
	case SiteBackupRestore:
		return "restore"
	case SiteFilesUnzip, SiteAppInstall, SiteAppUpdate:
		return "archive"
	case SiteUsage:
		return "usage"
	case SiteLogsRead:
		return "logs"
	case DatabaseCreate, DatabaseRotatePassword, DatabaseDelete, DatabaseUserCreate, DatabaseUserDelete, DatabaseGrant, DatabaseRevoke:
		return "database"
	default:
		return ""
	}
}
