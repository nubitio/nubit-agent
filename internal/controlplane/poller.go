package controlplane

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/nubitio/nubit-agent/internal/command"
	"github.com/nubitio/nubit-agent/internal/telemetry"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Executor is the subset of command.Executor the poller needs — narrowed so
// tests can supply a fake without a real Store/SiteProvisioner.
type Executor interface {
	Execute(cmd command.Command) (command.Result, error)
}

// PollOption configures optional Poll behaviour.
type PollOption func(*pollSettings)

type pollSettings struct {
	shouldStop func() bool
	onStop     func()
}

// WithStopCheck asks the loop to stop between polls when shouldStop reports
// true, calling onStop before it returns. It is checked only while the loop is
// idle — never with a command in flight — so a staged self-update can restart
// the process without interrupting a provisioning action half-applied.
func WithStopCheck(shouldStop func() bool, onStop func()) PollOption {
	return func(settings *pollSettings) {
		settings.shouldStop = shouldStop
		settings.onStop = onStop
	}
}

// Poll fetches and executes jobs immediately, then on every tick, until ctx
// is cancelled. A fetch or report failure is logged and retried next tick —
// it must never stop the loop, since Nubit Control being briefly unreachable
// is expected, not fatal.
func Poll(ctx context.Context, client *Client, executor Executor, outbox Outbox, interval time.Duration, options ...PollOption) {
	settings := &pollSettings{}
	for _, option := range options {
		option(settings)
	}

	stopRequested := func() bool {
		if settings.shouldStop == nil || !settings.shouldStop() {
			return false
		}
		if settings.onStop != nil {
			settings.onStop()
		}

		return true
	}

	pollOnce(ctx, client, executor, outbox)
	if stopRequested() {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollOnce(ctx, client, executor, outbox)
			if stopRequested() {
				return
			}
		}
	}
}

func pollOnce(ctx context.Context, client *Client, executor Executor, outbox Outbox) {
	ctx, span := telemetry.Tracer().Start(ctx, "agent.poll")
	defer span.End()

	if !flushOutbox(ctx, client, outbox) {
		return
	}

	commands, err := client.FetchJobs(ctx)
	if err != nil {
		log.Printf("nubit-agent: fetch jobs failed: %v", err)
		telemetry.MarkError(span, err)
		return
	}
	if len(commands) > 0 {
		log.Printf("nubit-agent: fetched %d command(s)", len(commands))
	}
	span.SetAttributes(attribute.Int("nubit.agent.commands", len(commands)))
	telemetry.RecordPoll(ctx, len(commands))

	for _, cmd := range commands {
		if !executeAndReport(ctx, client, executor, outbox, cmd) {
			return
		}
	}
}

func executeAndReport(ctx context.Context, client *Client, executor Executor, outbox Outbox, cmd command.Command) bool {
	ctx, span := telemetry.Tracer().Start(ctx, "agent.command", trace.WithAttributes(
		attribute.String("nubit.command.type", cmd.Type),
		attribute.String("nubit.command.id", cmd.ID),
	))
	defer span.End()
	log.Printf("nubit-agent: executing %s id=%s", cmd.Type, cmd.ID)

	result, execErr := executor.Execute(cmd)
	if command.SystemReset == cmd.Type && execErr == nil {
		if clearer, ok := outbox.(interface{ Reset() error }); ok {
			_ = clearer.Reset()
		}
	}

	status := result.Status
	if execErr != nil {
		status = "failed"
		telemetry.MarkError(span, execErr)
	}
	span.SetAttributes(attribute.String("nubit.command.status", status))
	telemetry.RecordCommand(ctx, cmd.Type, status)

	pending := PendingResult{CommandID: cmd.ID, Status: status, Output: result.Output}
	if execErr != nil {
		pending.Error = execErr.Error()
	}
	if err := outbox.Put(pending); err != nil {
		switch {
		case errors.Is(err, ErrOutboxFull):
			// Eviction kept the outbox healthy, but this command's report is
			// gone. The next poll will regenerate the report because the
			// executor is idempotent on the command's idempotency key.
			log.Printf("nubit-agent: outbox full, dropped result for command %s; will be regenerated on next poll: %v", cmd.ID, err)
			return true
		case errors.Is(err, ErrOutboxCorrupt), errors.Is(err, ErrOutboxIO):
			log.Printf("nubit-agent: persist result for command %s failed: %v", cmd.ID, err)
			return false
		default:
			log.Printf("nubit-agent: persist result for command %s failed: %v", cmd.ID, err)
			return false
		}
	}
	return flushOutbox(ctx, client, outbox)
}

func flushOutbox(ctx context.Context, client *Client, outbox Outbox) bool {
	for _, pending := range outbox.List() {
		if err := client.ReportPending(ctx, pending); err != nil {
			log.Printf("nubit-agent: report result for command %s failed; kept in outbox: %v", pending.CommandID, err)
			return false
		}
		if err := outbox.Delete(pending.CommandID); err != nil {
			log.Printf("nubit-agent: delete reported result %s from outbox failed: %v", pending.CommandID, err)
			return false
		}
	}
	return true
}
