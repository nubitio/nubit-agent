// Package selfupdate replaces the running agent binary with a newer tagged
// release.
//
// Integrity is established by downloading SHA256SUMS from the same release and
// verifying the binary against it before anything touches disk. That defends
// against a truncated or corrupted download, not against a compromised release:
// the checksum file shares its trust root with the binary. Artifact signing is
// tracked in docs/roadmap.md.
//
// The swap itself never interrupts work. Stage() prepares the replacement and
// arms a flag; the caller restarts only at a point it knows is idle.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DefaultRepository is the public repository releases are published to.
	DefaultRepository = "nubitio/nubit-agent"
	// DefaultInterval is deliberately long: a hosting agent gains nothing from
	// chasing releases, and every check is an outbound request per server.
	DefaultInterval = 6 * time.Hour

	maxDownloadBytes = 128 << 20
)

// Config configures an Updater. Only CurrentVersion is required.
type Config struct {
	CurrentVersion string
	Repository     string
	BinaryPath     string
	Interval       time.Duration
	APIBaseURL     string
	DownloadURL    string
	HTTPClient     *http.Client
}

// Updater checks for newer releases and stages them for the next restart.
type Updater struct {
	config         Config
	restartPending atomic.Bool
}

// New returns an Updater with defaults applied for any unset field.
func New(config Config) (*Updater, error) {
	if config.Repository == "" {
		config.Repository = DefaultRepository
	}
	if config.Interval <= 0 {
		config.Interval = DefaultInterval
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	}
	if config.APIBaseURL == "" {
		config.APIBaseURL = "https://api.github.com"
	}
	if config.DownloadURL == "" {
		config.DownloadURL = "https://github.com"
	}
	if config.BinaryPath == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate the running binary: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(executable)
		if err != nil {
			return nil, fmt.Errorf("resolve the running binary: %w", err)
		}
		config.BinaryPath = resolved
	}

	return &Updater{config: config}, nil
}

// RestartPending reports whether a verified newer binary is already in place
// and the process only needs to exit for it to take effect.
func (updater *Updater) RestartPending() bool {
	return updater.restartPending.Load()
}

// Run checks on start and then on every interval until ctx is cancelled. A
// failed check is logged and retried on the next tick: an unreachable GitHub
// must never take the agent down.
func (updater *Updater) Run(ctx context.Context) {
	check := func() {
		if updater.RestartPending() {
			return
		}
		staged, err := updater.Stage(ctx)
		switch {
		case err != nil:
			log.Printf("nubit-agent: update check failed: %v", err)
		case staged != "":
			log.Printf("nubit-agent: staged update %s; restarting when idle", staged)
		}
	}

	check()
	ticker := time.NewTicker(updater.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

// Stage installs a newer release over the current binary and arms the restart
// flag. It returns the version staged, or an empty string when already current.
func (updater *Updater) Stage(ctx context.Context) (string, error) {
	latest, err := updater.latestVersion(ctx)
	if err != nil {
		return "", err
	}
	if !newer(updater.config.CurrentVersion, latest) {
		return "", nil
	}

	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	expected, err := updater.expectedChecksum(ctx, latest, asset)
	if err != nil {
		return "", err
	}

	if err := updater.replaceBinary(ctx, latest, asset, expected); err != nil {
		return "", err
	}
	updater.restartPending.Store(true)

	return latest, nil
}

// AssetName is the release asset for a platform. Release, install script and
// updater all derive the name here so they cannot drift apart.
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("nubit-agent_%s_%s", goos, goarch)
}

func (updater *Updater) latestVersion(ctx context.Context) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(updater.config.APIBaseURL, "/"), updater.config.Repository)
	body, err := updater.get(ctx, endpoint)
	if err != nil {
		return "", err
	}
	defer body.Close()

	var release struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("decode the latest release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return "", nil
	}
	if release.TagName == "" {
		return "", errors.New("the latest release has no tag")
	}

	return release.TagName, nil
}

func (updater *Updater) expectedChecksum(ctx context.Context, tag, asset string) (string, error) {
	endpoint := fmt.Sprintf("%s/%s/releases/download/%s/SHA256SUMS", strings.TrimRight(updater.config.DownloadURL, "/"), updater.config.Repository, tag)
	body, err := updater.get(ctx, endpoint)
	if err != nil {
		return "", err
	}
	defer body.Close()

	sums, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read SHA256SUMS: %w", err)
	}
	for line := range strings.SplitSeq(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), nil
		}
	}

	return "", fmt.Errorf("SHA256SUMS has no entry for %s", asset)
}

func (updater *Updater) replaceBinary(ctx context.Context, tag, asset, expected string) error {
	endpoint := fmt.Sprintf("%s/%s/releases/download/%s/%s", strings.TrimRight(updater.config.DownloadURL, "/"), updater.config.Repository, tag, asset)
	body, err := updater.get(ctx, endpoint)
	if err != nil {
		return err
	}
	defer body.Close()

	// Stage beside the target so the rename below stays on one filesystem and
	// is therefore atomic: readers see either the old binary or the new one.
	directory := filepath.Dir(updater.config.BinaryPath)
	staged, err := os.CreateTemp(directory, ".nubit-agent-update-*")
	if err != nil {
		return fmt.Errorf("create the staging file: %w", err)
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(staged, digest), io.LimitReader(body, maxDownloadBytes))
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	if written == 0 {
		return fmt.Errorf("%s downloaded empty", asset)
	}

	if actual := hex.EncodeToString(digest.Sum(nil)); actual != expected {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, expected, actual)
	}
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return fmt.Errorf("mark the staged binary executable: %w", err)
	}
	if err := os.Rename(stagedPath, updater.config.BinaryPath); err != nil {
		return fmt.Errorf("install the staged binary: %w", err)
	}

	return nil
}

func (updater *Updater) get(ctx context.Context, endpoint string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request for %s: %w", endpoint, err)
	}
	request.Header.Set("User-Agent", "nubit-agent")
	request.Header.Set("Accept", "application/octet-stream, application/json")

	response, err := updater.config.HTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", endpoint, err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()

		return nil, fmt.Errorf("request %s returned %s", endpoint, response.Status)
	}

	return response.Body, nil
}

// newer reports whether candidate is a strictly higher release than current.
// A non-release current version ("dev") never updates: there is no ordering
// between an untagged build and a release, so replacing it would be a guess.
func newer(current, candidate string) bool {
	if candidate == "" || current == "" || current == "dev" {
		return false
	}
	currentParts, ok := semver(current)
	if !ok {
		return false
	}
	candidateParts, ok := semver(candidate)
	if !ok {
		return false
	}
	for index := range currentParts {
		if candidateParts[index] != currentParts[index] {
			return candidateParts[index] > currentParts[index]
		}
	}

	return false
}

// semver parses vMAJOR.MINOR.PATCH, ignoring any pre-release or build suffix.
func semver(tag string) ([3]int, bool) {
	var parsed [3]int
	trimmed := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if index := strings.IndexAny(trimmed, "-+"); index >= 0 {
		trimmed = trimmed[:index]
	}
	fields := strings.Split(trimmed, ".")
	if len(fields) != 3 {
		return parsed, false
	}
	for index, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil || value < 0 {
			return parsed, false
		}
		parsed[index] = value
	}

	return parsed, true
}
