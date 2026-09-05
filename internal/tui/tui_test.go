package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel(t *testing.T) model {
	t.Helper()
	return newModel(context.Background(), Options{
		StatusURL: "http://127.0.0.1:0",
		StateDir:  t.TempDir(),
		Refresh:   time.Hour,
	})
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func step(m model, k tea.KeyMsg) model {
	got, _ := m.Update(k)
	return got.(model)
}

func TestTabNavigation(t *testing.T) {
	m := newTestModel(t)
	if m.active != tabOverview {
		t.Fatalf("initial tab = %d", m.active)
	}
	if m.tabCount() != 4 {
		t.Fatalf("tabCount without control = %d, want 4", m.tabCount())
	}
	m = step(m, key("3"))
	if m.active != tabSites {
		t.Fatalf("after '3' active = %d, want tabSites", m.active)
	}
	// tab from the last non-control tab (Actions) wraps to Overview.
	m.active = tabActions
	m = step(m, key("tab"))
	if m.active != tabOverview {
		t.Fatalf("tab did not wrap: active = %d", m.active)
	}
}

func TestControlTabOnlyWithAdminURL(t *testing.T) {
	m := newModel(context.Background(), Options{StateDir: t.TempDir(), ControlAdminURL: "http://c"})
	if m.tabCount() != 5 || !m.hasControl {
		t.Fatalf("control tab not enabled: count=%d has=%v", m.tabCount(), m.hasControl)
	}
}

func TestResetGatedWhileDaemonRunning(t *testing.T) {
	m := newTestModel(t)
	m.active = tabActions
	m.actionIdx = 3 // Reset node
	m.gotStatus = true
	m.snapErr = nil // daemon reachable
	m = step(m, key("enter"))
	if m.inputFor != "" {
		t.Fatalf("reset prompt opened while daemon running")
	}
	if !errors.Is(m.actionErr, errDaemonRunning) {
		t.Fatalf("expected errDaemonRunning, got %v", m.actionErr)
	}
}

func TestResetConfirmationMismatch(t *testing.T) {
	m := newTestModel(t)
	m.active = tabActions
	m.actionIdx = 3
	m.gotStatus = true
	m.snapErr = errors.New("connection refused") // daemon down
	m = step(m, key("enter"))
	if m.inputFor != "reset" {
		t.Fatalf("reset prompt not open: %q", m.inputFor)
	}
	for _, r := range "nope" {
		m = step(m, key(string(r)))
	}
	m = step(m, key("enter"))
	if m.inputFor != "" {
		t.Fatalf("input should close after submit")
	}
	if m.actionErr == nil || !strings.Contains(m.actionErr.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got %v", m.actionErr)
	}
}

func TestViewRendersEveryTabWithoutPanic(t *testing.T) {
	m := newTestModel(t)
	m.gotStatus = true
	m.snapErr = errors.New("down")
	for _, tb := range []tab{tabOverview, tabJobs, tabSites, tabActions} {
		m.active = tb
		if out := m.View(); out == "" {
			t.Fatalf("empty view for tab %d", tb)
		}
	}
}
