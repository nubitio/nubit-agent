// Package selfupdate replaces the running agent binary with a newer tagged
// release.
//
// Integrity is established by verifying both SHA256SUMS and an independent
// Ed25519 signature from the release's detached .sig asset before anything
// touches disk. Release discovery uses the tagged-release list, never the
// mutable GitHub releases/latest route.
//
// The swap itself never interrupts work. Stage() prepares the replacement and
// arms a flag; the caller restarts only at a point it knows is idle.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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

// The private counterpart is held only in CI secret storage.
//
//go:embed release-signing-public-key.pem
var releaseSigningPublicKeyPEM []byte

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
	CurrentVersion   string
	Repository       string
	BinaryPath       string
	Interval         time.Duration
	APIBaseURL       string
	DownloadURL      string
	HTTPClient       *http.Client
	SigningPublicKey ed25519.PublicKey
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
	if len(config.SigningPublicKey) == 0 {
		block, _ := pem.Decode(releaseSigningPublicKeyPEM)
		if block == nil {
			return nil, errors.New("invalid embedded release signing public key")
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		publicKey, ok := key.(ed25519.PublicKey)
		if err != nil || !ok || len(publicKey) != ed25519.PublicKeySize {
			return nil, errors.New("invalid embedded release signing public key")
		}
		config.SigningPublicKey = publicKey
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

	signature, err := updater.signature(ctx, latest, asset)
	if err != nil {
		return "", err
	}
	if err := updater.replaceBinary(ctx, latest, asset, expected, signature); err != nil {
		return "", err
	}
	updater.restartPending.Store(true)

	return latest, nil
}

// StageRelease stages one exact release selected by the control plane. The
// caller supplies the digest from its audited release record; the detached
// signature is still fetched and verified with the embedded public key.
func (updater *Updater) StageRelease(ctx context.Context, tag, expected string) (string, error) {
	if !validReleaseTag(tag) {
		return "", fmt.Errorf("invalid release tag %q", tag)
	}
	if len(expected) != sha256.Size*2 {
		return "", errors.New("release checksum must be 64 hexadecimal characters")
	}
	for _, character := range expected {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return "", errors.New("release checksum must be hexadecimal")
		}
	}
	if updater.RestartPending() {
		return "", errors.New("an update is already staged")
	}

	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	signature, err := updater.signature(ctx, tag, asset)
	if err != nil {
		return "", err
	}
	if err := updater.replaceBinary(ctx, tag, asset, strings.ToLower(expected), signature); err != nil {
		return "", err
	}
	updater.restartPending.Store(true)

	return tag, nil
}

// AssetName is the release asset for a platform. Release, install script and
// updater all derive the name here so they cannot drift apart.
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("nubit-agent_%s_%s", goos, goarch)
}

func (updater *Updater) latestVersion(ctx context.Context) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases?per_page=100", strings.TrimRight(updater.config.APIBaseURL, "/"), updater.config.Repository)
	body, err := updater.get(ctx, endpoint)
	if err != nil {
		return "", err
	}
	defer body.Close()

	var releases []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&releases); err != nil {
		return "", fmt.Errorf("decode releases: %w", err)
	}
	best := ""
	for _, release := range releases {
		if !release.Draft && !release.Prerelease && validReleaseTag(release.TagName) && (best == "" || newer(best, release.TagName)) {
			best = release.TagName
		}
	}
	return best, nil
}

func validReleaseTag(tag string) bool {
	parts := strings.Split(tag, ".")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "v") {
		return false
	}
	for index, part := range parts {
		if index == 0 {
			part = strings.TrimPrefix(part, "v")
		}
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func (updater *Updater) signature(ctx context.Context, tag, asset string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/%s/releases/download/%s/%s.sig", strings.TrimRight(updater.config.DownloadURL, "/"), updater.config.Repository, tag, asset)
	body, err := updater.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	signature, err := io.ReadAll(io.LimitReader(body, ed25519.SignatureSize+1))
	if err != nil {
		return nil, fmt.Errorf("read signature for %s: %w", asset, err)
	}
	if len(signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid signature size for %s", asset)
	}
	return signature, nil
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

func (updater *Updater) replaceBinary(ctx context.Context, tag, asset, expected string, signature []byte) error {
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
	contents, err := os.ReadFile(stagedPath)
	if err != nil {
		return fmt.Errorf("read the staged binary: %w", err)
	}
	if !ed25519.Verify(updater.config.SigningPublicKey, contents, signature) {
		return fmt.Errorf("signature verification failed for %s", asset)
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
	if !validReleaseTag(candidate) || !validReleaseTag(current) {
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
