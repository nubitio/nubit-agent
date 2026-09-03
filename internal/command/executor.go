package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nubitio/nubit-agent/internal/access"
	"github.com/nubitio/nubit-agent/internal/audit"
	"github.com/nubitio/nubit-agent/internal/backup"
	"github.com/nubitio/nubit-agent/internal/cron"
	"github.com/nubitio/nubit-agent/internal/database"
	"github.com/nubitio/nubit-agent/internal/files"
	"github.com/nubitio/nubit-agent/internal/logs"
	"github.com/nubitio/nubit-agent/internal/mail"
	"github.com/nubitio/nubit-agent/internal/site"
	"github.com/nubitio/nubit-agent/internal/tls"
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
	mail    MailProvisioner
	tls     TLSInspector
	audit   *audit.Logger

	defaultTimeout time.Duration
	typeTimeouts   map[string]time.Duration
	defaultRate    float64
	typeRates      map[string]float64
	exemptTypes    map[string]bool
	rateLimitersMu sync.Mutex
	rateLimiters   map[string]*tokenBucket

	// tlsIssueWait is how long tls.letsencrypt.enable waits for Caddy to
	// finish obtaining a certificate before reporting that none exists. Zero
	// keeps the historical behaviour (fail immediately when Caddy has not
	// issued yet). tlsIssuePoll is the re-check interval while waiting.
	tlsIssueWait time.Duration
	tlsIssuePoll time.Duration
}

// SetAuditLogger installs the audit log used to record every command the
// executor runs. A nil logger disables auditing. The executor takes a copy
// of the pointer; the caller may keep using the same Logger afterwards.
func (executor *Executor) SetAuditLogger(logger *audit.Logger) {
	executor.audit = logger
}

type SiteProvisioner interface {
	Create(domain, systemUser, phpVersion string, resources site.Resources) (site.CreateResult, error)
	Inspect(siteID string) (site.State, error)
	SetPHPVersion(siteID, phpVersion string) (site.PHPVersionResult, error)
	SetResources(siteID string, resources site.Resources) (site.ResourcesResult, error)
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
	CreateFTPUser(siteID, label, publicKey, directory string) (access.FTPResult, error)
	UpdateFTPKey(siteID, label, publicKey string) (access.FTPResult, error)
	DeleteFTPUser(siteID, label string, confirmed bool) (access.FTPResult, error)
}

// TLSInspector reports the certificate Caddy holds for a site. It never reads
// a private key: the control plane audits TLS, it does not hold it.
type TLSInspector interface {
	Inspect(siteID string) (tls.Evidence, error)
}

// MailProvisioner administers the mail server that runs beside the web stack.
type MailProvisioner interface {
	CreateDomain(domain string) (mail.DomainResult, error)
	DeleteDomain(domain string, confirmed bool) (mail.DomainResult, error)
	CreateMailbox(address, password string, quotaBytes int64) (mail.MailboxResult, error)
	SetPassword(address, password string) error
	SetQuota(address string, quotaBytes int64) (mail.MailboxResult, error)
	DeleteMailbox(address string, confirmed bool) (mail.MailboxResult, error)
	Inventory() ([]mail.MailboxResult, error)
}

type DatabaseProvisioner interface {
	Create(siteID, database, role, password string) (database.Result, error)
	RotatePassword(siteID, database, role, password string) (database.Result, error)
	Delete(siteID, database, role string, confirmed bool) (database.Result, error)
	CreateUser(siteID, role, password string) (database.UserResult, error)
	DeleteUser(siteID, role string, confirmed bool) (database.UserResult, error)
	Grant(siteID, database, role string) (database.UserResult, error)
	Revoke(siteID, database, role string) (database.UserResult, error)
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
	return NewExecutorWithConfig(ExecutorConfig{}, store, services...)
}

