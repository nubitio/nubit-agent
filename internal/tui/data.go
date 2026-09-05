// Package tui implements `nubit-agent tui`, a full-screen operator cockpit
// for one node. It is deliberately narrow: it reads the running daemon's
// GET /status and the shared state files under the state directory, and it
// runs a small set of confirmed local actions through the same code paths as
// the closed command set. It never opens a shell and never mutates Control.
package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nubitio/nubit-agent/internal/audit"
	"github.com/nubitio/nubit-agent/internal/site"
	"github.com/nubitio/nubit-agent/internal/status"
)

// statusFile paths, relative to the state directory.
const (
	outboxFile = "outbox.json"
	sitesFile  = "sites.json"
	auditFile  = "audit.log"
)

// fetchStatus reads GET /status from the running daemon. A daemon that is
// down yields an error the Overview panel renders in red — the rest of the
// TUI still works from the state files alone.
func fetchStatus(client *http.Client, baseURL string) (status.Snapshot, error) {
	var snap status.Snapshot
	req, err := http.NewRequest(http.MethodGet, baseURL+"/status", nil)
	if err != nil {
		return snap, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return snap, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snap, errors.New("daemon /status returned " + resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snap, err
	}
	return snap, nil
}

// pendingResult mirrors controlplane.PendingResult without importing the
// package (which would pull the S3/telemetry graph into the TUI binary path
// for no reason — the TUI only ever reads this file).
type pendingResult struct {
	CommandID string          `json:"commandId"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
}

func loadOutbox(stateDir string) ([]pendingResult, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, outboxFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var byID map[string]pendingResult
	if err := json.Unmarshal(raw, &byID); err != nil {
		return nil, err
	}
	out := make([]pendingResult, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CommandID < out[j].CommandID })
	return out, nil
}

// jobRow is one line in the Jobs panel, sourced from the audit log (which,
// unlike commands.json, carries the type, the time and the duration).
type jobRow struct {
	When     time.Time
	Type     string
	Result   string
	Duration time.Duration
	Key      string
}

func loadJobs(stateDir string, limit int) ([]jobRow, error) {
	f, err := os.Open(filepath.Join(stateDir, auditFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []jobRow
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev audit.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		when, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
		rows = append(rows, jobRow{
			When:     when,
			Type:     ev.CommandType,
			Result:   ev.Result,
			Duration: time.Duration(ev.DurationMs) * time.Millisecond,
			Key:      ev.IdempotencyKey,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Newest first, capped.
	sort.Slice(rows, func(i, j int) bool { return rows[i].When.After(rows[j].When) })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func loadSites(stateDir string) ([]site.State, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, sitesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// FileStateStore persists a map keyed by siteID.
	var byID map[string]site.State
	if err := json.Unmarshal(raw, &byID); err != nil {
		// Older/alternate encodings may store a list; try that before giving up.
		var list []site.State
		if listErr := json.Unmarshal(raw, &list); listErr != nil {
			return nil, err
		}
		return sortedSites(list), nil
	}
	out := make([]site.State, 0, len(byID))
	for _, v := range byID {
		out = append(out, v)
	}
	return sortedSites(out), nil
}

func sortedSites(in []site.State) []site.State {
	sort.Slice(in, func(i, j int) bool { return in[i].SiteID < in[j].SiteID })
	return in
}

// controlServer is the slice of nubit-control's /api/servers rows the
// read-only Control panel shows. Unknown fields are ignored.
type controlServer struct {
	Hostname     string `json:"hostname"`
	Status       string `json:"status"`
	LastSeenAt   string `json:"lastSeenAt"`
	Enrolled     bool   `json:"enrolled"`
	AgentVersion string `json:"agentVersion"`
}

func fetchControlServers(client *http.Client, baseURL, token string) ([]controlServer, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/servers", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("GET /api/servers returned " + resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// A plain JSON array …
	var list []controlServer
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		return list, nil
	}
	// … or an API Platform / Hydra collection envelope.
	var envelope struct {
		Member []controlServer `json:"member"`
		Hydra  []controlServer `json:"hydra:member"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if len(envelope.Member) > 0 {
			return envelope.Member, nil
		}
		return envelope.Hydra, nil
	}
	return nil, nil
}
