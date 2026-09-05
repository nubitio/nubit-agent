package status

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSnapshotFillsDerivedFields(t *testing.T) {
	base := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	now := base
	r := New(Snapshot{Version: "dev", ListenAddr: "127.0.0.1:9090", Transport: TransportToken, StartedAt: base}).
		WithClock(func() time.Time { return now })
	r.SetDynamic(func() int { return 3 }, func() int { return 7 })

	now = base.Add(90 * time.Second)
	snap := r.Snapshot()

	if snap.UptimeSeconds != 90 {
		t.Fatalf("uptime = %d, want 90", snap.UptimeSeconds)
	}
	if snap.OutboxDepth != 3 || snap.SiteCount != 7 {
		t.Fatalf("dynamic fields not read: %+v", snap)
	}
	if snap.Version != "dev" || snap.Transport != TransportToken {
		t.Fatalf("static fields lost: %+v", snap)
	}
}

func TestRecordPollTracksOutcomes(t *testing.T) {
	r := New(Snapshot{})
	r.RecordPoll(nil, 2, 1)
	r.RecordPoll(errors.New("connection refused"), 0, 0)
	r.RecordPoll(nil, 1, 1)

	snap := r.Snapshot()
	if snap.PollsOK != 2 || snap.PollsFailed != 1 {
		t.Fatalf("poll counts = ok %d failed %d", snap.PollsOK, snap.PollsFailed)
	}
	if snap.JobsFetched != 3 || snap.JobsExecuted != 2 {
		t.Fatalf("job counts = fetched %d executed %d", snap.JobsFetched, snap.JobsExecuted)
	}
	if !snap.LastPollOK {
		t.Fatal("last poll should be the successful one")
	}
	if snap.LastPollError != "" {
		t.Fatalf("last poll error not cleared after success: %q", snap.LastPollError)
	}
	if snap.LastPollAt == nil {
		t.Fatal("last poll timestamp not set")
	}
}

func TestReporterIsConcurrencySafe(t *testing.T) {
	r := New(Snapshot{})
	r.SetDynamic(func() int { return 0 }, func() int { return 0 })
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.RecordPoll(nil, 1, 1) }()
		go func() { defer wg.Done(); _ = r.Snapshot() }()
	}
	wg.Wait()
	if got := r.Snapshot().PollsOK; got != 50 {
		t.Fatalf("PollsOK = %d, want 50", got)
	}
}
