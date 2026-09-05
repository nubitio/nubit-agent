// Package status exposes the running agent's live operational state so an
// operator on the box can see it without reading JSON files under
// /var/lib/nubit-agent by hand. The daemon serves a Snapshot at GET /status
// next to /healthz; `nubit-agent tui` consumes the same endpoint.
//
// Nothing here is persisted: a restart resets the counters, which is the
// point — they describe this process's session, not the node's history.
package status

import (
	"sync"
	"time"
)

// Transport names the control-plane authentication in force.
const (
	TransportToken   = "token"
	TransportMTLS    = "mtls"
	TransportOffline = "offline"
)

// Snapshot is the JSON body of GET /status. Field names are stable so the
// TUI and ad-hoc `curl | jq` both keep working.
type Snapshot struct {
	Version           string     `json:"version"`
	StartedAt         time.Time  `json:"startedAt"`
	UptimeSeconds     int64      `json:"uptimeSeconds"`
	ListenAddr        string     `json:"listenAddr"`
	StateDir          string     `json:"stateDir"`
	ControlURL        string     `json:"controlUrl"`
	Transport         string     `json:"transport"`
	Enrolled          bool       `json:"enrolled"`
	CertNotAfter      *time.Time `json:"certNotAfter,omitempty"`
	PollInterval      string     `json:"pollInterval,omitempty"`
	Polling           bool       `json:"polling"`
	LastPollAt        *time.Time `json:"lastPollAt,omitempty"`
	LastPollOK        bool       `json:"lastPollOk"`
	LastPollError     string     `json:"lastPollError,omitempty"`
	PollsOK           int        `json:"pollsOk"`
	PollsFailed       int        `json:"pollsFailed"`
	JobsFetched       int        `json:"jobsFetched"`
	JobsExecuted      int        `json:"jobsExecuted"`
	OutboxDepth       int        `json:"outboxDepth"`
	SiteCount         int        `json:"siteCount"`
	SelfUpdatePending bool       `json:"selfUpdatePending"`
}

// Reporter accumulates the mutable parts of a Snapshot. It is safe for
// concurrent use: the poll loop writes, the HTTP handler and TUI read.
type Reporter struct {
	mu   sync.RWMutex
	snap Snapshot
	now  func() time.Time

	outboxDepth func() int
	siteCount   func() int
}

// New seeds a Reporter with the fields fixed at startup (version, listen
// address, transport). StartedAt defaults to now when the caller leaves it
// zero.
func New(static Snapshot) *Reporter {
	r := &Reporter{snap: static, now: time.Now}
	if r.snap.StartedAt.IsZero() {
		r.snap.StartedAt = time.Now().UTC()
	}
	return r
}

// WithClock overrides the time source. Tests use it; production does not.
func (r *Reporter) WithClock(now func() time.Time) *Reporter {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
	return r
}

// SetDynamic registers callbacks read at Snapshot time for values the
// Reporter does not own — outbox depth and local site count both live in
// their own stores.
func (r *Reporter) SetDynamic(outboxDepth, siteCount func() int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outboxDepth = outboxDepth
	r.siteCount = siteCount
}

// MarkPolling records that the poll loop has started (or that it never will,
// on a node with no control URL). Interval is a human string like "15s".
func (r *Reporter) MarkPolling(polling bool, interval string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Polling = polling
	r.snap.PollInterval = interval
}

// SetTransport records which control-plane credential is in force and
// whether the agent has an enrolled client certificate.
func (r *Reporter) SetTransport(transport string, enrolled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.Transport = transport
	r.snap.Enrolled = enrolled
}

// RecordPoll folds the outcome of one poll into the snapshot. err nil means
// the poll reached Control; fetched/executed are the counts from that pass.
func (r *Reporter) RecordPoll(err error, fetched, executed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := r.now().UTC()
	r.snap.LastPollAt = &at
	r.snap.JobsFetched += fetched
	r.snap.JobsExecuted += executed
	if err != nil {
		r.snap.LastPollOK = false
		r.snap.LastPollError = err.Error()
		r.snap.PollsFailed++
		return
	}
	r.snap.LastPollOK = true
	r.snap.LastPollError = ""
	r.snap.PollsOK++
}

// SetCertNotAfter records the enrolled client certificate's expiry.
func (r *Reporter) SetCertNotAfter(notAfter time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := notAfter.UTC()
	r.snap.CertNotAfter = &t
}

// SetSelfUpdatePending records that a newer binary is staged and the agent
// will exit for a restart once it is idle.
func (r *Reporter) SetSelfUpdatePending(pending bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap.SelfUpdatePending = pending
}

// Snapshot returns a consistent copy with the derived fields (uptime,
// outbox depth, site count) filled in.
func (r *Reporter) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := r.snap
	out.UptimeSeconds = int64(r.now().UTC().Sub(out.StartedAt).Seconds())
	if out.UptimeSeconds < 0 {
		out.UptimeSeconds = 0
	}
	if r.outboxDepth != nil {
		out.OutboxDepth = r.outboxDepth()
	}
	if r.siteCount != nil {
		out.SiteCount = r.siteCount()
	}
	return out
}
