package tui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nubitio/nubit-agent/internal/site"
	"github.com/nubitio/nubit-agent/internal/status"
)

// Options configures a TUI session. Enroll is injected by cmd/nubit-agent so
// the package does not import the enrollment graph directly.
type Options struct {
	StatusURL         string
	StateDir          string
	ControlURL        string
	Refresh           time.Duration
	ControlAdminURL   string
	ControlAdminToken string
	Enroll            func(ctx context.Context, token string) (time.Time, error)
}

// Run starts the TUI and blocks until the operator quits or ctx is done.
func Run(ctx context.Context, opts Options) error {
	if opts.Refresh <= 0 {
		opts.Refresh = 2 * time.Second
	}
	m := newModel(ctx, opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

type tab int

const (
	tabOverview tab = iota
	tabJobs
	tabSites
	tabActions
	tabControl
)

func (t tab) title() string {
	return [...]string{"Overview", "Jobs", "Sites", "Actions", "Control"}[t]
}

type model struct {
	ctx        context.Context
	opts       Options
	http       *http.Client
	width      int
	height     int
	active     tab
	hasControl bool

	snap      status.Snapshot
	snapErr   error
	gotStatus bool

	jobs      []jobRow
	outbox    []pendingResult
	jobsErr   error
	sites     []site.State
	siteIdx   int
	sitesErr  error
	servers   []controlServer
	serverErr error

	// Actions panel.
	actionIdx int
	input     textinput.Model
	inputFor  string // "enroll" | "reset" | ""
	actionMsg string
	actionErr error
	working   bool
}

var actionItems = []string{"Reconcile (drift report)", "Flush outbox to Control", "Enroll (mTLS)", "Reset node"}

func newModel(ctx context.Context, opts Options) model {
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 200
	return model{
		ctx:        ctx,
		opts:       opts,
		http:       &http.Client{Timeout: 4 * time.Second},
		active:     tabOverview,
		hasControl: opts.ControlAdminURL != "",
		input:      in,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refreshAll(), tick(m.opts.Refresh))
}

// ---- messages -------------------------------------------------------------

type tickMsg struct{}
type statusMsg struct {
	snap status.Snapshot
	err  error
}
type jobsMsg struct {
	jobs   []jobRow
	outbox []pendingResult
	err    error
}
type sitesMsg struct {
	sites []site.State
	err   error
}
type controlMsg struct {
	servers []controlServer
	err     error
}
type actionResultMsg struct {
	body string
	err  error
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) refreshAll() tea.Cmd {
	cmds := []tea.Cmd{m.loadStatus(), m.loadJobs(), m.loadSites()}
	if m.hasControl {
		cmds = append(cmds, m.loadControl())
	}
	return tea.Batch(cmds...)
}

func (m model) loadStatus() tea.Cmd {
	return func() tea.Msg {
		snap, err := fetchStatus(m.http, m.opts.StatusURL)
		return statusMsg{snap: snap, err: err}
	}
}

func (m model) loadJobs() tea.Cmd {
	return func() tea.Msg {
		jobs, err := loadJobs(m.opts.StateDir, 200)
		ob, obErr := loadOutbox(m.opts.StateDir)
		if err == nil {
			err = obErr
		}
		return jobsMsg{jobs: jobs, outbox: ob, err: err}
	}
}

func (m model) loadSites() tea.Cmd {
	return func() tea.Msg {
		sites, err := loadSites(m.opts.StateDir)
		return sitesMsg{sites: sites, err: err}
	}
}

func (m model) loadControl() tea.Cmd {
	return func() tea.Msg {
		servers, err := fetchControlServers(m.http, strings.TrimRight(m.opts.ControlAdminURL, "/"), m.opts.ControlAdminToken)
		return controlMsg{servers: servers, err: err}
	}
}

// daemonRunning reports whether GET /status answered on the last refresh.
// Before the first status result comes back it reports true (fail safe: the
// mutating actions stay gated until we know the daemon is down).
func (m model) daemonRunning() bool {
	if !m.gotStatus {
		return true
	}
	return m.snapErr == nil
}

// ---- update -------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshAll(), tick(m.opts.Refresh))

	case statusMsg:
		m.snap, m.snapErr, m.gotStatus = msg.snap, msg.err, true
		return m, nil
	case jobsMsg:
		m.jobs, m.outbox, m.jobsErr = msg.jobs, msg.outbox, msg.err
		return m, nil
	case sitesMsg:
		m.sites, m.sitesErr = msg.sites, msg.err
		if m.siteIdx >= len(m.sites) {
			m.siteIdx = max(0, len(m.sites)-1)
		}
		return m, nil
	case controlMsg:
		m.servers, m.serverErr = msg.servers, msg.err
		return m, nil

	case actionResultMsg:
		m.working = false
		m.actionErr = msg.err
		m.actionMsg = msg.body
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	if m.inputFor != "" {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// An open text input captures everything except escape/enter/ctrl+c.
	if m.inputFor != "" {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.inputFor = ""
			m.input.Blur()
			m.input.SetValue("")
			return m, nil
		case "enter":
			return m.submitInput()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "l", "right":
		m.active = (m.active + 1) % m.tabCount()
		return m, nil
	case "shift+tab", "h", "left":
		m.active = (m.active - 1 + m.tabCount()) % m.tabCount()
		return m, nil
	case "1", "2", "3", "4", "5":
		if n := tab(msg.String()[0] - '1'); n < m.tabCount() {
			m.active = n
		}
		return m, nil
	case "r":
		return m, m.refreshAll()
	}

	switch m.active {
	case tabSites:
		return m.handleSitesKey(msg)
	case tabActions:
		return m.handleActionsKey(msg)
	}
	return m, nil
}

func (m model) tabCount() tab {
	if m.hasControl {
		return 5
	}
	return 4
}

func (m model) handleSitesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.siteIdx = max(0, m.siteIdx-1)
	case "down", "j":
		m.siteIdx = min(len(m.sites)-1, m.siteIdx+1)
	}
	return m, nil
}

