package command

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nubitio/nubit-agent/internal/access"
	"github.com/nubitio/nubit-agent/internal/backup"
	"github.com/nubitio/nubit-agent/internal/cron"
	"github.com/nubitio/nubit-agent/internal/database"
	"github.com/nubitio/nubit-agent/internal/files"
	"github.com/nubitio/nubit-agent/internal/logs"
	"github.com/nubitio/nubit-agent/internal/site"
)

type Result struct {
	CommandID string          `json:"commandId"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output"`
}

type Store interface {
	Get(key string) (Result, bool)
	Save(key string, result Result) error
}

type MemoryStore struct {
	mu      sync.RWMutex
	results map[string]Result
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{results: make(map[string]Result)}
}

func (store *MemoryStore) Get(key string) (Result, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	result, found := store.results[key]
	return result, found
}

func (store *MemoryStore) Save(key string, result Result) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.results[key] = result

	return nil
}

type Executor struct {
	mu      sync.Mutex
	store   Store
	sites   SiteProvisioner
	sftp    SFTPProvisioner
	db      DatabaseProvisioner
	files   FilesProvisioner
	cron    CronProvisioner
	logs    LogProvisioner
	backups BackupProvisioner
}

type SiteProvisioner interface {
	Create(domain, systemUser, phpVersion string) (site.CreateResult, error)
	Inspect(siteID string) (site.State, error)
	SetPHPVersion(siteID, phpVersion string) (site.PHPVersionResult, error)
	Suspend(siteID string) (site.LifecycleResult, error)
	Resume(siteID string) (site.LifecycleResult, error)
	AddDomain(siteID, domain string) (site.DomainResult, error)
	RemoveDomain(siteID, domain string) (site.DomainResult, error)
	Delete(siteID string, confirmed bool) (site.DeleteResult, error)
	RuntimeInventory() ([]site.RuntimeInfo, error)
	RemoveRuntime(phpVersion string, confirmed bool) (site.RemoveRuntimeResult, error)
	Reconcile() ([]site.Drift, error)
}

type SFTPProvisioner interface {
	Create(siteID, publicKey string) (access.Result, error)
	UpdateKey(siteID, publicKey string) (access.Result, error)
	Revoke(siteID string) (access.Result, error)
}

type DatabaseProvisioner interface {
	Create(siteID, database, role, password string) (database.Result, error)
	RotatePassword(siteID, database, role, password string) (database.Result, error)
	Delete(siteID, database, role string, confirmed bool) (database.Result, error)
}

type FilesProvisioner interface {
	List(siteID, rel string) (files.ListResult, error)
	Mkdir(siteID, rel string) error
	Write(siteID, rel string, content []byte) error
	Read(siteID, rel string) (files.ReadResult, error)
	Delete(siteID, rel string) error
	Rename(siteID, from, to string) error
	Unzip(siteID, rel string) error
	Usage(siteID string) (files.UsageResult, error)
}

type CronProvisioner interface {
	List(siteID string) ([]cron.Task, error)
	Replace(siteID string, tasks []cron.Task) ([]cron.Task, error)
}

type LogProvisioner interface {
	Read(siteID, source string, limit int) (logs.Result, error)
}

type BackupProvisioner interface {
	List(siteID string) ([]backup.Archive, error)
	Create(siteID string) (backup.Archive, error)
	Restore(siteID, name string, confirmed bool) error
}

func NewExecutor(store Store, services ...any) *Executor {
	executor := &Executor{store: store}
	for _, service := range services {
		if sites, ok := service.(SiteProvisioner); ok {
			executor.sites = sites
		}
		if sftp, ok := service.(SFTPProvisioner); ok {
			executor.sftp = sftp
		}
		if databases, ok := service.(DatabaseProvisioner); ok {
			executor.db = databases
		}
		if fileManager, ok := service.(FilesProvisioner); ok {
			executor.files = fileManager
		}
		if cronManager, ok := service.(CronProvisioner); ok {
			executor.cron = cronManager
		}
		if logManager, ok := service.(LogProvisioner); ok {
			executor.logs = logManager
		}
		if backupManager, ok := service.(BackupProvisioner); ok {
			executor.backups = backupManager
		}
	}
	return executor
}

