package main

import (
	"context"
	"errors"
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
	"github.com/nubitio/nubit-agent/internal/backup"
	"github.com/nubitio/nubit-agent/internal/command"
	"github.com/nubitio/nubit-agent/internal/controlplane"
	"github.com/nubitio/nubit-agent/internal/cron"
	"github.com/nubitio/nubit-agent/internal/database"
	"github.com/nubitio/nubit-agent/internal/enrollment"
	"github.com/nubitio/nubit-agent/internal/files"
	"github.com/nubitio/nubit-agent/internal/inventory"
	"github.com/nubitio/nubit-agent/internal/logs"
	"github.com/nubitio/nubit-agent/internal/selfupdate"
	"github.com/nubitio/nubit-agent/internal/site"
	"github.com/nubitio/nubit-agent/internal/version"
)

const defaultPollInterval = 15 * time.Second

func main() {
	// Release builds are verified by asking the binary what it is, and operators
	// need the same answer when diagnosing a server.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version" || os.Args[1] == "version") {
		fmt.Println(version.Version)
		return
	}

	address := os.Getenv("NUBIT_AGENT_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:9090"
	}
	stateDir := os.Getenv("NUBIT_AGENT_STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/nubit-agent"
	}
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

	provisioner := site.Provisioner{Runner: site.OSRunner{}, Store: siteStore}
	sftp := access.Manager{Runner: site.OSRunner{}, Sites: siteStore, ConfigDir: "/etc/ssh/sshd_config.d", KeysDir: filepath.Join(stateDir, "authorized_keys")}
	databases := database.Manager{Runner: database.OSRunner{}, Sites: siteStore}
	fileManager := files.Manager{Sites: siteStore}
	cronManager := cron.Manager{Sites: siteStore, Dir: filepath.Join(stateDir, "cron")}
	logManager := logs.Manager{Sites: siteStore}
	backupManager := backup.Manager{Sites: siteStore, Dir: filepath.Join(stateDir, "backups")}
	executor := command.NewExecutor(store, provisioner, sftp, databases, fileManager, cronManager, logManager, backupManager)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("nubit-agent %s starting", version.Version)

	updater := startSelfUpdate(ctx)
	if client := startPolling(ctx, executor, outbox, updater, stop); client != nil {
		go publishInventory(ctx, client, provisioner, 5*time.Minute)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"status":"ok"}`)
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
) *controlplane.Client {
	controlURL := os.Getenv("NUBIT_CONTROL_URL")
	token := os.Getenv("NUBIT_AGENT_TOKEN")
	if controlURL == "" {
		log.Print("nubit-agent: NUBIT_CONTROL_URL not set, control-plane polling disabled")
		return nil
	}
	configDir := os.Getenv("NUBIT_AGENT_CONFIG_DIR")
	if configDir == "" {
		configDir = "/etc/nubit-agent"
	}
	manager := enrollment.Manager{Directory: configDir, ControlURL: controlURL}
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
		go renewCertificate(ctx, manager, 12*time.Hour)
	} else {
		if token == "" {
			log.Print("nubit-agent: no certificate or NUBIT_AGENT_TOKEN configured, polling disabled")
			return nil
		}
		client = controlplane.NewClient(controlURL, token)
	}
	options := []controlplane.PollOption{}
	if updater != nil {
		// Exiting is how the update is applied: systemd restarts the service and
		// picks up the binary already swapped in on disk.
		options = append(options, controlplane.WithStopCheck(updater.RestartPending, func() {
			log.Print("nubit-agent: idle with an update staged, exiting for restart")
			stop()
		}))
	}
	go controlplane.Poll(ctx, client, executor, outbox, interval, options...)
	log.Printf("nubit-agent: polling %s every %s", controlURL, interval)
	return client
}

func renewCertificate(ctx context.Context, manager enrollment.Manager, interval time.Duration) {
	renew := func() {
		if manager.NeedsRenewal(time.Now().UTC(), 7*24*time.Hour) {
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