func (m model) handleActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.actionIdx = max(0, m.actionIdx-1)
	case "down", "j":
		m.actionIdx = min(len(actionItems)-1, m.actionIdx+1)
	case "enter":
		return m.startAction()
	}
	return m, nil
}

func (m model) startAction() (tea.Model, tea.Cmd) {
	m.actionMsg, m.actionErr = "", nil
	switch m.actionIdx {
	case 0: // reconcile
		m.working = true
		return m, func() tea.Msg {
			out, err := runReconcile(m.opts.StateDir)
			return actionResultMsg{body: out, err: err}
		}
	case 1: // flush outbox
		m.working = true
		ctx, running := m.ctx, m.daemonRunning()
		return m, func() tea.Msg {
			n, err := flushOutboxNow(ctx, m.opts.StateDir, m.opts.ControlURL, running)
			if err != nil {
				return actionResultMsg{err: err}
			}
			return actionResultMsg{body: fmt.Sprintf("Flushed %d pending result(s).", n)}
		}
	case 2: // enroll
		m.inputFor = "enroll"
		m.input.Placeholder = "one-time enrollment token"
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	case 3: // reset
		if m.daemonRunning() {
			m.actionErr = errDaemonRunning
			return m, nil
		}
		m.inputFor = "reset"
		m.input.Placeholder = "type RESET to confirm"
		m.input.SetValue("")
		m.input.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m model) submitInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	kind := m.inputFor
	m.inputFor = ""
	m.input.Blur()
	m.input.SetValue("")

	switch kind {
	case "enroll":
		if value == "" {
			m.actionErr = fmt.Errorf("no token entered")
			return m, nil
		}
		m.working = true
		ctx, enroll := m.ctx, m.opts.Enroll
		return m, func() tea.Msg {
			if enroll == nil {
				return actionResultMsg{err: fmt.Errorf("enrollment not wired")}
			}
			exp, err := enroll(ctx, value)
			if err != nil {
				return actionResultMsg{err: err}
			}
			return actionResultMsg{body: "Enrolled. Certificate expires " + exp.UTC().Format(time.RFC3339) + ". Restart the daemon to pick up mTLS."}
		}
	case "reset":
		if value != "RESET" {
			m.actionErr = fmt.Errorf("confirmation mismatch — node NOT reset")
			return m, nil
		}
		m.working = true
		return m, func() tea.Msg {
			res, err := runNodeReset(m.opts.StateDir, m.daemonRunning())
			if err != nil {
				return actionResultMsg{err: err}
			}
			body := fmt.Sprintf("Reset done. Removed %d site(s).", len(res.Deleted))
			if len(res.Errors) > 0 {
				body += "\nErrors:\n  " + strings.Join(res.Errors, "\n  ")
			}
			return actionResultMsg{body: body}
		}
	}
	return m, nil
}

// ---- view -------------------------------------------------------------

