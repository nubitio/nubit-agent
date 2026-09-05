package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nubitio/nubit-agent/internal/command"
	"github.com/nubitio/nubit-agent/internal/controlplane"
	"github.com/nubitio/nubit-agent/internal/site"
)

// errDaemonRunning is returned by the mutating actions when the daemon is up.
// Two processes writing sites.json / outbox.json race; the operator either
// stops the unit first (`systemctl stop nubit-agent`) or drives the change
// from Control with `app:agent:dispatch`.
var errDaemonRunning = errors.New("the nubit-agent daemon is running — stop it (systemctl stop nubit-agent) to run node-local mutations, or use `app:agent:dispatch` from Control")

func newProvisioner(stateDir string) (site.Provisioner, error) {
	store, err := site.NewFileStateStore(filepath.Join(stateDir, sitesFile))
	if err != nil {
		return site.Provisioner{}, err
	}
	return site.Provisioner{Runner: site.OSRunner{}, Store: store}, nil
}

// runReconcile reports drift between local state and the host. It only reads
// the filesystem, so it is safe to run while the daemon polls.
func runReconcile(stateDir string) (string, error) {
	p, err := newProvisioner(stateDir)
	if err != nil {
		return "", err
	}
	drifts, err := p.Reconcile()
	if err != nil {
		return "", err
	}
	if len(drifts) == 0 {
		return "No drift — local state matches the host.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d drift(s):\n", len(drifts))
	for _, d := range drifts {
		fmt.Fprintf(&b, "  %s / %s: expected %s, got %s\n", d.SiteID, d.Resource, d.Expected, d.Actual)
	}
	return b.String(), nil
}

// runNodeReset force-removes every site this node created and clears the
// local command/outbox state — the same effect as Control's system.reset,
// run from the box. Gated on the daemon being stopped.
func runNodeReset(stateDir string, daemonRunning bool) (site.ResetResult, error) {
	if daemonRunning {
		return site.ResetResult{}, errDaemonRunning
	}
	p, err := newProvisioner(stateDir)
	if err != nil {
		return site.ResetResult{}, err
	}
	res, err := p.Reset()
	if err != nil {
		return res, err
	}
	if cs, csErr := command.NewFileStore(filepath.Join(stateDir, "commands.json")); csErr == nil {
		_ = cs.Reset()
	}
	if ob, obErr := controlplane.NewFileOutbox(filepath.Join(stateDir, outboxFile)); obErr == nil {
		_ = ob.Reset()
	}
	return res, nil
}

// flushOutboxNow drains pending command results to Control. Gated on the
// daemon being stopped so the two do not both mutate outbox.json. The token
// is read from the environment, the same NUBIT_AGENT_TOKEN the daemon uses.
func flushOutboxNow(ctx context.Context, stateDir, controlURL string, daemonRunning bool) (int, error) {
	if daemonRunning {
		return 0, errDaemonRunning
	}
	token := os.Getenv("NUBIT_AGENT_TOKEN")
	if controlURL == "" || token == "" {
		return 0, errors.New("outbox flush needs NUBIT_CONTROL_URL and NUBIT_AGENT_TOKEN in the environment")
	}
	ob, err := controlplane.NewFileOutbox(filepath.Join(stateDir, outboxFile))
	if err != nil {
		return 0, err
	}
	return controlplane.Flush(ctx, controlplane.NewClient(controlURL, token), ob)
}
