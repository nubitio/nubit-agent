package controlplane

import (
	"context"
	"log"
	"time"

	"github.com/nubitio/nubit-agent/internal/command"
)

// Executor is the subset of command.Executor the poller needs — narrowed so
// tests can supply a fake without a real Store/SiteProvisioner.
type Executor interface {
	Execute(cmd command.Command) (command.Result, error)
}

// Poll fetches and executes jobs immediately, then on every tick, until ctx
// is cancelled. A fetch or report failure is logged and retried next tick —
// it must never stop the loop, since Nubit Control being briefly unreachable
// is expected, not fatal.
func Poll(ctx context.Context, client *Client, executor Executor, outbox Outbox, interval time.Duration) {
	pollOnce(ctx, client, executor, outbox)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollOnce(ctx, client, executor, outbox)
		}
	}
}

func pollOnce(ctx context.Context, client *Client, executor Executor, outbox Outbox) {
	if !flushOutbox(ctx, client, outbox) {
		return
	}

	commands, err := client.FetchJobs(ctx)
	if err != nil {
		log.Printf("nubit-agent: fetch jobs failed: %v", err)
		return
	}

	for _, cmd := range commands {
		if !executeAndReport(ctx, client, executor, outbox, cmd) {
			return
		}
	}
}

func executeAndReport(ctx context.Context, client *Client, executor Executor, outbox Outbox, cmd command.Command) bool {
	result, execErr := executor.Execute(cmd)

	status := result.Status
	if execErr != nil {
		status = "failed"
	}

	pending := PendingResult{CommandID: cmd.ID, Status: status, Output: result.Output}
	if execErr != nil {
		pending.Error = execErr.Error()
	}
	if err := outbox.Put(pending); err != nil {
		log.Printf("nubit-agent: persist result for command %s failed: %v", cmd.ID, err)
		return false
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
