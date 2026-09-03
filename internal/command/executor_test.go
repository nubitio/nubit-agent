package command

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nubitio/nubit-agent/internal/audit"
	"github.com/nubitio/nubit-agent/internal/files"
	"github.com/nubitio/nubit-agent/internal/site"
)

type fakeSiteProvisioner struct{}

func (fakeSiteProvisioner) Create(domain, user, phpVersion string, resources site.Resources) (site.CreateResult, error) {
	return site.CreateResult{SiteID: domain, DocumentRoot: "/srv/" + domain + "/public", PHPSocket: "/run/php/" + user + ".sock", CaddyConfigHash: "sha256:caddy", PHPConfigHash: "sha256:php"}, nil
}

func (fakeSiteProvisioner) Inspect(siteID string) (site.State, error) {
	return site.State{SiteID: siteID, PHPVersion: "8.4"}, nil
}

func (fakeSiteProvisioner) SetResources(siteID string, resources site.Resources) (site.ResourcesResult, error) {
	return site.ResourcesResult{SiteID: siteID, Previous: site.DefaultResources(), Resources: resources}, nil
}

func (fakeSiteProvisioner) SetPHPVersion(siteID, phpVersion string) (site.PHPVersionResult, error) {
	return site.PHPVersionResult{SiteID: siteID, PreviousVersion: "8.4", PHPVersion: phpVersion}, nil
}

func (fakeSiteProvisioner) Suspend(siteID string) (site.LifecycleResult, error) {
	return site.LifecycleResult{SiteID: siteID, Status: "suspended"}, nil
}

func (fakeSiteProvisioner) Resume(siteID string) (site.LifecycleResult, error) {
	return site.LifecycleResult{SiteID: siteID, Status: "active"}, nil
}

func (fakeSiteProvisioner) AddDomain(siteID, domain string) (site.DomainResult, error) {
	return site.DomainResult{SiteID: siteID, Domains: []string{siteID, domain}}, nil
}

func (fakeSiteProvisioner) RemoveDomain(siteID, domain string) (site.DomainResult, error) {
	return site.DomainResult{SiteID: siteID, Domains: []string{siteID}}, nil
}

func (fakeSiteProvisioner) Delete(siteID string, confirmed bool) (site.DeleteResult, error) {
	return site.DeleteResult{SiteID: siteID, Status: "deleted", RecoveryDir: "/recovery/site"}, nil
}

func (fakeSiteProvisioner) RuntimeInventory() ([]site.RuntimeInfo, error) {
	return []site.RuntimeInfo{{Version: "8.4", Installed: true, SiteCount: 1}}, nil
}

func (fakeSiteProvisioner) RemoveRuntime(phpVersion string, confirmed bool) (site.RemoveRuntimeResult, error) {
	return site.RemoveRuntimeResult{Version: phpVersion, Removed: true}, nil
}

func (fakeSiteProvisioner) Reconcile() ([]site.Drift, error) {
	return []site.Drift{{SiteID: "example.com", Resource: "phpConfig", Expected: "present", Actual: "missing"}}, nil
}

func (fakeSiteProvisioner) Reset() (site.ResetResult, error) {
	return site.ResetResult{Deleted: []string{"example.com"}}, nil
}

type fakeFilesProvisioner struct{}

func (fakeFilesProvisioner) List(siteID, rel string) (files.ListResult, error) {
	return files.ListResult{Path: rel, Entries: []files.Entry{{Name: "index.html", Type: "file", Size: 4}}}, nil
}

func (fakeFilesProvisioner) Mkdir(string, string) error         { return nil }
func (fakeFilesProvisioner) Write(string, string, []byte) error { return nil }
func (fakeFilesProvisioner) Read(string, string) (files.ReadResult, error) {
	return files.ReadResult{Name: "index.html", Size: 4, Content: []byte("hola")}, nil
}
func (fakeFilesProvisioner) Delete(string, string) error         { return nil }
func (fakeFilesProvisioner) Rename(string, string, string) error { return nil }
func (fakeFilesProvisioner) Unzip(string, string) error          { return nil }
func (fakeFilesProvisioner) Usage(string) (files.UsageResult, error) {
	return files.UsageResult{Bytes: 4, Files: 1}, nil
}

