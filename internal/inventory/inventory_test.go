package inventory

import (
	"testing"

	"github.com/nubitio/nubit-agent/internal/site"
)

type fakeRuntimeProvider struct{}

func (fakeRuntimeProvider) RuntimeInventory() ([]site.RuntimeInfo, error) {
	return []site.RuntimeInfo{{Version: "8.4", Installed: true, SiteCount: 2}}, nil
}

func TestCollectIncludesCapabilitiesAndPHPRuntimes(t *testing.T) {
	snapshot, err := Collect(fakeRuntimeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Capabilities) == 0 || len(snapshot.PHPRuntimes) != 1 {
		t.Fatalf("unexpected inventory: %#v", snapshot)
	}
}
