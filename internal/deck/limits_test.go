package deck

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DavidGodefroid/lea/internal/settings"
)

// A live limit of the settings screen (#420) is set through the running daemon (limits_set),
// which applies it and writes the file: the deck never writes it itself. A value the daemon
// refuses keeps the field open with why.
func TestTheSettingsSetALiveLimitThroughTheDaemon(t *testing.T) {
	api := &fakeAPI{}
	st := someSettings()
	st.list = append(st.list, settings.Setting{Key: "LEA_MAX_RUNNING", Group: "Limits", Name: "max_running", Help: "the most children",
		Kind: settings.Text, Value: "3", Default: "3", Source: settings.SourceDefault, Apply: settings.ApplyNow, Live: true, Limit: "max_running"})
	m := withSettings(t, api, st)
	m, _ = step(t, m, key(","))
	for range len(st.list) - 1 {
		m, _ = step(t, m, key("j"))
	}
	if view := strings.Join(viewLines(m), "\n"); !strings.Contains(view, "Limits") || !strings.Contains(view, "applies now") {
		t.Fatalf("view:\n%s", view)
	}
	m, _ = step(t, m, key("enter"))
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeIn(t, m, "1")
	m, _ = step(t, m, key("enter"))
	if !slices.Contains(api.got(), "limit max_running 1") || len(st.sets) != 0 || m.setEditing {
		t.Fatalf("calls=%q sets=%q editing=%v status=%q", api.got(), st.sets, m.setEditing, m.status)
	}
	if !strings.Contains(m.status, "max_running") || !strings.Contains(m.status, "applies now") {
		t.Fatalf("status=%q", m.status)
	}
	api.err = errors.New("limits_set: max_running is a whole number from 0 to 100")
	m, _ = step(t, m, key("enter"))
	m = typeIn(t, m, "9")
	m, _ = step(t, m, key("enter"))
	if !m.setEditing || !strings.Contains(m.status, "whole number") || len(st.sets) != 0 {
		t.Fatalf("refused: editing=%v status=%q sets=%q", m.setEditing, m.status, st.sets)
	}
}