func TestExecutorListsSiteFiles(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeFilesProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_files", Type: SiteFilesList, Version: 1, IdempotencyKey: "files:list", Payload: []byte(`{"siteId":"example.com","path":""}`)})
	if err != nil {
		t.Fatal(err)
	}
	var listed files.ListResult
	if err := json.Unmarshal(result.Output, &listed); err != nil {
		t.Fatal(err)
	}
	if 1 != len(listed.Entries) || "index.html" != listed.Entries[0].Name {
		t.Fatalf("unexpected list: %#v", listed)
	}
}

func TestExecutorResetsEverySite(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_reset", Type: SystemReset, Version: 1, IdempotencyKey: "system:reset", Payload: []byte(`{"confirm":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	var output site.ResetResult
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if 1 != len(output.Deleted) || "example.com" != output.Deleted[0] {
		t.Fatalf("unexpected reset: %#v", output)
	}
}

func TestExecutorCreatesSiteThroughProvisioner(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_site", Type: SiteCreate, Version: 1, IdempotencyKey: "site:create", Payload: []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]string
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output["siteId"] != "example.com" || output["phpSocket"] == "" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestExecutorInspectsSite(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_inspect", Type: SiteInspect, Version: 1, IdempotencyKey: "site:inspect", Payload: []byte(`{"siteId":"example.com"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var state site.State
	if err := json.Unmarshal(result.Output, &state); err != nil {
		t.Fatal(err)
	}
	if state.PHPVersion != "8.4" {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestExecutorChangesPHPVersion(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_php", Type: RuntimeSetVersion, Version: 1, IdempotencyKey: "site:php", Payload: []byte(`{"siteId":"example.com","phpVersion":"8.5"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var changed site.PHPVersionResult
	if err := json.Unmarshal(result.Output, &changed); err != nil {
		t.Fatal(err)
	}
	if changed.PreviousVersion != "8.4" || changed.PHPVersion != "8.5" {
		t.Fatalf("unexpected change: %#v", changed)
	}
}

func TestExecutorRequiresConfirmationToRemoveRuntime(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	_, err := executor.Execute(Command{ID: "cmd_remove", Type: RuntimeRemove, Version: 1, IdempotencyKey: "php:remove", Payload: []byte(`{"phpVersion":"8.3","confirm":false}`)})
	if err == nil {
		t.Fatal("expected explicit confirmation to be required")
	}
}

func TestExecutorReportsRuntimeInventory(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_runtimes", Type: RuntimeInspect, Version: 1, IdempotencyKey: "php:inspect", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Runtimes []site.RuntimeInfo `json:"runtimes"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Runtimes) != 1 || output.Runtimes[0].Version != "8.4" {
		t.Fatalf("unexpected inventory: %#v", output.Runtimes)
	}
}

func TestExecutorReturnsStoredResultForDuplicateIdempotencyKey(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	first, err := executor.Execute(Command{
		ID:             "cmd_1",
		Type:           SystemPing,
		Version:        1,
		IdempotencyKey: "service_1:ping",
		Payload:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("first execution failed: %v", err)
	}

	second, err := executor.Execute(Command{
		ID:             "cmd_2",
		Type:           SystemPing,
		Version:        1,
		IdempotencyKey: "service_1:ping",
		Payload:        []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("duplicate execution failed: %v", err)
	}
	if second.CommandID != first.CommandID {
		t.Fatalf("expected stored command id %q, got %q", first.CommandID, second.CommandID)
	}
}

func TestExecutorRejectsUnknownCommand(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	_, err := executor.Execute(Command{
		ID:             "cmd_1",
		Type:           "shell.execute",
		Version:        1,
		IdempotencyKey: "service_1:unsafe",
		Payload:        []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected unsupported command to fail")
	}
}

func TestExecutorSerializesDuplicateCommands(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	command := Command{
		ID:             "cmd_1",
		Type:           SystemPing,
		Version:        1,
		IdempotencyKey: "service_1:concurrent-ping",
		Payload:        []byte(`{}`),
	}

	var results [2]Result
	var errors [2]error
	var group sync.WaitGroup
	group.Add(2)
	for index := range results {
		go func(index int) {
			defer group.Done()
			results[index], errors[index] = executor.Execute(command)
		}(index)
	}
	group.Wait()

	for _, err := range errors {
		if err != nil {
			t.Fatalf("concurrent execution failed: %v", err)
		}
	}
	if results[0].CommandID != results[1].CommandID {
		t.Fatalf("expected the stored result, got %q and %q", results[0].CommandID, results[1].CommandID)
	}
}

// A control plane that says nothing about limits must still get a site, on the
// tier every site had before plans could set them.
func TestSiteCreateWithoutResourcesUsesTheSharedTier(t *testing.T) {
	request, err := parseSiteCreate([]byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Resources != site.DefaultResources() {
		t.Fatalf("an absent plan did not fall back: %#v", request.Resources)
	}
}

func TestSiteResourcesAreBoundedAtTheCommandBoundary(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	_, err := executor.Execute(Command{
		ID: "cmd_resources", Type: SiteSetResources, Version: 1, IdempotencyKey: "site:resources",
		Payload: []byte(`{"siteId":"example.com","resources":{"workers":9000,"memoryLimitMb":128}}`),
	})
	if err == nil {
		t.Fatal("a worker count far outside the bounds reached the provisioner")
	}

	result, err := executor.Execute(Command{
		ID: "cmd_resources_ok", Type: SiteSetResources, Version: 1, IdempotencyKey: "site:resources:ok",
		Payload: []byte(`{"siteId":"example.com","resources":{"workers":20,"memoryLimitMb":512}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"workers":20`) {
		t.Fatalf("the applied limits were not reported: %s", result.Output)
	}
}

// ----- Timeout fixtures and tests ----------------------------------------

// slowSiteProvisioner sleeps before returning so the executor's timeout has
// something to interrupt. The sleep uses a select on the context passed in
// when one is supplied, but the existing SiteProvisioner interface has no
// context, so the sleep is unconditional — the executor's timeout only
// governs the dispatcher's bookkeeping, not the underlying work. That is
// the documented limitation: this fixture models the worst case the
// executor protects against.
type slowSiteProvisioner struct {
	delay time.Duration
}

func (slow slowSiteProvisioner) Create(domain, user, phpVersion string, resources site.Resources) (site.CreateResult, error) {
	time.Sleep(slow.delay)
	return site.CreateResult{SiteID: domain}, nil
}

func (slow slowSiteProvisioner) Inspect(siteID string) (site.State, error) {
	time.Sleep(slow.delay)
	return site.State{SiteID: siteID, PHPVersion: "8.4"}, nil
}

func (slow slowSiteProvisioner) SetResources(siteID string, resources site.Resources) (site.ResourcesResult, error) {
	time.Sleep(slow.delay)
	return site.ResourcesResult{SiteID: siteID, Resources: resources}, nil
}

func (slow slowSiteProvisioner) SetPHPVersion(siteID, phpVersion string) (site.PHPVersionResult, error) {
	time.Sleep(slow.delay)
	return site.PHPVersionResult{SiteID: siteID, PHPVersion: phpVersion}, nil
}

func (slow slowSiteProvisioner) Suspend(siteID string) (site.LifecycleResult, error) {
	time.Sleep(slow.delay)
	return site.LifecycleResult{SiteID: siteID, Status: "suspended"}, nil
}

func (slow slowSiteProvisioner) Resume(siteID string) (site.LifecycleResult, error) {
	time.Sleep(slow.delay)
	return site.LifecycleResult{SiteID: siteID, Status: "active"}, nil
}

func (slow slowSiteProvisioner) AddDomain(siteID, domain string) (site.DomainResult, error) {
	time.Sleep(slow.delay)
	return site.DomainResult{SiteID: siteID, Domains: []string{siteID, domain}}, nil
}

func (slow slowSiteProvisioner) RemoveDomain(siteID, domain string) (site.DomainResult, error) {
	time.Sleep(slow.delay)
	return site.DomainResult{SiteID: siteID, Domains: []string{siteID}}, nil
}

func (slow slowSiteProvisioner) Delete(siteID string, confirmed bool) (site.DeleteResult, error) {
	time.Sleep(slow.delay)
	return site.DeleteResult{SiteID: siteID, Status: "deleted"}, nil
}

func (slow slowSiteProvisioner) RuntimeInventory() ([]site.RuntimeInfo, error) {
	time.Sleep(slow.delay)
	return nil, nil
}

func (slow slowSiteProvisioner) RemoveRuntime(phpVersion string, confirmed bool) (site.RemoveRuntimeResult, error) {
	time.Sleep(slow.delay)
	return site.RemoveRuntimeResult{Version: phpVersion, Removed: true}, nil
}

func (slow slowSiteProvisioner) Reconcile() ([]site.Drift, error) {
	time.Sleep(slow.delay)
	return nil, nil
}

// ensure context import is used (the fixture is internal to these tests
// and uses a select on ctx in helpers; declared here to keep the import).
var _ = context.Background

func TestExecutorTimesOutSlowCommand(t *testing.T) {
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultCommandTimeout: 100 * time.Millisecond},
		NewMemoryStore(),
		slowSiteProvisioner{delay: 2 * time.Second},
	)
	_, err := executor.Execute(Command{
		ID: "cmd_slow", Type: SiteInspect, Version: 1, IdempotencyKey: "site:inspect:slow",
		Payload: []byte(`{"siteId":"example.com"}`),
	})
	if err == nil {
		t.Fatal("a slow command should have timed out")
	}
	if !strings.Contains(err.Error(), "exceeded timeout") {
		t.Fatalf("timeout error did not mention the timeout: %v", err)
	}
}

func TestExecutorHonorsCommandTypeOverride(t *testing.T) {
	executor := NewExecutorWithConfig(
		ExecutorConfig{
			DefaultCommandTimeout: 10 * time.Second,
			TypeTimeouts:          map[string]time.Duration{SiteInspect: 50 * time.Millisecond},
		},
		NewMemoryStore(),
		slowSiteProvisioner{delay: 2 * time.Second},
	)
	_, err := executor.Execute(Command{
		ID: "cmd_override", Type: SiteInspect, Version: 1, IdempotencyKey: "site:inspect:override",
		Payload: []byte(`{"siteId":"example.com"}`),
	})
	if err == nil {
		t.Fatal("the per-type override should have fired")
	}
	if !strings.Contains(err.Error(), "exceeded timeout") {
		t.Fatalf("override did not produce a timeout: %v", err)
	}
}

func TestExecutorDoesNotTimeoutFastCommand(t *testing.T) {
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultCommandTimeout: 1 * time.Second},
		NewMemoryStore(),
		slowSiteProvisioner{delay: 10 * time.Millisecond},
	)
	result, err := executor.Execute(Command{
		ID: "cmd_fast", Type: SiteInspect, Version: 1, IdempotencyKey: "site:inspect:fast",
		Payload: []byte(`{"siteId":"example.com"}`),
	})
	if err != nil {
		t.Fatalf("a fast command should not have timed out: %v", err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("unexpected status: %q", result.Status)
	}
}

func TestExecutorWithZeroTimeoutDoesNotTimeout(t *testing.T) {
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultCommandTimeout: 0},
		NewMemoryStore(),
		slowSiteProvisioner{delay: 50 * time.Millisecond},
	)
	if _, err := executor.Execute(Command{
		ID: "cmd_zero", Type: SiteInspect, Version: 1, IdempotencyKey: "site:inspect:zero",
		Payload: []byte(`{"siteId":"example.com"}`),
	}); err != nil {
		t.Fatalf("a zero timeout should disable the guard: %v", err)
	}
}

func TestExecutorTimeoutPersistsFailureToCommandStore(t *testing.T) {
	store := NewMemoryStore()
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultCommandTimeout: 50 * time.Millisecond},
		store,
		slowSiteProvisioner{delay: 1 * time.Second},
	)
	_, err := executor.Execute(Command{
		ID: "cmd_persist", Type: SiteInspect, Version: 1, IdempotencyKey: "site:inspect:persist",
		Payload: []byte(`{"siteId":"example.com"}`),
	})
	if err == nil {
		t.Fatal("a slow command should have timed out")
	}
	saved, found := store.Get("site:inspect:persist")
	if !found {
		t.Fatal("the timeout failure was not persisted to the command store")
	}
	if saved.Status != "failed" {
		t.Fatalf("the persisted status is not 'failed': %q", saved.Status)
	}
}

// ----- Rate limit fixtures and tests -------------------------------------

// counterProvisioner counts how many calls the executor makes; it never
// sleeps, so the rate limit alone is the only thing that gates calls.
type counterProvisioner struct {
	mu    sync.Mutex
	calls int
}

func (counter *counterProvisioner) Create(domain, user, phpVersion string, resources site.Resources) (site.CreateResult, error) {
	counter.record()
	return site.CreateResult{SiteID: domain}, nil
}

func (counter *counterProvisioner) Inspect(siteID string) (site.State, error) {
	counter.record()
	return site.State{SiteID: siteID, PHPVersion: "8.4"}, nil
}

func (counter *counterProvisioner) SetResources(siteID string, resources site.Resources) (site.ResourcesResult, error) {
	counter.record()
	return site.ResourcesResult{SiteID: siteID, Resources: resources}, nil
}

func (counter *counterProvisioner) SetPHPVersion(siteID, phpVersion string) (site.PHPVersionResult, error) {
	counter.record()
	return site.PHPVersionResult{SiteID: siteID, PHPVersion: phpVersion}, nil
}

func (counter *counterProvisioner) Suspend(siteID string) (site.LifecycleResult, error) {
	counter.record()
	return site.LifecycleResult{SiteID: siteID, Status: "suspended"}, nil
}

func (counter *counterProvisioner) Resume(siteID string) (site.LifecycleResult, error) {
	counter.record()
	return site.LifecycleResult{SiteID: siteID, Status: "active"}, nil
}

func (counter *counterProvisioner) AddDomain(siteID, domain string) (site.DomainResult, error) {
	counter.record()
	return site.DomainResult{SiteID: siteID, Domains: []string{siteID, domain}}, nil
}

func (counter *counterProvisioner) RemoveDomain(siteID, domain string) (site.DomainResult, error) {
	counter.record()
	return site.DomainResult{SiteID: siteID, Domains: []string{siteID}}, nil
}

func (counter *counterProvisioner) Delete(siteID string, confirmed bool) (site.DeleteResult, error) {
	counter.record()
	return site.DeleteResult{SiteID: siteID, Status: "deleted"}, nil
}

func (counter *counterProvisioner) RuntimeInventory() ([]site.RuntimeInfo, error) {
	counter.record()
	return nil, nil
}

func (counter *counterProvisioner) RemoveRuntime(phpVersion string, confirmed bool) (site.RemoveRuntimeResult, error) {
	counter.record()
	return site.RemoveRuntimeResult{Version: phpVersion, Removed: true}, nil
}

func (counter *counterProvisioner) Reconcile() ([]site.Drift, error) {
	counter.record()
	return nil, nil
}

func (counter *counterProvisioner) record() {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	counter.calls++
}

func (counter *counterProvisioner) Calls() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.calls
}

