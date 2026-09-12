package command

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nubitio/nubit-agent/internal/backup"
	"github.com/nubitio/nubit-agent/internal/capacity"
)

type failingSaveStore struct{ *MemoryStore }

func (failingSaveStore) Save(string, Result) error { return errors.New("disk full") }

func admissionConfig() *capacity.Config {
	c := capacity.DefaultConfig()
	c.Limit = capacity.Resources{CPUmilli: 1000, MemoryBytes: 1024 * 1024 * 1024, PHPWorkers: 10, PIDs: 100}
	c.ScratchBytes = 1
	return &c
}

func TestAdmissionRollsBackWhenResultCannotBeSaved(t *testing.T) {
	store := failingSaveStore{NewMemoryStore()}
	executor := NewExecutorWithConfig(ExecutorConfig{Capacity: admissionConfig()}, store, fakeSiteProvisioner{})
	_, err := executor.Execute(Command{ID: "create-save-fails", Type: SiteCreate, Version: 1, IdempotencyKey: "create-save-fails", Payload: []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`)})
	if err == nil {
		t.Fatal("expected store failure")
	}
	if got := executor.CapacitySnapshot().Reservations; got != 1 {
		t.Fatalf("successful host mutation lost its reservation after Save failure: %d", got)
	}
}

type slowBackup struct{}

func (slowBackup) List(string) ([]backup.Archive, error) { return nil, nil }
func (slowBackup) Create(string, int) (backup.Archive, error) {
	time.Sleep(80 * time.Millisecond)
	return backup.Archive{}, nil
}
func (slowBackup) Restore(string, string, bool) error         { return nil }
func (slowBackup) Verify(string) (backup.VerifyResult, error) { return backup.VerifyResult{}, nil }

func TestTimedOutOperationRetainsAdmissionUntilProvisionerStops(t *testing.T) {
	c := admissionConfig()
	c.OperationConcurrency = map[string]int{"backup": 1}
	executor := NewExecutorWithConfig(ExecutorConfig{Capacity: c, TypeTimeouts: map[string]time.Duration{SiteBackupCreate: 10 * time.Millisecond}}, NewMemoryStore(), slowBackup{})
	_, err := executor.Execute(Command{ID: "backup-timeout", Type: SiteBackupCreate, Version: 1, IdempotencyKey: "backup-timeout", Payload: []byte(`{"siteId":"example.com","retentionDays":7}`)})
	if err == nil {
		t.Fatal("expected timeout")
	}
	if got := executor.CapacitySnapshot().Operations["backup"].InUse; got != 1 {
		t.Fatalf("timed-out operation was released early: %d", got)
	}
	time.Sleep(120 * time.Millisecond)
	if got := executor.CapacitySnapshot().Operations["backup"].InUse; got != 0 {
		t.Fatalf("operation admission was not released after completion: %d", got)
	}
	if _, err := executor.Execute(Command{ID: "backup-timeout-redelivery", Type: SiteBackupCreate, Version: 1, IdempotencyKey: "backup-timeout", Payload: []byte(`{"siteId":"example.com","retentionDays":7}`)}); err == nil {
		t.Fatal("a timed-out backup must not be redelivered")
	}
}

func TestLateTimeoutCompletionCannotRestoreStaleReservation(t *testing.T) {
	executor := NewExecutorWithConfig(ExecutorConfig{Capacity: admissionConfig(), TypeTimeouts: map[string]time.Duration{SiteCreate: 10 * time.Millisecond}}, NewMemoryStore(), slowSiteProvisioner{delay: 80 * time.Millisecond})
	_, _ = executor.Execute(Command{ID: "create-timeout", Type: SiteCreate, Version: 1, IdempotencyKey: "create-timeout", Payload: []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`)})
	_, err := executor.Execute(Command{ID: "resource-update", Type: SiteSetResources, Version: 1, IdempotencyKey: "resource-update", Payload: []byte(`{"siteId":"example.com","resources":{"workers":1,"memoryLimitMb":64}}`)})
	if !errors.Is(err, capacity.ErrSiteConflict) {
		t.Fatalf("expected cross-command site fence, got %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := executor.Execute(Command{ID: "resource-update-after-fence", Type: SiteSetResources, Version: 1, IdempotencyKey: "resource-update-after-fence", Payload: []byte(`{"siteId":"example.com","resources":{"workers":1,"memoryLimitMb":64}}`)}); err != nil {
		t.Fatal(err)
	}
	if got := executor.CapacitySnapshot().Reserved.PHPWorkers; got != 1 {
		t.Fatalf("unexpected reservation after fence release: %d", got)
	}
}

func TestRestoreRequiresScratchPreflight(t *testing.T) {
	c := admissionConfig()
	c.ScratchBytes = int64(^uint64(0) >> 1)
	executor := NewExecutorWithConfig(ExecutorConfig{Capacity: c}, NewMemoryStore(), slowBackup{})
	_, err := executor.Execute(Command{ID: "restore-no-scratch", Type: SiteBackupRestore, Version: 1, IdempotencyKey: "restore-no-scratch", Payload: []byte(`{"siteId":"example.com","name":"latest.tar.zst","confirmed":true}`)})
	if err == nil || !strings.Contains(err.Error(), "insufficient scratch") {
		t.Fatalf("expected restore scratch rejection, got %v", err)
	}
}