var (
	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Padding(0, 1)
	styleTabInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
	styleKey         = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleOK          = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn        = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleErr         = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleHead        = lipgloss.NewStyle().Bold(true)
)

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.renderTabs())
	b.WriteString("\n\n")

	switch m.active {
	case tabOverview:
		b.WriteString(m.viewOverview())
	case tabJobs:
		b.WriteString(m.viewJobs())
	case tabSites:
		b.WriteString(m.viewSites())
	case tabActions:
		b.WriteString(m.viewActions())
	case tabControl:
		b.WriteString(m.viewControl())
	}

	b.WriteString("\n\n")
	b.WriteString(styleKey.Render("tab/1-5 switch · r refresh · ↑↓ select · enter act · q quit"))
	return b.String()
}

func (m model) renderTabs() string {
	parts := make([]string, 0, int(m.tabCount()))
	for t := tabOverview; t < m.tabCount(); t++ {
		label := fmt.Sprintf("%d %s", int(t)+1, t.title())
		if t == m.active {
			parts = append(parts, styleTabActive.Render(label))
		} else {
			parts = append(parts, styleTabInactive.Render(label))
		}
	}
	return "nubit-agent  " + strings.Join(parts, " ")
}

func (m model) viewOverview() string {
	if !m.gotStatus {
		return styleKey.Render("connecting to " + m.opts.StatusURL + "/status …")
	}
	if m.snapErr != nil {
		return styleErr.Render("daemon /status unreachable: "+m.snapErr.Error()) +
			"\n" + styleKey.Render("Jobs/Sites panels still work from the state files. Node-local actions are enabled while the daemon is down.")
	}
	s := m.snap
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "  %-16s %s\n", k, v) }

	pollState := styleOK.Render("ok")
	switch {
	case !s.Polling:
		pollState = styleWarn.Render("disabled (no NUBIT_CONTROL_URL)")
	case s.LastPollAt == nil:
		pollState = styleWarn.Render("starting")
	case !s.LastPollOK:
		pollState = styleErr.Render("failing: " + s.LastPollError)
	case time.Since(*s.LastPollAt) > 3*time.Minute:
		pollState = styleWarn.Render("stale, last " + s.LastPollAt.Format(time.RFC3339))
	}

	b.WriteString(styleHead.Render("Node") + "\n")
	line("version", s.Version)
	line("uptime", (time.Duration(s.UptimeSeconds) * time.Second).String())
	line("listen", s.ListenAddr)
	line("state dir", s.StateDir)
	b.WriteString("\n" + styleHead.Render("Control") + "\n")
	line("url", orDash(s.ControlURL))
	line("transport", s.Transport)
	if s.Enrolled {
		exp := "unknown"
		if s.CertNotAfter != nil {
			exp = s.CertNotAfter.Format(time.RFC3339)
		}
		line("cert expires", exp)
	}
	line("poll", pollState)
	if s.LastPollAt != nil {
		line("last poll", s.LastPollAt.Format(time.RFC3339))
	}
	line("poll interval", orDash(s.PollInterval))
	line("polls ok/fail", fmt.Sprintf("%d / %d", s.PollsOK, s.PollsFailed))
	line("jobs fetched", fmt.Sprintf("%d", s.JobsFetched))
	line("jobs executed", fmt.Sprintf("%d", s.JobsExecuted))
	b.WriteString("\n" + styleHead.Render("Work") + "\n")
	line("outbox depth", fmt.Sprintf("%d", s.OutboxDepth))
	line("local sites", fmt.Sprintf("%d", s.SiteCount))
	if s.SelfUpdatePending {
		line("self-update", styleWarn.Render("staged — daemon will restart when idle"))
	}
	return b.String()
}