func TestExecutorRateLimitsExcessiveCommands(t *testing.T) {
	counter := &counterProvisioner{}
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultRatePerMinute: 30},
		NewMemoryStore(),
		counter,
	)
	// First 30 should pass; the next 5 should be rate limited.
	for index := 0; index < 30; index++ {
		_, err := executor.Execute(Command{
			ID:             "cmd_ok",
			Type:           SiteCreate,
			Version:        1,
			IdempotencyKey: "site:create:" + itoa(index),
			Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
		})
		if err != nil {
			t.Fatalf("command %d should have passed (30/min default): %v", index, err)
		}
	}
	for index := 30; index < 35; index++ {
		_, err := executor.Execute(Command{
			ID:             "cmd_blocked",
			Type:           SiteCreate,
			Version:        1,
			IdempotencyKey: "site:create:" + itoa(index),
			Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
		})
		if err == nil {
			t.Fatalf("command %d should have been rate limited", index)
		}
		if !strings.Contains(err.Error(), "rate limit exceeded") {
			t.Fatalf("command %d error did not mention the rate limit: %v", index, err)
		}
	}
	if got := counter.Calls(); got != 30 {
		t.Fatalf("the provisioner was reached %d times, expected exactly 30", got)
	}
}

func TestExecutorRateLimitRecoversAfterTime(t *testing.T) {
	counter := &counterProvisioner{}
	// 60/min means a token refills every second. Burn the whole bucket,
	// wait long enough for one token, then verify the next call passes.
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultRatePerMinute: 60},
		NewMemoryStore(),
		counter,
	)
	for index := 0; index < 60; index++ {
		_, err := executor.Execute(Command{
			ID:             "cmd_burst",
			Type:           SiteCreate,
			Version:        1,
			IdempotencyKey: "site:create:burst:" + itoa(index),
			Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
		})
		if err != nil {
			t.Fatalf("burst command %d should have passed: %v", index, err)
		}
	}
	if _, err := executor.Execute(Command{
		ID:             "cmd_blocked",
		Type:           SiteCreate,
		Version:        1,
		IdempotencyKey: "site:create:burst:blocked",
		Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
	}); err == nil {
		t.Fatal("the 61st call should have been rate limited")
	}
	// 1.1s gives the bucket time to refill at least one token.
	time.Sleep(1100 * time.Millisecond)
	if _, err := executor.Execute(Command{
		ID:             "cmd_recovered",
		Type:           SiteCreate,
		Version:        1,
		IdempotencyKey: "site:create:burst:recovered",
		Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
	}); err != nil {
		t.Fatalf("after a refill the call should have passed: %v", err)
	}
}

