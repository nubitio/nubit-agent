package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nubitio/nubit-agent/internal/access"
	"github.com/nubitio/nubit-agent/internal/audit"
	"github.com/nubitio/nubit-agent/internal/backup"
	"github.com/nubitio/nubit-agent/internal/command"
	"github.com/nubitio/nubit-agent/internal/controlplane"
	"github.com/nubitio/nubit-agent/internal/cron"
	"github.com/nubitio/nubit-agent/internal/database"
	"github.com/nubitio/nubit-agent/internal/enrollment"
	"github.com/nubitio/nubit-agent/internal/files"
	"github.com/nubitio/nubit-agent/internal/inventory"
	"github.com/nubitio/nubit-agent/internal/logs"
	"github.com/nubitio/nubit-agent/internal/mail"
	"github.com/nubitio/nubit-agent/internal/objectstore"
	"github.com/nubitio/nubit-agent/internal/selfupdate"
	"github.com/nubitio/nubit-agent/internal/site"
	"github.com/nubitio/nubit-agent/internal/status"
	"github.com/nubitio/nubit-agent/internal/telemetry"
	"github.com/nubitio/nubit-agent/internal/tls"
	"github.com/nubitio/nubit-agent/internal/tui"
	"github.com/nubitio/nubit-agent/internal/version"
)

const (
	defaultPollInterval   = 15 * time.Second
	defaultRenewInterval  = 12 * time.Hour
	defaultRenewThreshold = 7 * 24 * time.Hour
	defaultConfigDir      = "/etc/nubit-agent"
	defaultStateDir       = "/var/lib/nubit-agent"
	defaultListenAddr     = "127.0.0.1:9090"
	expiringSoonWindow    = 7 * 24 * time.Hour
)

