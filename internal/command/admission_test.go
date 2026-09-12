package command

import (
	"errors"
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
	if got := executor.CapacitySnapshot().Reservations; got != 0 {
		t.Fatalf("reservation leaked after Save failure: %d", got)
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
}