func TestExecutorExemptsSystemPing(t *testing.T) {
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultRatePerMinute: 1},
		NewMemoryStore(),
	)
	for index := 0; index < 100; index++ {
		if _, err := executor.Execute(Command{
			ID:             "cmd_ping",
			Type:           SystemPing,
			Version:        1,
			IdempotencyKey: "system:ping:" + itoa(index),
			Payload:        []byte(`{}`),
		}); err != nil {
			t.Fatalf("system.ping should never be rate limited (call %d): %v", index, err)
		}
	}
}

func TestExecutorRateLimitPerTypeIsolated(t *testing.T) {
	counter := &counterProvisioner{}
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultRatePerMinute: 30},
		NewMemoryStore(),
		counter,
	)
	// 30 site.create fits the bucket exactly; they should all pass.
	for index := 0; index < 30; index++ {
		if _, err := executor.Execute(Command{
			ID:             "cmd_site",
			Type:           SiteCreate,
			Version:        1,
			IdempotencyKey: "site:create:iso:" + itoa(index),
			Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
		}); err != nil {
			t.Fatalf("site.create %d should have passed: %v", index, err)
		}
	}
	// 30 site.suspends use a separate bucket; they should also pass.
	for index := 0; index < 30; index++ {
		if _, err := executor.Execute(Command{
			ID:             "cmd_suspend",
			Type:           SiteSuspend,
			Version:        1,
			IdempotencyKey: "site:suspend:iso:" + itoa(index),
			Payload:        []byte(`{"siteId":"example.com"}`),
		}); err != nil {
			t.Fatalf("site.suspend %d should have passed (separate bucket): %v", index, err)
		}
	}
}