func (m model) viewJobs() string {
	var b strings.Builder
	b.WriteString(styleHead.Render(fmt.Sprintf("Recent commands (audit log, newest first) — %d", len(m.jobs))) + "\n")
	if m.jobsErr != nil {
		b.WriteString(styleErr.Render("  "+m.jobsErr.Error()) + "\n")
	}
	shown := m.jobs
	if len(shown) > 18 {
		shown = shown[:18]
	}
	if len(shown) == 0 {
		b.WriteString(styleKey.Render("  no audit entries yet") + "\n")
	}
	for _, j := range shown {
		res := j.Result
		switch j.Result {
		case "ok", "success", "succeeded":
			res = styleOK.Render(j.Result)
		case "failed", "error":
			res = styleErr.Render(j.Result)
		}
		when := "?"
		if !j.When.IsZero() {
			when = j.When.Format("01-02 15:04:05")
		}
		fmt.Fprintf(&b, "  %s  %-24s %-9s %s\n", when, truncate(j.Type, 24), res, j.Duration.Round(time.Millisecond))
	}

	b.WriteString("\n" + styleHead.Render(fmt.Sprintf("Pending outbox — %d", len(m.outbox))) + "\n")
	if len(m.outbox) == 0 {
		b.WriteString(styleKey.Render("  empty — every result has been acknowledged by Control") + "\n")
	}
	for _, p := range m.outbox {
		row := fmt.Sprintf("  %-12s %s", p.Status, p.CommandID)
		if p.Error != "" {
			row += styleErr.Render("  " + truncate(p.Error, 60))
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (m model) viewSites() string {
	var b strings.Builder
	b.WriteString(styleHead.Render(fmt.Sprintf("Sites — %d", len(m.sites))) + "\n")
	if m.sitesErr != nil {
		b.WriteString(styleErr.Render("  "+m.sitesErr.Error()) + "\n")
	}
	if len(m.sites) == 0 {
		b.WriteString(styleKey.Render("  no sites in sites.json") + "\n")
		return b.String()
	}
	for i, s := range m.sites {
		cursor := "  "
		if i == m.siteIdx {
			cursor = "> "
		}
		st := s.Status
		if st == "" {
			st = "active"
		}
		fmt.Fprintf(&b, "%s%-28s %-14s php%-4s %s\n", cursor, truncate(s.SiteID, 28), truncate(s.SystemUser, 14), s.PHPVersion, st)
	}
	if m.siteIdx < len(m.sites) {
		s := m.sites[m.siteIdx]
		b.WriteString("\n" + styleHead.Render("Detail: "+s.SiteID) + "\n")
		line := func(k, v string) { fmt.Fprintf(&b, "  %-14s %s\n", k, v) }
		line("domains", strings.Join(s.Domains, ", "))
		line("document root", s.DocumentRoot)
		line("php socket", s.PHPSocket)
		line("databases", orDash(strings.Join(s.Databases, ", ")))
		line("db users", orDash(strings.Join(s.DatabaseUsers, ", ")))
		line("sftp", fmt.Sprintf("%t", s.SFTPEnabled))
		if len(s.FTPUsers) > 0 {
			line("extra sftp", strings.Join(s.FTPUsers, ", "))
		}
		line("workers", fmt.Sprintf("%d", s.Resources.Workers))
		line("memory MB", fmt.Sprintf("%d", s.Resources.MemoryLimitMB))
	}
	return b.String()
}

func (m model) viewActions() string {
	var b strings.Builder
	b.WriteString(styleHead.Render("Node actions") + "\n")
	if m.daemonRunning() {
		b.WriteString(styleWarn.Render("  daemon is running — Reconcile is read-only and safe; Flush/Reset need the unit stopped") + "\n")
	} else {
		b.WriteString(styleKey.Render("  daemon is stopped — all actions available") + "\n")
	}
	b.WriteString("\n")
	for i, it := range actionItems {
		cursor := "  "
		if i == m.actionIdx {
			cursor = "> "
		}
		b.WriteString(cursor + it + "\n")
	}
	if m.inputFor != "" {
		b.WriteString("\n" + m.input.View() + "\n")
		b.WriteString(styleKey.Render("enter confirm · esc cancel") + "\n")
	}
	if m.working {
		b.WriteString("\n" + styleKey.Render("working…") + "\n")
	}
	if m.actionErr != nil {
		b.WriteString("\n" + styleErr.Render(m.actionErr.Error()) + "\n")
	}
	if m.actionMsg != "" {
		b.WriteString("\n" + m.actionMsg + "\n")
	}
	return b.String()
}

func (m model) viewControl() string {
	var b strings.Builder
	b.WriteString(styleHead.Render("Control — "+m.opts.ControlAdminURL) + "  " + styleKey.Render("(read-only)") + "\n")
	if m.serverErr != nil {
		b.WriteString(styleErr.Render("  "+m.serverErr.Error()) + "\n")
		b.WriteString(styleKey.Render("  set --control-admin-token (a ROLE_ADMIN bearer) to read /api/servers") + "\n")
		return b.String()
	}
	if len(m.servers) == 0 {
		b.WriteString(styleKey.Render("  no servers returned") + "\n")
		return b.String()
	}
	for _, s := range m.servers {
		st := s.Status
		switch s.Status {
		case "online":
			st = styleOK.Render(s.Status)
		case "offline", "unreachable":
			st = styleErr.Render(s.Status)
		}
		fmt.Fprintf(&b, "  %-28s %-10s enrolled=%t  v%s  seen %s\n", truncate(s.Hostname, 28), st, s.Enrolled, orDash(s.AgentVersion), orDash(s.LastSeenAt))
	}
	b.WriteString("\n" + styleKey.Render("  To queue a command against a node, run `app:agent:dispatch` on Control.") + "\n")
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
