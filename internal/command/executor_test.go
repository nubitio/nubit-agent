package command

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/nubitio/nubit-agent/internal/files"
	"github.com/nubitio/nubit-agent/internal/site"
)

type fakeSiteProvisioner struct{}

func (fakeSiteProvisioner) Create(domain, user, phpVersion string) (site.CreateResult, error) {
	return site.CreateResult{SiteID: domain, DocumentRoot: "/srv/" + domain + "/public", PHPSocket: "/run/php/" + user + ".sock", CaddyConfigHash: "sha256:caddy", PHPConfigHash: "sha256:php"}, nil
}

func (fakeSiteProvisioner) Inspect(siteID string) (site.State, error) {
	return site.State{SiteID: siteID, PHPVersion: "8.4"}, nil
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

type fakeFilesProvisioner struct{}

func (fakeFilesProvisioner) List(siteID, rel string) (files.ListResult, error) {
	return files.ListResult{Path: rel, Entries: []files.Entry{{Name: "index.html", Type: "file", Size: 4}}}, nil
}

func (fakeFilesProvisioner) Mkdir(string, string) error { return nil }
func (fakeFilesProvisioner) Write(string, string, []byte) error { return nil }
func (fakeFilesProvisioner) Read(string, string) (files.ReadResult, error) {
	return files.ReadResult{Name: "index.html", Size: 4, Content: []byte("hola")}, nil
}
func (fakeFilesProvisioner) Delete(string, string) error { return nil }
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
	result, err := executor.Execute(Command{ID: "cmd_php", Type: PHPSetVersion, Version: 1, IdempotencyKey: "site:php", Payload: []byte(`{"siteId":"example.com","phpVersion":"8.5"}`)})
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
	_, err := executor.Execute(Command{ID: "cmd_remove", Type: PHPRuntimeRemove, Version: 1, IdempotencyKey: "php:remove", Payload: []byte(`{"phpVersion":"8.3","confirm":false}`)})
	if err == nil {
		t.Fatal("expected explicit confirmation to be required")
	}
}

func TestExecutorReportsRuntimeInventory(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeSiteProvisioner{})
	result, err := executor.Execute(Command{ID: "cmd_runtimes", Type: PHPRuntimeInspect, Version: 1, IdempotencyKey: "php:inspect", Payload: []byte(`{}`)})
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
