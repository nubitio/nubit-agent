package command

import (
	"context"
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
	if (kind == "backup" || kind == "archive") && !executor.capacity.ScratchAvailable("/") {
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
		return finish, nil
	}
	if siteChange == "delete" {
		return func(success bool) {
			if success {
				executor.capacity.ReleaseSite(siteID)
			}
			finish(success)
		}, nil
	}
	previous, existed := executor.capacity.Reservation(siteID)
	if err := executor.capacity.ReserveSite(siteID, requested); err != nil {
		finish(false)
		telemetry.RecordAdmission(ctx, "site", "rejected")
		return nil, fmt.Errorf("site %s admission: %w", siteID, err)
	}
	telemetry.RecordAdmission(ctx, func() string {
		if siteChange != "" {
			return "site"
		}
		return kind
	}(), "accepted")
	return func(success bool) {
		if success {
			finish(true)
			return
		}
		if existed {
			_ = executor.capacity.ReserveSite(siteID, previous)
		} else {
			executor.capacity.ReleaseSite(siteID)
		}
		finish(false)
	}, nil
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
