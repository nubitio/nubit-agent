package capacity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{Limit: Resources{CPUmilli: 100, MemoryBytes: 100, PHPWorkers: 10, PIDs: 10}, ScratchBytes: 1, OperationConcurrency: map[string]int{"backup": 1}}
}

func TestReservationIsAtomicAndReplaceable(t *testing.T) {
	m, err := New(testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := Resources{CPUmilli: 60, MemoryBytes: 60, PHPWorkers: 6, PIDs: 6}
	if err := m.ReserveSite("a", r); err != nil {
		t.Fatal(err)
	}
	if err := m.ReserveSite("b", r); err != ErrCapacityExceeded {
		t.Fatalf("expected capacity rejection, got %v", err)
	}
	if err := m.ReserveSite("a", Resources{CPUmilli: 20, MemoryBytes: 20, PHPWorkers: 2, PIDs: 2}); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot().Reserved.PHPWorkers; got != 2 {
		t.Fatalf("replacement was not atomic: %d", got)
	}
}

func TestOperationAdmissionHonorsContextAndReleases(t *testing.T) {
	m, err := New(testConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	release, err := m.AcquireOperation(context.Background(), "backup")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := m.AcquireOperation(ctx, "backup"); err == nil {
		t.Fatal("second operation should wait and be cancelled")
	}
	release()
	releaseAgain, err := m.AcquireOperation(context.Background(), "backup")
	if err != nil {
		t.Fatal(err)
	}
	releaseAgain()
}

func TestTenSiteEnvelopeRejectsTheEleventh(t *testing.T) {
	c := testConfig()
	c.Limit = Resources{CPUmilli: 1000, MemoryBytes: 1000, PHPWorkers: 100, PIDs: 100}
	m, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := Resources{CPUmilli: 100, MemoryBytes: 100, PHPWorkers: 10, PIDs: 10}
	for i := 0; i < 10; i++ {
		if err := m.ReserveSite(string(rune('a'+i)), r); err != nil {
			t.Fatalf("site %d rejected: %v", i+1, err)
		}
	}
	if err := m.ReserveSite("eleventh", r); !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("expected eleventh site rejection, got %v", err)
	}
}

func TestExistingOvercommitStartsDegraded(t *testing.T) {
	c := testConfig()
	m, err := New(c, []Reservation{{Name: "legacy", Resources: Resources{CPUmilli: 200, MemoryBytes: 200, PHPWorkers: 20, PIDs: 20}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := m.Snapshot()
	if !snapshot.Degraded || snapshot.Reservations != 1 {
		t.Fatalf("expected degraded inventory, got %#v", snapshot)
	}
}