func main() {
	// Release builds are verified by asking the binary what it is, and operators
	// need the same answer when diagnosing a server.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Println(version.Version)
		return
	}

	// The `enroll` subcommand runs an out-of-band mTLS enrollment against
	// Nubit Control and exits. It does not start the polling loop, the HTTP
	// health endpoint or the self-update goroutine — those are reserved for
	// the long-running `nubit-agent` (no-args) form.
	if len(os.Args) > 1 && os.Args[1] == "enroll" {
		if err := runEnroll(os.Args[2:]); err != nil {
			log.Printf("enroll: %v", err)
			os.Exit(1)
		}
		return
	}

	// `nubit-agent tui` is the operator cockpit for this node: it reads the
	// running daemon's GET /status and its state files, and offers a small
	// set of confirmed local actions (enroll, reconcile, node reset, outbox
	// flush). It never opens a shell and never mutates Control.
	if len(os.Args) > 1 && os.Args[1] == "tui" {
		if err := runTUI(os.Args[2:]); err != nil {
			log.Printf("tui: %v", err)
			os.Exit(1)
		}
		return
	}

	address := os.Getenv("NUBIT_AGENT_LISTEN_ADDR")
	if address == "" {
		address = defaultListenAddr
	}
	stateDir := os.Getenv("NUBIT_AGENT_STATE_DIR")
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	// Cert validation at startup: a stale or untrusted cert means the agent
	// will be silently downgraded to token-only mode (or worse, fail to
	// connect). Operators expect loud failure so they fix the deployment.
	verifyStartCertificate()
	store, err := command.NewFileStore(filepath.Join(stateDir, "commands.json"))
	if err != nil {
		log.Fatalf("initialize command store: %v", err)
	}
	siteStore, err := site.NewFileStateStore(filepath.Join(stateDir, "sites.json"))
	if err != nil {
		log.Fatalf("initialize site state store: %v", err)
	}
	outbox, err := controlplane.NewFileOutbox(filepath.Join(stateDir, "outbox.json"))
	if err != nil {
		log.Fatalf("initialize result outbox: %v", err)
	}

	reporter := status.New(status.Snapshot{
		Version:    version.Version,
		ListenAddr: address,
		StateDir:   stateDir,
		ControlURL: os.Getenv("NUBIT_CONTROL_URL"),
		Transport:  status.TransportOffline,
	})
	reporter.SetDynamic(
		func() int { return len(outbox.List()) },
		func() int { return len(siteStore.List()) },
	)

	provisioner := site.Provisioner{Runner: site.OSRunner{}, Store: siteStore}
	sftp := access.Manager{Runner: site.OSRunner{}, Sites: siteStore, ConfigDir: "/etc/ssh/sshd_config.d", KeysDir: filepath.Join(stateDir, "authorized_keys")}
	databases := database.Manager{Runner: database.OSRunner{}, Sites: siteStore}
	fileManager := files.Manager{Sites: siteStore}
	cronManager := cron.Manager{Sites: siteStore, Dir: filepath.Join(stateDir, "cron")}
	logManager := logs.Manager{Sites: siteStore}
	// Off-host backups. A node with no NUBIT_BACKUP_S3_* configuration gets a
	// manager with no store, and backup commands are refused rather than run
	// against nothing.
	blobStore, err := objectstore.New(context.Background(), objectstore.ConfigFromEnv())
	if err != nil {
		log.Fatalf("initialize backup storage: %v", err)
	}
	backupManager := backup.Manager{Sites: siteStore, TempDir: filepath.Join(stateDir, "tmp")}
	if blobStore != nil {
		backupManager.Blobs = blobStore
	}
	// Caddy's storage location is overridable because a host that installed it
	// some other way puts it elsewhere.
	certificates := tls.Inspector{StorageDir: os.Getenv("NUBIT_CADDY_CERTIFICATE_DIR")}
	services := []any{
		provisioner, sftp, databases, fileManager, cronManager, logManager, backupManager, certificates,
	}
	if mailManager, ok := mailProvisioner(); ok {
		services = append(services, mailManager)
	}
	executor := command.NewExecutorWithConfig(command.ConfigFromEnv(), store, services...)
	auditLogger, err := audit.New(filepath.Join(stateDir, "audit.log"))
	if err != nil {
		log.Fatalf("initialize audit log: %v", err)
	}
	executor.SetAuditLogger(auditLogger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("nubit-agent %s starting", version.Version)

	shutdownTelemetry, err := telemetry.Start(ctx)
	if err != nil {
		log.Printf("nubit-agent: telemetry disabled: %v", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := shutdownTelemetry(flushCtx); shutdownErr != nil {
			log.Printf("nubit-agent: telemetry shutdown: %v", shutdownErr)
		}
	}()

	updater := startSelfUpdate(ctx)
	if client := startPolling(ctx, executor, outbox, updater, stop, reporter); client != nil {
		go publishInventory(ctx, client, provisioner, 5*time.Minute)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"status":"ok"}`)
	})
	// GET /status is the operator/TUI view of this process's session: poll
	// health, transport, job counters, outbox depth, local site count. It is
	// read-only and carries no secrets.
	mux.HandleFunc("GET /status", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(reporter.Snapshot())
	})

	server := &http.Server{
		Addr:    address,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("nubit-agent health endpoint listening on %s", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("health endpoint: %v", err)
	}
}

// startSelfUpdate begins the release-tracking loop unless it is switched off
// with NUBIT_AGENT_UPDATE=off. Source builds report version "dev" and never
// self-update, so local development is unaffected without setting anything.
func startSelfUpdate(ctx context.Context) *selfupdate.Updater {
	if strings.EqualFold(os.Getenv("NUBIT_AGENT_UPDATE"), "off") {
		log.Print("nubit-agent: self-update disabled by NUBIT_AGENT_UPDATE=off")
		return nil
	}
	if !version.IsRelease() {
		log.Print("nubit-agent: source build, self-update disabled")
		return nil
	}

	config := selfupdate.Config{CurrentVersion: version.Version}
	if repository := os.Getenv("NUBIT_AGENT_UPDATE_REPOSITORY"); repository != "" {
		config.Repository = repository
	}
	if raw := os.Getenv("NUBIT_AGENT_UPDATE_INTERVAL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			log.Fatalf("nubit-agent: invalid NUBIT_AGENT_UPDATE_INTERVAL %q", raw)
		}
		config.Interval = parsed
	}

	updater, err := selfupdate.New(config)
	if err != nil {
		log.Printf("nubit-agent: self-update disabled: %v", err)
		return nil
	}
	go updater.Run(ctx)

	return updater
}

// startPolling begins the agent-initiated poll loop against Nubit Control
// when both NUBIT_CONTROL_URL and NUBIT_AGENT_TOKEN are configured (see
// ServerTokenController on the control-plane side for issuing the token).
// Either left unset, the agent still runs — useful for local dev — it just
// never picks up ProvisioningJobs.
func startPolling(
	ctx context.Context,
	executor controlplane.Executor,
	outbox controlplane.Outbox,
	updater *selfupdate.Updater,
	stop context.CancelFunc,
	reporter *status.Reporter,
) *controlplane.Client {
	controlURL := os.Getenv("NUBIT_CONTROL_URL")
	token := os.Getenv("NUBIT_AGENT_TOKEN")
	if controlURL == "" {
		log.Print("nubit-agent: NUBIT_CONTROL_URL not set, control-plane polling disabled")
		reporter.MarkPolling(false, "")
		return nil
	}
	configDir := os.Getenv("NUBIT_AGENT_CONFIG_DIR")
	if configDir == "" {
		configDir = defaultConfigDir
	}
	stateDir := os.Getenv("NUBIT_AGENT_STATE_DIR")
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	manager := enrollment.Manager{
		Directory:      configDir,
		StateDirectory: stateDir,
		ControlURL:     controlURL,
		RootCAPath:     os.Getenv("NUBIT_STEPCA_ROOT_CERT_PATH"),
	}
	if enrollmentToken := os.Getenv("NUBIT_AGENT_ENROLLMENT_TOKEN"); enrollmentToken != "" && !manager.Enrolled() {
		if err := manager.Enroll(ctx, enrollmentToken); err != nil {
			log.Fatalf("nubit-agent: enrollment failed: %v", err)
		}
		log.Print("nubit-agent: enrollment completed")
	}

	interval := defaultPollInterval
	if raw := os.Getenv("NUBIT_AGENT_POLL_INTERVAL"); "" != raw {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			log.Fatalf("nubit-agent: invalid NUBIT_AGENT_POLL_INTERVAL %q: %v", raw, parseErr)
		}
		if parsed <= 0 {
			log.Fatalf("nubit-agent: NUBIT_AGENT_POLL_INTERVAL must be greater than zero")
		}
		interval = parsed
	}

	var client *controlplane.Client
	if manager.Enrolled() {
		tlsConfig, err := manager.TLSConfig()
		if err != nil {
			log.Fatalf("nubit-agent: load mTLS identity: %v", err)
		}
		client = controlplane.NewMTLSClient(controlURL, tlsConfig)
		reporter.SetTransport(status.TransportMTLS, true)
		if expiry, expiryErr := manager.CertificateExpiry(); expiryErr == nil {
			reporter.SetCertNotAfter(expiry)
		}
		renewInterval := getDurationEnv("NUBIT_AGENT_RENEW_CHECK_INTERVAL", defaultRenewInterval)
		renewThreshold := getDurationEnv("NUBIT_AGENT_RENEW_THRESHOLD", defaultRenewThreshold)
		go renewCertificate(ctx, manager, renewInterval, renewThreshold)
	} else {
		if token == "" {
			log.Print("nubit-agent: no certificate or NUBIT_AGENT_TOKEN configured, polling disabled")
			reporter.MarkPolling(false, "")
			return nil
		}
		client = controlplane.NewClient(controlURL, token)
		reporter.SetTransport(status.TransportToken, false)
	}
	reporter.MarkPolling(true, interval.String())
	options := []controlplane.PollOption{
		controlplane.WithStatusSink(func(err error, fetched, executed int) {
			reporter.RecordPoll(err, fetched, executed)
		}),
	}
	if updater != nil {
		// Exiting is how the update is applied: systemd restarts the service and
		// picks up the binary already swapped in on disk.
		options = append(options, controlplane.WithStopCheck(updater.RestartPending, func() {
			log.Print("nubit-agent: idle with an update staged, exiting for restart")
			reporter.SetSelfUpdatePending(true)
			stop()
		}))
	}
	go controlplane.Poll(ctx, client, executor, outbox, interval, options...)
	log.Printf("nubit-agent: polling %s every %s", controlURL, interval)
	return client
}

func renewCertificate(ctx context.Context, manager enrollment.Manager, interval, threshold time.Duration) {
	renew := func() {
		if manager.NeedsRenewal(time.Now().UTC(), threshold) {
			if err := manager.Renew(ctx); err != nil {
				log.Printf("nubit-agent: renew mTLS certificate failed: %v", err)
			}
		}
	}
	renew()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renew()
		}
	}
}

func publishInventory(ctx context.Context, client *controlplane.Client, provider inventory.RuntimeProvider, interval time.Duration) {
	publish := func() {
		snapshot, err := inventory.Collect(provider)
		if err != nil {
			log.Printf("nubit-agent: collect inventory failed: %v", err)
			return
		}
		if err := client.PublishInventory(ctx, snapshot); err != nil {
			log.Printf("nubit-agent: publish inventory failed: %v", err)
		}
	}
	publish()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
		}
	}
}

// mailProvisioner wires the Stalwart administration client when this node runs
// mail. A web-only node leaves the credentials unset, and the executor then
// refuses mail commands rather than pretending to have carried them out.
func mailProvisioner() (mail.Manager, bool) {
	secret := os.Getenv("NUBIT_MAIL_API_SECRET")
	if secret == "" {
		return mail.Manager{}, false
	}

	baseURL := os.Getenv("NUBIT_MAIL_BASE_URL")
	if baseURL == "" {
		// The mail server runs on this host. Its certificate is issued for the
		// public hostname, so verification is skipped for the loopback hop the
		// traffic never leaves.
		baseURL = "https://127.0.0.1"
	}
	username := os.Getenv("NUBIT_MAIL_API_USER")
	if username == "" {
		username = "nubit-agent"
	}

	return mail.Manager{JMAP: &mail.Client{
		BaseURL:     baseURL,
		Username:    username,
		Secret:      secret,
		InsecureTLS: strings.HasPrefix(baseURL, "https://127.0.0.1"),
	}}, true
}

// runEnroll implements the `nubit-agent enroll` subcommand. It parses the
// --token flag, runs Manager.Enroll against the configured Control URL and
// prints the issued certificate's expiry on success. The subcommand
// deliberately bypasses the long-running flag validation (no HTTP server,
// no polling loop) so operators can run enrollment interactively without
// disturbing a running agent — systemd can restart the service after.
func runEnroll(args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	token := flags.String("token", "", "one-time enrollment token issued by Nubit Control")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	expiry, err := enrollWithToken(ctx, *token)
	if err != nil {
		return err
	}
	fmt.Printf("Enrollment successful. Cert expires at: %s\n", expiry.UTC().Format(time.RFC3339))
	return nil
}

// enrollWithToken runs the mTLS enrollment against the configured Control URL
// and returns the issued certificate's expiry. It is shared by the `enroll`
// subcommand and the TUI's Actions panel so both persist identity the same
// way.
func enrollWithToken(ctx context.Context, token string) (time.Time, error) {
	if strings.TrimSpace(token) == "" {
		return time.Time{}, errors.New("--token is required")
	}
	controlURL := os.Getenv("NUBIT_CONTROL_URL")
	if controlURL == "" {
		return time.Time{}, errors.New("NUBIT_CONTROL_URL is required")
	}
	stateDir := os.Getenv("NUBIT_AGENT_STATE_DIR")
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	configDir := os.Getenv("NUBIT_AGENT_CONFIG_DIR")
	if configDir == "" {
		configDir = defaultConfigDir
	}
	manager := enrollment.Manager{
		Directory:      configDir,
		StateDirectory: stateDir,
		ControlURL:     controlURL,
		RootCAPath:     os.Getenv("NUBIT_STEPCA_ROOT_CERT_PATH"),
	}
	if err := manager.Enroll(ctx, token); err != nil {
		return time.Time{}, err
	}
	expiry, err := manager.CertificateExpiry()
	if err != nil {
		return time.Time{}, fmt.Errorf("enrollment succeeded but reading expiry failed: %w", err)
	}
	return expiry, nil
}

// runTUI implements the `nubit-agent tui` subcommand: a full-screen operator
// cockpit for this node. Like `enroll` it bypasses the daemon's HTTP server
// and polling loop — it is a separate short-lived process the operator runs
// over SSH. It talks to the running daemon only through its GET /status
// endpoint and the shared state files under the state directory.
func runTUI(args []string) error {
	flags := flag.NewFlagSet("tui", flag.ContinueOnError)
	addr := flags.String("addr", "", "daemon status address (default NUBIT_AGENT_LISTEN_ADDR or "+defaultListenAddr+")")
	stateDir := flags.String("state-dir", "", "agent state directory (default NUBIT_AGENT_STATE_DIR or "+defaultStateDir+")")
	refresh := flags.Duration("refresh", 2*time.Second, "status/state refresh interval")
	controlAdminURL := flags.String("control-admin-url", os.Getenv("NUBIT_CONTROL_URL"), "optional nubit-control base URL for the read-only Control panel")
	controlAdminToken := flags.String("control-admin-token", os.Getenv("NUBIT_CONTROL_ADMIN_TOKEN"), "bearer token for --control-admin-url")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *addr == "" {
		if *addr = os.Getenv("NUBIT_AGENT_LISTEN_ADDR"); *addr == "" {
			*addr = defaultListenAddr
		}
	}
	if *stateDir == "" {
		if *stateDir = os.Getenv("NUBIT_AGENT_STATE_DIR"); *stateDir == "" {
			*stateDir = defaultStateDir
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return tui.Run(ctx, tui.Options{
		StatusURL:         "http://" + *addr,
		StateDir:          *stateDir,
		ControlURL:        os.Getenv("NUBIT_CONTROL_URL"),
		Refresh:           *refresh,
		ControlAdminURL:   *controlAdminURL,
		ControlAdminToken: *controlAdminToken,
		Enroll:            enrollWithToken,
	})
}

// verifyStartCertificate enforces the on-disk mTLS material at startup. The
// checks are loud — they exit with a non-zero status — because silent
// failure here means the agent runs in a degraded state (token-only or no
// polling) that operators may not notice for days.
func verifyStartCertificate() {
	configDir := os.Getenv("NUBIT_AGENT_CONFIG_DIR")
	if configDir == "" {
		configDir = defaultConfigDir
	}
	certPath := getEnvPath("NUBIT_AGENT_CERT_PATH", filepath.Join(configDir, "agent-cert.pem"))
	caChainPath := getEnvPath("NUBIT_AGENT_CA_CHAIN_PATH", filepath.Join(configDir, "ca-cert.pem"))
	if _, err := os.Stat(certPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Fatalf("nubit-agent: stat certificate %s: %v", certPath, err)
	}
	if _, err := os.Stat(caChainPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Fatalf("nubit-agent: stat CA chain %s: %v", caChainPath, err)
	}
	cert, err := enrollment.Manager{Directory: configDir}.VerifyCertificate()
	if err != nil {
		log.Fatalf("nubit-agent: verify mTLS certificate: %v", err)
	}
	now := time.Now().UTC()
	remaining := time.Until(cert.NotAfter)
	switch {
	case cert.NotAfter.Before(now):
		log.Printf("nubit-agent: warning: mTLS certificate expired at %s", cert.NotAfter.UTC().Format(time.RFC3339))
	default:
		if remaining < expiringSoonWindow {
			log.Printf("nubit-agent: warning: mTLS certificate expires in %s (%s); renewal is overdue", remaining.Round(time.Second), cert.NotAfter.UTC().Format(time.RFC3339))
		}
	}
}

// getEnvPath returns the value of the named environment variable, or the
// supplied default when the variable is unset.
func getEnvPath(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// getDurationEnv parses the named environment variable as a Go duration
// string (e.g. "12h", "30m", "168h") and returns it. An unset variable
// yields the supplied default; a malformed value kills the process — the
// agent must not start with a polling interval that is invalid or zero.
func getDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		log.Fatalf("nubit-agent: invalid %s %q: %v", key, raw, err)
	}
	if parsed <= 0 {
		log.Fatalf("nubit-agent: %s must be greater than zero, got %s", key, raw)
	}
	return parsed
}
