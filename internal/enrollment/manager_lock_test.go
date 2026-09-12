package enrollment

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nubitio/nubit-agent/internal/durable"
)

func TestEnrollmentAndRenewalRespectWriterLock(t *testing.T) {
	stateDir := t.TempDir()
	lock, err := durable.Acquire(filepath.Join(stateDir, "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	manager := Manager{StateDirectory: stateDir, Directory: t.TempDir()}
	if err := manager.Enroll(context.Background(), "token"); !errors.Is(err, durable.ErrWriterBusy) {
		t.Fatalf("enrollment bypassed writer lock: %v", err)
	}
	if err := manager.Renew(context.Background()); !errors.Is(err, durable.ErrWriterBusy) {
		t.Fatalf("renewal bypassed writer lock: %v", err)
	}
}
