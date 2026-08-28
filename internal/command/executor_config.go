package command

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ExecutorConfig configures defence-in-depth limits on the executor: a
// per-command timeout (with overrides) and a per-type rate limit (with
// overrides and a small set of exempt, read-only types). Both are evaluated
// in memory only: neither is persisted across restarts. The defaults are
// deliberately generous so a freshly installed agent does not surprise
// operators; tight values belong in NUBIT_AGENT_* env vars per host.
//
// Zero or negative values disable the corresponding limit and are kept as
// the documented escape hatch for debugging.
type ExecutorConfig struct {
	// DefaultCommandTimeout is applied to every command type unless the type
	// has an override in TypeTimeouts. Zero or negative disables the timeout.
	DefaultCommandTimeout time.Duration
	// TypeTimeouts overrides the default timeout for a specific command
	// type, e.g. "site.backup.create" → 30m. Missing entries fall back to
	// DefaultCommandTimeout.
	TypeTimeouts map[string]time.Duration
	// DefaultRatePerMinute is the steady-state cap applied to every command
	// type unless the type has an override. Zero or negative disables the
	// rate limit.
	DefaultRatePerMinute float64
	// TypeRates overrides the per-minute cap for a specific command type.
	// Missing entries fall back to DefaultRatePerMinute.
	TypeRates map[string]float64
	// ExemptTypes lists command types that bypass the rate limit (and the
	// refill accounting that backs it). Read-only and reconciliation commands
	// belong here. Zero or negative DefaultRatePerMinute also exempts all
	// types by virtue of the rate limit being off.
	ExemptTypes map[string]bool
}

// ConfigFromEnv reads NUBIT_AGENT_* environment variables and returns a
// populated ExecutorConfig. Missing entries keep the documented defaults
// (5 minute timeout, 30 commands per minute per type, exempt: system.ping,
// system.reconcile, tls.certificate.inspect). The function never errors:
// invalid values are skipped and the default is used, on the principle that
// a misconfigured agent should keep serving what it can.
func ConfigFromEnv() ExecutorConfig {
	config := ExecutorConfig{
		DefaultCommandTimeout: 5 * time.Minute,
		DefaultRatePerMinute:  30,
		TypeTimeouts:          map[string]time.Duration{},
		TypeRates:             map[string]float64{},
		ExemptTypes: map[string]bool{
			SystemPing:            true,
			SystemReconcile:       true,
			TLSCertificateInspect: true,
		},
	}

	if raw := os.Getenv("NUBIT_AGENT_COMMAND_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			config.DefaultCommandTimeout = parsed
		}
	}

	for _, env := range os.Environ() {
		prefix := "NUBIT_AGENT_COMMAND_TIMEOUT_"
		if !strings.HasPrefix(env, prefix) {
			continue
		}
		name, value, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		commandType := strings.TrimPrefix(name, prefix)
		if commandType == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			continue
		}
		config.TypeTimeouts[commandType] = parsed
	}

	if raw := os.Getenv("NUBIT_AGENT_RATE_LIMIT_DEFAULT"); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			config.DefaultRatePerMinute = parsed
		}
	}

	for _, env := range os.Environ() {
		prefix := "NUBIT_AGENT_RATE_LIMIT_"
		if !strings.HasPrefix(env, prefix) {
			continue
		}
		name, value, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		commandType := strings.TrimPrefix(name, prefix)
		if commandType == "" || commandType == "DEFAULT" {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		config.TypeRates[commandType] = parsed
	}

	return config
}