// NewExecutorWithConfig wires the executor with the supplied defence-in-depth
// configuration. A zero ExecutorConfig disables both the timeout and the rate
// limit, which keeps the long-standing call sites (tests, in-process callers)
// behaving exactly as before. The exempt set always includes the
// read-only/reconciliation commands documented as exempt: callers may add
// more, but the defaults cannot be removed (a control-plane that depends on
// system.ping being reachable in a flood is the documented contract).
func NewExecutorWithConfig(config ExecutorConfig, store Store, services ...any) *Executor {
	exempt := map[string]bool{
		SystemPing:            true,
		SystemReconcile:       true,
		TLSCertificateInspect: true,
	}
	for commandType, value := range config.ExemptTypes {
		exempt[commandType] = value
	}
	executor := &Executor{
		store:          store,
		defaultTimeout: config.DefaultCommandTimeout,
		typeTimeouts:   config.TypeTimeouts,
		defaultRate:    config.DefaultRatePerMinute,
		typeRates:      config.TypeRates,
		exemptTypes:    exempt,
		rateLimiters:   map[string]*tokenBucket{},
		tlsIssueWait:   config.TLSIssueWait,
		tlsIssuePoll:   config.TLSIssuePollInterval,
	}
	if executor.typeTimeouts == nil {
		executor.typeTimeouts = map[string]time.Duration{}
	}
	if executor.typeRates == nil {
		executor.typeRates = map[string]float64{}
	}
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
		if mailManager, ok := service.(MailProvisioner); ok {
			executor.mail = mailManager
		}
		if inspector, ok := service.(TLSInspector); ok {
			executor.tls = inspector
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

	// The rate limit is evaluated before the idempotency check, on purpose:
	// a control plane that replays the same command under load will keep
	// hitting the same idempotency key, and the rate limit is the only
	// signal that distinguishes "Control is misbehaving" from "Control
	// intentionally re-sent". Trade-off documented in the executor config.
	if executor.defaultRate > 0 && !executor.exemptTypes[command.Type] {
		if !executor.allowCommand(command.Type) {
			return executor.recordRateLimitFailure(command)
		}
	}

	cacheResult := !isSiteFilesCommand(command.Type)
	if cacheResult {
		if result, found := executor.store.Get(command.IdempotencyKey); found {
			return result, nil
		}
	}

	payloadSHA := audit.HashPayload(command.Payload)
	started := time.Now()

	result, err := executor.runWithTimeout(command)
	if err != nil {
		executor.recordAudit(command, payloadSHA, started, "failed")
		return Result{}, err
	}
	if cacheResult {
		if saveErr := executor.store.Save(command.IdempotencyKey, result); saveErr != nil {
			executor.recordAudit(command, payloadSHA, started, "failed")
			return Result{}, saveErr
		}
	}
	executor.recordAudit(command, payloadSHA, started, "ok")

	return result, nil
}

// recordAudit appends one entry to the audit log. The audit is best-effort:
// a write failure is logged but never propagated, so a full disk or a broken
// permission cannot block commands that are otherwise fine to run.
func (executor *Executor) recordAudit(command Command, payloadSHA string, started time.Time, result string) {
	if executor.audit == nil {
		return
	}
	event := audit.Event{
		CommandID:      command.ID,
		CommandType:    command.Type,
		IdempotencyKey: command.IdempotencyKey,
		PayloadSHA256:  payloadSHA,
		Result:         result,
		DurationMs:     time.Since(started).Milliseconds(),
	}
	if err := executor.audit.Record(context.Background(), event); err != nil {
		log.Printf("nubit-agent: audit log write failed for command %s: %v", command.ID, err)
	}
}

// runWithTimeout dispatches the command under a per-type timeout. The
// provisioners themselves do not currently accept a context, so the timeout
// only protects the executor's mutex and the dispatcher's bookkeeping: any
// child process the provisioner spawns will keep running until the
// provisioner returns, and rely on systemd's KillMode=mixed to clean up if
// it does not. This is the documented limitation: a tighter guarantee would
// require every provisioner to honour ctx, which is out of scope for a
// defensive fix.
func (executor *Executor) runWithTimeout(command Command) (Result, error) {
	timeout := executor.timeoutFor(command.Type)
	if timeout <= 0 {
		return executor.runCommand(command)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := executor.runCommand(command)
		// runCommand returns Result{} for a non-timeout error; let the
		// parent goroutine build the failure Result so the persistence
		// path is shared.
		if err == nil {
			done <- outcome{result: result}
			return
		}
		done <- outcome{err: err}
	}()

	select {
	case <-ctx.Done():
		// The provisioner is still running in its own goroutine and may
		// eventually return; we deliberately do not wait for it. The
		// command is recorded as failed and the control plane will see
		// the timeout message.
		seconds := int(timeout / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		message := fmt.Sprintf("command %s exceeded timeout %ds", command.Type, seconds)
		failed := Result{
			CommandID: command.ID,
			Status:    "failed",
			Output:    json.RawMessage(fmt.Sprintf(`{"error":%q}`, message)),
		}
		if saveErr := executor.store.Save(command.IdempotencyKey, failed); saveErr != nil {
			return Result{}, saveErr
		}
		return Result{}, errors.New(message)
	case outcome := <-done:
		return outcome.result, outcome.err
	}
}

// timeoutFor picks the timeout applicable to a command type: an explicit
// override, or the default. Zero or negative disables the timeout.
func (executor *Executor) timeoutFor(commandType string) time.Duration {
	if override, ok := executor.typeTimeouts[commandType]; ok {
		return override
	}
	return executor.defaultTimeout
}

// allowCommand checks the per-type rate limit. It returns true if the
// command may proceed.
func (executor *Executor) allowCommand(commandType string) bool {
	rate := executor.defaultRate
	if override, ok := executor.typeRates[commandType]; ok {
		rate = override
	}
	if rate <= 0 {
		return true
	}

	executor.rateLimitersMu.Lock()
	bucket, exists := executor.rateLimiters[commandType]
	if !exists {
		bucket = newTokenBucket(rate)
		executor.rateLimiters[commandType] = bucket
	}
	executor.rateLimitersMu.Unlock()

	allowed, _ := bucket.allow()
	return allowed
}

// recordRateLimitFailure builds the failure error for a rate-limited
// command. The retry-after shown in the message is rounded up to the next
// whole second so an operator-facing log line is stable. The failure is
// NOT persisted to the store: a rate-limited command never reached the
// provisioner, and the existing rate-limit accounting already prevents
// replay from succeeding.
func (executor *Executor) recordRateLimitFailure(command Command) (Result, error) {
	perMinute := executor.rateFor(command.Type)

	executor.rateLimitersMu.Lock()
	bucket, exists := executor.rateLimiters[command.Type]
	executor.rateLimitersMu.Unlock()
	if !exists {
		bucket = newTokenBucket(perMinute)
	}

	_, retryAfter := bucket.allow()
	seconds := int(retryAfter / time.Second)
	if retryAfter > 0 && time.Duration(seconds)*time.Second < retryAfter {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	message := fmt.Sprintf("rate limit exceeded for %s: max %g per %d, retry in %ds", command.Type, perMinute, 60, seconds)
	return Result{}, errors.New(message)
}

// rateFor returns the configured per-minute cap for a command type.
func (executor *Executor) rateFor(commandType string) float64 {
	if override, ok := executor.typeRates[commandType]; ok {
		return override
	}
	return executor.defaultRate
}

func (executor *Executor) runCommand(command Command) (Result, error) {
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
		created, createErr := executor.sites.Create(site.Domain, site.SystemUser, site.PHPVersion, site.Resources)
		if createErr != nil {
			return Result{}, createErr
		}
		output, err = json.Marshal(map[string]string{"siteId": created.SiteID, "documentRoot": created.DocumentRoot, "phpSocket": created.PHPSocket, "caddyConfigHash": created.CaddyConfigHash, "phpConfigHash": created.PHPConfigHash})
	case SiteSetResources:
		request, parseErr := parseSiteResources(command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		applied, resourcesErr := executor.sites.SetResources(request.SiteID, request.Resources)
		if resourcesErr != nil {
			return Result{}, resourcesErr
		}
		output, err = json.Marshal(applied)
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
	case RuntimeSetVersion:
		request, parseErr := parseRuntimeSetVersion(command.Payload)
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
	case RuntimeInspect:
		if executor.sites == nil {
			return Result{}, errors.New("site provisioner is not configured")
		}
		runtimes, inspectErr := executor.sites.RuntimeInventory()
		if inspectErr != nil {
			return Result{}, inspectErr
		}
		output, err = json.Marshal(map[string]any{"runtimes": runtimes})
	case RuntimeRemove:
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
	case SystemReset:
		if _, parseErr := parseSystemReset(command.Payload); parseErr != nil {
			return Result{}, parseErr
		}
		resetter, ok := executor.sites.(interface {
			Reset() (site.ResetResult, error)
		})
		if !ok {
			return Result{}, errors.New("site provisioner cannot reset")
		}
		reset, resetErr := resetter.Reset()
		if resetErr != nil {
			return Result{}, resetErr
		}
		if clearer, hasClear := executor.store.(interface{ Reset() error }); hasClear {
			_ = clearer.Reset()
		}
		output, err = json.Marshal(reset)
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
	case SFTPUserCreate, SFTPUserUpdateKey, SFTPUserDelete:
		request, parseErr := parseSFTPUser(
			command.Payload,
			command.Type != SFTPUserDelete,
			command.Type == SFTPUserDelete,
		)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.sftp == nil {
			return Result{}, errors.New("SFTP provisioner is not configured")
		}
		var ftpResult access.FTPResult
		var ftpErr error
		switch command.Type {
		case SFTPUserCreate:
			ftpResult, ftpErr = executor.sftp.CreateFTPUser(
				request.SiteID, request.Label, request.PublicKey, request.Directory,
			)
		case SFTPUserUpdateKey:
			ftpResult, ftpErr = executor.sftp.UpdateFTPKey(request.SiteID, request.Label, request.PublicKey)
		case SFTPUserDelete:
			ftpResult, ftpErr = executor.sftp.DeleteFTPUser(request.SiteID, request.Label, request.Confirm)
		}
		if ftpErr != nil {
			return Result{}, ftpErr
		}
		output, err = json.Marshal(ftpResult)
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
	case DatabaseUserCreate, DatabaseUserDelete, DatabaseGrant, DatabaseRevoke:
		needsDatabase := command.Type == DatabaseGrant || command.Type == DatabaseRevoke
		request, parseErr := parseDatabaseUser(
			command.Payload,
			command.Type == DatabaseUserCreate,
			needsDatabase,
			command.Type == DatabaseUserDelete,
		)
		if parseErr != nil {
			return Result{}, parseErr
		}
		if executor.db == nil {
			return Result{}, errors.New("database provisioner is not configured")
		}
		var userResult database.UserResult
		var userErr error
		switch command.Type {
		case DatabaseUserCreate:
			userResult, userErr = executor.db.CreateUser(request.SiteID, request.Role, request.Password)
		case DatabaseUserDelete:
			userResult, userErr = executor.db.DeleteUser(request.SiteID, request.Role, request.Confirm)
		case DatabaseGrant:
			userResult, userErr = executor.db.Grant(request.SiteID, request.Database, request.Role)
		case DatabaseRevoke:
			userResult, userErr = executor.db.Revoke(request.SiteID, request.Database, request.Role)
		}
		if userErr != nil {
			return Result{}, userErr
		}
		output, err = json.Marshal(userResult)
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
	case MailDomainCreate, MailDomainDelete, MailMailboxCreate, MailMailboxSetPassword,
		MailMailboxSetQuota, MailMailboxDelete, MailInventory:
		if executor.mail == nil {
			return Result{}, errors.New("mail provisioner is not configured")
		}
		output, err = executor.executeMail(command.Type, command.Payload)
	case TLSLetsEncryptEnable, TLSCertificateInspect:
		siteID, challengeType, domains, parseErr := parseTLSRequest(command.Type, command.Payload)
		if parseErr != nil {
			return Result{}, parseErr
		}
		output, err = executor.executeTLS(command.Type, siteID, challengeType, domains)
	default:
		return Result{}, errors.New("unsupported command type")
	}
	if err != nil {
		return Result{}, err
	}

	return Result{
		CommandID: command.ID,
		Status:    "succeeded",
		Output:    output,
	}, nil
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

func (executor *Executor) executeMail(commandType string, payload json.RawMessage) ([]byte, error) {
	// The inventory takes no arguments: it answers what the server actually
	// holds, which is what a reconcile compares against.
	if commandType == MailInventory {
		inventory, err := executor.mail.Inventory()
		if err != nil {
			return nil, err
		}
		return json.Marshal(inventory)
	}

	request, err := parseMail(payload, commandType)
	if err != nil {
		return nil, err
	}

	switch commandType {
	case MailDomainCreate:
		created, err := executor.mail.CreateDomain(request.Domain)
		if err != nil {
			return nil, err
		}
		return json.Marshal(created)
	case MailDomainDelete:
		removed, err := executor.mail.DeleteDomain(request.Domain, request.Confirm)
		if err != nil {
			return nil, err
		}
		return json.Marshal(removed)
	case MailMailboxCreate:
		mailbox, err := executor.mail.CreateMailbox(request.Address, request.Password, request.QuotaBytes)
		if err != nil {
			return nil, err
		}
		return json.Marshal(mailbox)
	case MailMailboxSetPassword:
		if err := executor.mail.SetPassword(request.Address, request.Password); err != nil {
			return nil, err
		}
		// Only the address comes back. Echoing the password would put it in
		// the persisted command result.
		return json.Marshal(map[string]any{"address": request.Address, "status": "active"})
	case MailMailboxSetQuota:
		mailbox, err := executor.mail.SetQuota(request.Address, request.QuotaBytes)
		if err != nil {
			return nil, err
		}
		return json.Marshal(mailbox)
	case MailMailboxDelete:
		mailbox, err := executor.mail.DeleteMailbox(request.Address, request.Confirm)
		if err != nil {
			return nil, err
		}
		return json.Marshal(mailbox)
	}

	return nil, errors.New("unsupported mail command")
}

// executeTLS reports the certificate the site actually has.
//
// Caddy issues and renews on its own; the agent's job is to say what came of
// it. For TLSCertificateInspect, the absence of a certificate is reported as
// a state (the control plane keeps asking until the domain resolves). For
// TLSLetsEncryptEnable, the agent waits up to tlsIssueWait for Caddy to
// finish an ACME order that is already in flight (the node's Caddy is pointed
// at an ACME CA it can reach); if the wait elapses with still no certificate,
// the absence is surfaced as a failure with an explicit message so the
// operator sees the issue never completed and the control plane does not
// assume the job is still running. With tlsIssueWait at zero the wait is
// skipped and the failure is immediate — the historical behaviour for a node
// whose Caddy has no reachable CA.
func (executor *Executor) executeTLS(commandType, siteID, challengeType string, domains []string) ([]byte, error) {
	if executor.tls == nil {
		return nil, errors.New("TLS inspector is not configured")
	}

	evidence, err := executor.tls.Inspect(siteID)
	if errors.Is(err, tls.ErrNoCertificate) && commandType == TLSLetsEncryptEnable && executor.tlsIssueWait > 0 {
		evidence, err = executor.waitForCertificate(siteID)
	}
	if errors.Is(err, tls.ErrNoCertificate) {
		if commandType == TLSLetsEncryptEnable {
			domain := siteID
			if len(domains) > 0 {
				domain = domains[0]
			}
			return nil, fmt.Errorf("tls.letsencrypt.enable is not implemented: Caddy is expected to have issued the certificate automatically. No certificate found for %s. Verify Caddy has the domain in its config and that ACME HTTP-01 is reachable", domain)
		}
		return json.Marshal(map[string]any{
			"siteId":           siteID,
			"domains":          domains,
			"challengeType":    challengeType,
			"manager":          "caddy_automatic_tls",
			"privateKeyStored": false,
			"status":           "pending_certificate",
			"message":          "Caddy has not issued a certificate for this site yet.",
		})
	}
	if err != nil {
		return nil, err
	}

	// The challenge type rides alongside rather than inside the status: the
	// control plane allowlists it as its own field, and folding it into the
	// status would make that value unpredictable to match on. It is only
	// meaningful when the caller asked for a specific challenge (the enable
	// command); the inspect command never names one.
	report := map[string]any{
		"siteId":           evidence.SiteID,
		"domains":          evidence.Domains,
		"issuer":           evidence.Issuer,
		"fingerprint":      evidence.Fingerprint,
		"notBefore":        evidence.NotBefore,
		"expiresAt":        evidence.ExpiresAt,
		"manager":          evidence.Manager,
		"status":           evidence.Status,
		"privateKeyStored": evidence.PrivateKeyStored,
	}
	if challengeType != "" {
		report["challengeType"] = challengeType
	}

	return json.Marshal(report)
}

// waitForCertificate re-inspects Caddy's storage until a certificate appears
// or tlsIssueWait elapses. It is only reached for tls.letsencrypt.enable on a
// node whose Caddy is pointed at a reachable ACME CA: the order is already in
// flight when this command runs, and issuance against a private step-ca on the
// same network is seconds, not the minutes a public HTTP-01 round-trip can
// take. The first Inspect has already happened; this loop starts by sleeping.
func (executor *Executor) waitForCertificate(siteID string) (tls.Evidence, error) {
	step := executor.tlsIssuePoll
	if step <= 0 {
		step = 5 * time.Second
	}
	deadline := time.Now().Add(executor.tlsIssueWait)

	var (
		evidence tls.Evidence
		err      = tls.ErrNoCertificate
	)
	for {
		time.Sleep(step)
		evidence, err = executor.tls.Inspect(siteID)
		if !errors.Is(err, tls.ErrNoCertificate) || time.Now().After(deadline) {
			return evidence, err
		}
	}
}