func (executor *Executor) Execute(command Command) (Result, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()

	if err := command.Validate(); err != nil {
		return Result{}, err
	}
	cacheResult := !isSiteFilesCommand(command.Type)
	if cacheResult {
		if result, found := executor.store.Get(command.IdempotencyKey); found {
			return result, nil
		}
	}

	var output []byte
	var err error
	switch command.Type {
	case SystemPing:
		output, err = json.Marshal(map[string]string{"receivedAt": time.Now().UTC().Format(time.RFC3339)})
	case SiteCreate:
		site, parseErr := parseSiteCreate(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		created, createErr := executor.sites.Create(site.Domain, site.SystemUser, site.PHPVersion)
		if createErr != nil {
			return Result{}, createErr
		}
		output, err = json.Marshal(map[string]string{"siteId": created.SiteID, "documentRoot": created.DocumentRoot, "phpSocket": created.PHPSocket, "caddyConfigHash": created.CaddyConfigHash, "phpConfigHash": created.PHPConfigHash})
	case SiteInspect:
		request, parseErr := parseSiteInspect(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		state, inspectErr := executor.sites.Inspect(request.SiteID)
		if inspectErr != nil {
			return Result{}, inspectErr
		}
		output, err = json.Marshal(state)
	case SiteSuspend, SiteResume:
		request, parseErr := parseSiteInspect(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		var lifecycle site.LifecycleResult
		var lifecycleErr error
		if command.Type == SiteSuspend {
			lifecycle, lifecycleErr = executor.sites.Suspend(request.SiteID)
		} else {
			lifecycle, lifecycleErr = executor.sites.Resume(request.SiteID)
		}
		if lifecycleErr != nil {
			return Result{}, lifecycleErr
		}
		output, err = json.Marshal(lifecycle)
	case SiteAddDomain, SiteRemoveDomain:
		request, parseErr := parseSiteDomain(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		var domains site.DomainResult
		var domainErr error
		if command.Type == SiteAddDomain {
			domains, domainErr = executor.sites.AddDomain(request.SiteID, request.Domain)
		} else {
			domains, domainErr = executor.sites.RemoveDomain(request.SiteID, request.Domain)
		}
		if domainErr != nil {
			return Result{}, domainErr
		}
		output, err = json.Marshal(domains)
	case SiteDelete:
		request, parseErr := parseSiteDelete(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		deleted, deleteErr := executor.sites.Delete(request.SiteID, request.Confirm)
		if deleteErr != nil {
			return Result{}, deleteErr
		}
		output, err = json.Marshal(deleted)
	case PHPSetVersion:
		request, parseErr := parsePHPSetVersion(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		changed, changeErr := executor.sites.SetPHPVersion(request.SiteID, request.PHPVersion)
		if changeErr != nil {
			return Result{}, changeErr
		}
		output, err = json.Marshal(changed)
	case PHPRuntimeInspect:
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		runtimes, inspectErr := executor.sites.RuntimeInventory()
		if inspectErr != nil {
			return Result{}, inspectErr
		}
		output, err = json.Marshal(map[string]any{"runtimes": runtimes})
	case PHPRuntimeRemove:
		request, parseErr := parsePHPRuntime(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		removed, removeErr := executor.sites.RemoveRuntime(request.PHPVersion, request.Confirm)
		if removeErr != nil {
			return Result{}, removeErr
		}
		output, err = json.Marshal(removed)
	case SystemReconcile:
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		drifts, reconcileErr := executor.sites.Reconcile()
		if reconcileErr != nil {
			return Result{}, reconcileErr
		}
		output, err = json.Marshal(map[string]any{"drifts": drifts})
	case SFTPCreate, SFTPUpdateKey, SFTPRevoke:
		request, parseErr := parseSFTP(command.Payload, command.Type != SFTPRevoke)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sftp == nil {
			return Result{}, errors.New("SFTP provisioner is not configured")
		}
		var accessResult access.Result
		var accessErr error
		switch command.Type {
		case SFTPCreate:
			accessResult, accessErr = executor.sftp.Create(request.SiteID, request.PublicKey)
		case SFTPUpdateKey:
			accessResult, accessErr = executor.sftp.UpdateKey(request.SiteID, request.PublicKey)
		case SFTPRevoke:
			accessResult, accessErr = executor.sftp.Revoke(request.SiteID)
		}
		if accessErr != nil {
			return Result{}, accessErr
		}
		output, err = json.Marshal(accessResult)
	case DatabaseCreate, DatabaseRotatePassword, DatabaseDelete:
		request, parseErr := parseDatabase(command.Payload, command.Type != DatabaseDelete, command.Type == DatabaseDelete)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.db == nil {
			return Result{}, errors.New("database provisioner is not configured")
		}
		var databaseResult database.Result
		var databaseErr error
		switch command.Type {
		case DatabaseCreate:
			databaseResult, databaseErr = executor.db.Create(request.SiteID, request.Database, request.Role, request.Password)
		case DatabaseRotatePassword:
			databaseResult, databaseErr = executor.db.RotatePassword(request.SiteID, request.Database, request.Role, request.Password)
		case DatabaseDelete:
			databaseResult, databaseErr = executor.db.Delete(request.SiteID, request.Database, request.Role, request.Confirm)
		}
		if databaseErr != nil {
			return Result{}, databaseErr
		}
		output, err = json.Marshal(databaseResult)
	case SiteFilesList, SiteFilesMkdir, SiteFilesWrite, SiteFilesRead, SiteFilesDelete, SiteFilesUnzip, SiteFilesRename:
		request, parseErr := parseSiteFiles(command.Payload, command.Type)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.files == nil {
			return Result{}, errors.New("file manager is not configured")
		}
		output, err = executor.executeFiles(command.Type, request)
	case SiteUsage:
		request, parseErr := parseSiteInspect(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.files == nil {
			return Result{}, errors.New("file manager is not configured")
		}
		usage, usageErr := executor.files.Usage(request.SiteID)
		if usageErr != nil {
			return Result{}, usageErr
		}
		output, err = json.Marshal(usage)
	case SiteLogsRead:
		request, parseErr := parseLogs(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.logs == nil {
			return Result{}, errors.New("log manager is not configured")
		}
		logged, logErr := executor.logs.Read(request.SiteID, request.Source, request.Lines)
		if logErr != nil {
			return Result{}, logErr
		}
		output, err = json.Marshal(logged)
	case SiteCronList:
		request, parseErr := parseSiteInspect(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.cron == nil {
			return Result{}, errors.New("cron manager is not configured")
		}
		tasks, cronErr := executor.cron.List(request.SiteID)
		if cronErr != nil {
			return Result{}, cronErr
		}
		output, err = json.Marshal(map[string]any{"tasks": tasks})
	case SiteCronReplace:
		request, parseErr := parseCronReplace(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.cron == nil {
			return Result{}, errors.New("cron manager is not configured")
		}
		tasks, cronErr := executor.cron.Replace(request.SiteID, request.Tasks)
		if cronErr != nil {
			return Result{}, cronErr
		}
		output, err = json.Marshal(map[string]any{"tasks": tasks})
	case SiteBackupList:
		request, parseErr := parseSiteInspect(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.backups == nil {
			return Result{}, errors.New("backup manager is not configured")
		}
		archives, backupErr := executor.backups.List(request.SiteID)
		if backupErr != nil {
			return Result{}, backupErr
		}
		output, err = json.Marshal(map[string]any{"archives": archives})
	case SiteBackupCreate:
		request, parseErr := parseSiteInspect(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.backups == nil {
			return Result{}, errors.New("backup manager is not configured")
		}
		archive, backupErr := executor.backups.Create(request.SiteID)
		if backupErr != nil {
			return Result{}, backupErr
		}
		output, err = json.Marshal(archive)
	case SiteBackupRestore:
		request, parseErr := parseBackupRestore(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.backups == nil {
			return Result{}, errors.New("backup manager is not configured")
		}
		if err := executor.backups.Restore(request.SiteID, request.Name, request.Confirm); err != nil {
			return Result{}, err
		}
		output, err = json.Marshal(map[string]any{"ok": true})
	default:
		return Result{}, errors.New("unsupported command type")
	}
	if err != nil {
		return Result{}, err
	}

	result := Result{
		CommandID: command.ID,
		Status:    "succeeded",
		Output:    output,
	}
	if cacheResult {
		if err := executor.store.Save(command.IdempotencyKey, result); err != nil {
			return Result{}, err
		}
	}

	return result, nil
}

func isSiteFilesCommand(commandType string) bool {
	switch commandType {
	case SiteFilesList, SiteFilesMkdir, SiteFilesWrite, SiteFilesRead, SiteFilesDelete,
		SiteFilesUnzip, SiteFilesRename, SiteUsage, SiteLogsRead,
		SiteCronList, SiteCronReplace, SiteBackupList, SiteBackupCreate, SiteBackupRestore:
		return true
	default:
		return false
	}
}

func (executor *Executor) executeFiles(commandType string, request SiteFilesPayload) ([]byte, error) {
	switch commandType {
	case SiteFilesList:
		listed, err := executor.files.List(request.SiteID, request.Path)
		if err != nil {
			return nil, err
		}
		return json.Marshal(listed)
	case SiteFilesMkdir:
		if err := executor.files.Mkdir(request.SiteID, request.Path); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]string{"ok": "true", "path": request.Path})
	case SiteFilesWrite:
		content, err := base64.StdEncoding.DecodeString(request.ContentBase64)
		if err != nil {
			return nil, errors.New("file content is not valid base64")
		}
		if err := executor.files.Write(request.SiteID, request.Path, content); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"ok": true, "path": request.Path, "size": len(content)})
	case SiteFilesRead:
		read, err := executor.files.Read(request.SiteID, request.Path)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"name":          read.Name,
			"size":          read.Size,
			"contentBase64": base64.StdEncoding.EncodeToString(read.Content),
		})
	case SiteFilesDelete:
		if err := executor.files.Delete(request.SiteID, request.Path); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"ok": true, "path": request.Path})
	case SiteFilesUnzip:
		if err := executor.files.Unzip(request.SiteID, request.Path); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"ok": true, "path": request.Path})
	case SiteFilesRename:
		if err := executor.files.Rename(request.SiteID, request.Path, request.To); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"ok": true, "path": request.To})
	default:
		return nil, errors.New("unsupported command type")
	}
}
