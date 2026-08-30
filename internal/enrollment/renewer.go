package enrollment

import (
	"context"
	"log"
	"time"
)

// Renewer runs the certificate renewal loop. It owns the ticker and the
// context lifecycle, and delegates every real decision (when to renew, how
// to talk to Control, how to persist) to Manager. The loop is best-effort:
// a failed renewal is logged and retried on the next tick. The agent keeps
// polling Control the whole time, so a stale-but-still-valid cert keeps the
// agent functional while renewal is retried.
type Renewer struct {
	Manager  Manager
	Interval time.Duration
	// Before is how close to expiry the renewer waits before asking Control
	// for a fresh certificate. The default is 7 days, which gives one full
	// weekly tick of headroom even when the renewal endpoint is briefly
	// unreachable.
	Before time.Duration
}

// Run blocks until ctx is cancelled. It performs a renewal pass immediately
// (so an agent that restarted just past the 7-day window does not have to
// wait Interval) and then on every tick.
func (renewer Renewer) Run(ctx context.Context) {
	interval := renewer.Interval
	if interval <= 0 {
		interval = 12 * time.Hour
	}
	before := renewer.Before
	if before <= 0 {
		before = 7 * 24 * time.Hour
	}
	if !renewer.Manager.Enrolled() {
		return
	}
	renewer.pass(ctx, before)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewer.pass(ctx, before)
		}
	}
}

func (renewer Renewer) pass(ctx context.Context, before time.Duration) {
	if !renewer.Manager.NeedsRenewal(time.Now().UTC(), before) {
		return
	}
	if remaining, err := timeUntilExpiry(renewer.Manager); err == nil && remaining > 0 {
		log.Printf("nubit-agent: mTLS certificate expires in %s, renewing", remaining.Round(time.Second))
	}
	if err := renewer.Manager.Renew(ctx); err != nil {
		log.Printf("nubit-agent: mTLS certificate renewal failed: %v", err)
		return
	}
	if expiry, err := renewer.Manager.CertificateExpiry(); err == nil {
		log.Printf("nubit-agent: mTLS certificate renewed, next expiry %s", expiry.UTC().Format(time.RFC3339))
		return
	}
	log.Print("nubit-agent: mTLS certificate renewed")
}

func timeUntilExpiry(manager Manager) (time.Duration, error) {
	expiry, err := manager.CertificateExpiry()
	if err != nil {
		return 0, err
	}
	return time.Until(expiry), nil
}
