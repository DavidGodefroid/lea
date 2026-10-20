package deck

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DavidGodefroid/lea/internal/settings"
)

// fakeSettings keeps the settings in memory; Set refuses the values in bad.
type fakeSettings struct {
	list []settings.Setting
	sets []string
	bad  map[string]bool
	// normalize maps a value set to the one read back.
	normalize map[string]string
}

func (f *fakeSettings) List() ([]settings.Setting, error) {
	return append([]settings.Setting(nil), f.list...), nil
}

func (f *fakeSettings) Set(key, value string) error {
	if f.bad[value] {
		return errors.New("not a good value: " + value)
	}
	f.sets = append(f.sets, key+"="+value)
	for i := range f.list {
		if f.list[i].Key == key {
			f.list[i].Value, f.list[i].Source = value, settings.SourceFile
			if n, ok := f.normalize[value]; ok {
				f.list[i].Value = n
			}
		}
	}
	return nil
}

func someSettings() *fakeSettings {
	return &fakeSettings{list: []settings.Setting{
		{Key: "LEA_APPROVALS", Group: "Agents", Name: "Approvals", Help: "who answers permission requests",
			Kind: settings.Choice, Choices: []string{"deck", "pane"}, Value: "deck", Source: settings.SourceDefault, Apply: settings.ApplyAfterStop},
		{Key: "LEA_APPROVAL_TIMEOUT", Group: "Agents", Name: "Approval timeout", Help: "seconds",
			Kind: settings.Text, Value: "5", Source: settings.SourceEnvironment, Apply: settings.ApplyAfterStop,
			Locked: "set by the environment, which wins over the files"},
		{Key: "LEA_SOUND", Group: "Sounds", Name: "Sounds", Help: "a sound when an agent waits for you",
			Kind: settings.Toggle, Value: "1", Source: settings.SourceFile, Apply: settings.ApplyNow, Live: true},
		{Key: "LEA_SOUND_DONE", Group: "Sounds", Name: "Done sound", Help: "sound file when a turn ends",
			Kind: settings.Text, Value: "", Source: settings.SourceDefault, Apply: settings.ApplyAfterStop},
	}}
}

func withSettings(t *testing.T, api *fakeAPI, st *fakeSettings) Model {
	t.Helper()
	m, _ := step(t, newModel(api).WithSettings(st, ""), snapshotMsg{snap: Snapshot{Version: 7, Shown: "lea-a/a",
		Agents: []Agent{ag("a", "idle", 1)}, Sound: "on"}})
	return sized(m, 34, 30)
}