func TestExecutorRateLimitErrorMessageIncludesRetry(t *testing.T) {
	executor := NewExecutorWithConfig(
		ExecutorConfig{DefaultRatePerMinute: 30},
		NewMemoryStore(),
		&counterProvisioner{},
	)
	for index := 0; index < 30; index++ {
		_, _ = executor.Execute(Command{
			ID:             "cmd_burst",
			Type:           SiteCreate,
			Version:        1,
			IdempotencyKey: "site:create:retry:" + itoa(index),
			Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
		})
	}
	_, err := executor.Execute(Command{
		ID:             "cmd_blocked",
		Type:           SiteCreate,
		Version:        1,
		IdempotencyKey: "site:create:retry:blocked",
		Payload:        []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`),
	})
	if err == nil {
		t.Fatal("the 31st call should have been rate limited")
	}
	message := err.Error()
	for _, want := range []string{"rate limit exceeded", "site.create", "retry in"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message is missing %q: %s", want, message)
		}
	}
}

// itoa avoids importing strconv just for the test keys.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// ----- Audit log integration --------------------------------------------

// Execute must record an audit event for every command that reaches the
// dispatch path, including failures, and the recorded hash must match the
// payload the executor actually saw.
func TestExecutorRecordsAuditEventForSuccessfulCommand(t *testing.T) {
	dir := t.TempDir()
	logger, err := audit.New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	executor.SetAuditLogger(logger)

	payload := []byte(`{"domain":"example.com","systemUser":"site-example","phpVersion":"8.4"}`)
	_, err = executor.Execute(Command{
		ID: "cmd_audit", Type: SiteCreate, Version: 1, IdempotencyKey: "site:audit",
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := readAuditEvents(t, filepath.Join(dir, "audit.log"))
	if len(events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(events))
	}
	got := events[0]
	if got.CommandID != "cmd_audit" {
		t.Fatalf("unexpected command id: %q", got.CommandID)
	}
	if got.Result != "ok" {
		t.Fatalf("unexpected result: %q", got.Result)
	}
	if got.PayloadSHA256 != audit.HashPayload(payload) {
		t.Fatalf("payload hash mismatch: got %q, want %q", got.PayloadSHA256, audit.HashPayload(payload))
	}
	if got.Actor != "control-plane" {
		t.Fatalf("unexpected actor: %q", got.Actor)
	}
}

func TestExecutorRecordsAuditEventForFailedCommand(t *testing.T) {
	dir := t.TempDir()
	logger, err := audit.New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}

	executor := NewExecutor(NewMemoryStore())
	executor.SetAuditLogger(logger)
	_, err = executor.Execute(Command{
		ID: "cmd_audit_fail", Type: "shell.execute", Version: 1, IdempotencyKey: "unsafe:1",
		Payload: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected an unsupported command error")
	}

	events := readAuditEvents(t, filepath.Join(dir, "audit.log"))
	if len(events) != 1 {
		t.Fatalf("expected one audit event, got %d", len(events))
	}
	if events[0].Result != "failed" {
		t.Fatalf("unexpected result: %q", events[0].Result)
	}
}

func readAuditEvents(t *testing.T, path string) []audit.Event {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []audit.Event
	for _, line := range bytes.Split(bytes.TrimRight(contents, "\n"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("invalid NDJSON line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}