func typeIn(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// , opens the settings screen: grouped, each with its value; the selected one says what it does,
// where its value comes from and when a change applies. Every line keeps the deck's width.
func TestCommaOpensTheSettings(t *testing.T) {
	m := withSettings(t, &fakeAPI{}, someSettings())
	m, _ = step(t, m, key(","))
	if !m.setOpen {
		t.Fatal(", did not open the settings")
	}
	ls := viewLines(m)
	view := strings.Join(ls, "\n")
	for _, want := range []string{"Settings", "Agents", "Approvals", "deck", "Sounds", "on", "Done sound",
		"who answers permission", "from: default", "applies after lea stop --all", "5 (env)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "idle") {
		t.Errorf("the agent rows still show:\n%s", view)
	}
	for i, l := range ls {
		if w := utf8.RuneCountInString(l); w != 34 {
			t.Fatalf("line %d is %d wide: %q", i, w, l)
		}
	}
	// j moves to the next setting: its help shows instead.
	m, _ = step(t, m, key("j"))
	if view := strings.Join(viewLines(m), "\n"); !strings.Contains(view, "from: environment") || !strings.Contains(view, "read-only") || strings.Contains(view, "who answers") {
		t.Errorf("after j:\n%s", view)
	}
	// esc closes it; the rows are back.
	m, _ = step(t, m, key("esc"))
	if m.setOpen || !strings.Contains(strings.Join(viewLines(m), "\n"), "idle") {
		t.Fatalf("esc did not close:\n%s", strings.Join(viewLines(m), "\n"))
	}
}

// While the screen is open, the deck's keys do nothing to the agents.
func TestTheSettingsKeepTheKeys(t *testing.T) {
	api := &fakeAPI{}
	m := withSettings(t, api, someSettings())
	m, _ = step(t, m, key(","))
	for _, k := range []string{"x", "n", "q", "S"} {
		var cmd tea.Cmd
		m, cmd = step(t, m, key(k))
		if k == "q" {
			if m.setOpen {
				t.Fatal("q did not close the settings")
			}
			m, _ = step(t, m, key(","))
			continue
		}
		if cmd != nil || m.confirm != "" {
			t.Fatalf("%s reached the deck: confirm %q", k, m.confirm)
		}
	}
	if got := api.got(); len(got) != 0 {
		t.Fatalf("calls %q", got)
	}
}

// enter on a choice picks the next one and saves it; the status says when it applies.
func TestAChoiceCycles(t *testing.T) {
	st := someSettings()
	m := withSettings(t, &fakeAPI{}, st)
	m, _ = step(t, m, key(","))
	m, _ = step(t, m, key("enter"))
	if len(st.sets) != 1 || st.sets[0] != "LEA_APPROVALS=pane" {
		t.Fatalf("sets %q", st.sets)
	}
	if !strings.Contains(m.status, "pane") || !strings.Contains(m.status, "after lea stop --all") {
		t.Fatalf("status %q", m.status)
	}
	if view := strings.Join(viewLines(m), "\n"); !strings.Contains(view, "from: your file") {
		t.Errorf("the source did not follow:\n%s", view)
	}
}

// A live setting applies at once: the sounds switch in the daemon too.
func TestTheSoundsApplyLive(t *testing.T) {
	api, st := &fakeAPI{}, someSettings()
	m := withSettings(t, api, st)
	m, _ = step(t, m, key(","))
	m, _ = step(t, m, key("j"))
	m, _ = step(t, m, key("j"))
	m, cmd := step(t, m, key(" "))
	if len(st.sets) != 1 || st.sets[0] != "LEA_SOUND=0" || cmd == nil {
		t.Fatalf("sets %q cmd %v", st.sets, cmd != nil)
	}
	m, _ = step(t, m, cmd())
	if got := api.got(); len(got) != 1 || got[0] != "sound false" || m.sound != "off" {
		t.Fatalf("calls %q sound %q", got, m.sound)
	}
	if !m.setOpen {
		t.Fatal("the screen closed")
	}
}

// A read-only setting is never written: the status gives the reason.
func TestALockedSettingIsReadOnly(t *testing.T) {
	st := someSettings()
	m := withSettings(t, &fakeAPI{}, st)
	m, _ = step(t, m, key(","))
	m, _ = step(t, m, key("j"))
	m, _ = step(t, m, key("enter"))
	if len(st.sets) != 0 || m.setEditing || !strings.Contains(m.status, "environment") {
		t.Fatalf("sets %q editing %v status %q", st.sets, m.setEditing, m.status)
	}
}

// A text setting opens a field: enter saves, a refused value keeps the field open with the
// reason, esc cancels.
func TestATextSettingIsEditedInPlace(t *testing.T) {
	st := someSettings()
	st.bad = map[string]bool{"/bad": true}
	m := withSettings(t, &fakeAPI{}, st)
	m, _ = step(t, m, key(","))
	m, _ = step(t, m, key("down"))
	m, _ = step(t, m, key("down"))
	m, _ = step(t, m, key("down"))
	m, _ = step(t, m, key("enter"))
	if !m.setEditing {
		t.Fatal("enter did not open the field")
	}
	m = typeIn(t, m, "/bad")
	if last := viewLines(m)[len(viewLines(m))-2]; !strings.Contains(last, "value: /bad") {
		t.Fatalf("field %q", last)
	}
	m, _ = step(t, m, key("enter"))
	if !m.setEditing || !strings.Contains(m.status, "not a good value") || len(st.sets) != 0 {
		t.Fatalf("editing %v status %q sets %q", m.setEditing, m.status, st.sets)
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeIn(t, m, "/ok.oga")
	m, _ = step(t, m, key("enter"))
	if m.setEditing || len(st.sets) != 1 || st.sets[0] != "LEA_SOUND_DONE=/ok.oga" {
		t.Fatalf("editing %v sets %q", m.setEditing, st.sets)
	}
	m, _ = step(t, m, key("enter"))
	m = typeIn(t, m, "zz")
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.setEditing || !m.setOpen || len(st.sets) != 1 {
		t.Fatalf("esc: editing %v open %v sets %q", m.setEditing, m.setOpen, st.sets)
	}
}

// Without a store (inside lea, or no lea installation found) , says why and opens nothing.
func TestNoSettingsWithoutAStore(t *testing.T) {
	m := loaded(t, &fakeAPI{}, "lea-a/a", ag("a", "idle", 1))
	m, _ = step(t, m, key(","))
	if m.setOpen || !strings.Contains(m.status, "settings") {
		t.Fatalf("open %v status %q", m.setOpen, m.status)
	}
	m, _ = step(t, newModel(&fakeAPI{}).WithSettings(nil, "host only"), key(","))
	if m.setOpen || !strings.Contains(m.status, "host only") {
		t.Fatalf("open %v status %q", m.setOpen, m.status)
	}
}

// The row menu and the key list offer the settings.
func TestTheMenuAndTheKeyListOfferTheSettings(t *testing.T) {
	found := false
	for _, it := range menuItems(ag("a", "idle", 1)) {
		found = found || (it.label == "Settings" && it.key == ",")
	}
	if !found {
		t.Error("no Settings in the menu")
	}
	if !strings.Contains(strings.Join(helpLines, "\n"), "settings") {
		t.Error("no settings in the key list")
	}
	m := withSettings(t, &fakeAPI{}, someSettings())
	m.openMenu("lea-a/a")
	items := menuItems(m.agents[0])
	for i, it := range items {
		if it.key == "," {
			nm, _ := m.pick(i)
			if !nm.(Model).setOpen {
				t.Fatal("the menu's Settings did not open the screen")
			}
		}
	}
}

// The status after a save shows the value read back, not the one typed.
func TestTheStatusShowsTheValueReadBack(t *testing.T) {
	st := someSettings()
	st.normalize = map[string]string{"/ok.oga": "/read-back.oga"}
	m := withSettings(t, &fakeAPI{}, st)
	m, _ = step(t, m, key(","))
	for i := 0; i < 3; i++ {
		m, _ = step(t, m, key("down"))
	}
	m, _ = step(t, m, key("enter"))
	m = typeIn(t, m, "/ok.oga")
	m, _ = step(t, m, key("enter"))
	if !strings.Contains(m.status, "/read-back.oga") {
		t.Fatalf("status %q", m.status)
	}
}
