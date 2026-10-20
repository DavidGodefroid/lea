package deck

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DavidGodefroid/lea/internal/settings"
	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// The settings screen (,): lea's configuration, grouped, each key with its value. j/k move,
// enter or space changes the selected one (a toggle flips, a choice takes the next one, a text
// opens a field), esc, q or , close it. A change is written to the user configuration at once and
// applied live when the setting allows it (the sounds); the status says when it applies
// otherwise. The deck reads and writes the file itself, on the host: no daemon method reaches
// the settings, so an agent can neither open nor drive this screen.

// SettingsStore is where the screen reads and writes the settings (settings.Store).
type SettingsStore interface {
	List() ([]settings.Setting, error)
	Set(key, value string) error
}

// maxSetting is the longest value the field takes.
const maxSetting = 256

// WithSettings gives the deck its settings; a nil store leaves , saying why (off).
func (m Model) WithSettings(st SettingsStore, off string) Model {
	m.settings, m.settingsOff = st, off
	return m
}

// openSettings reads the settings and shows the screen on its first one.
func (m Model) openSettings() (tea.Model, tea.Cmd) {
	if m.settings == nil {
		m.status = "no settings here"
		if m.settingsOff != "" {
			m.status = "settings: " + m.settingsOff
		}
		return m, nil
	}
	list, err := m.settings.List()
	if err != nil {
		m.status = oneLine(err.Error())
		return m, nil
	}
	m.setOpen, m.setList, m.setSel, m.setEditing, m.status = true, list, 0, false, ""
	m.menuFor, m.confirm, m.roleFor, m.editFor = "", "", "", ""
	return m, nil
}

// settingsKey is a key typed while the screen is open.
func (m Model) settingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.setEditing {
		return m.settingFieldKey(k)
	}
	switch k.String() {
	case "j", "down":
		m.setSel = min(m.setSel+1, len(m.setList)-1)
	case "k", "up":
		m.setSel = max(m.setSel-1, 0)
	case "esc", "q", ",", "ctrl+c":
		m.setOpen, m.setList, m.status = false, nil, ""
	case "enter", " ":
		if m.setSel >= len(m.setList) {
			return m, nil
		}
		s := m.setList[m.setSel]
		switch {
		case s.Locked != "":
			m.status = s.Name + ": read-only, " + s.Locked
		case s.Kind == settings.Toggle:
			flip := "0"
			if s.Value == "0" {
				flip = "1"
			}
			return m.saveSetting(s, flip)
		case s.Kind == settings.Choice:
			next := s.Choices[0]
			for i, c := range s.Choices {
				if c == s.Value && i+1 < len(s.Choices) {
					next = s.Choices[i+1]
				}
			}
			return m.saveSetting(s, next)
		default:
			m.setEditing, m.setDraft, m.status = true, []rune(s.Value), ""
		}
	}
	return m, nil
}

// settingFieldKey is a key typed in a text setting's field: enter saves (a refused value keeps
// the field open, the status says why), esc cancels.
func (m Model) settingFieldKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s := k.String(); {
	case s == "esc" || s == "ctrl+c":
		m.setEditing, m.setDraft, m.status = false, nil, ""
	case s == "enter":
		return m.saveSetting(m.setList[m.setSel], string(m.setDraft))
	case s == "backspace":
		if len(m.setDraft) > 0 {
			m.setDraft = m.setDraft[:len(m.setDraft)-1]
		}
	case s == "ctrl+u":
		m.setDraft = nil
	case k.Type == tea.KeySpace:
		m.typeSetting(' ')
	case k.Type == tea.KeyRunes:
		for _, r := range k.Runes {
			m.typeSetting(r)
		}
	}
	return m, nil
}

func (m *Model) typeSetting(r rune) {
	if !textsafe.Unsafe(r) && len(m.setDraft) < maxSetting {
		m.setDraft = append(m.setDraft, r)
	}
}

// saveSetting writes value for s, reads the settings back and applies a live one.
func (m Model) saveSetting(s settings.Setting, value string) (tea.Model, tea.Cmd) {
	if err := m.settings.Set(s.Key, value); err != nil {
		m.status = oneLine(err.Error())
		return m, nil
	}
	m.setEditing, m.setDraft = false, nil
	s.Value = value
	if list, err := m.settings.List(); err == nil {
		m.setList = list
		// The status shows the value read back: what lea now uses, not what was typed.
		for _, r := range list {
			if r.Key == s.Key {
				s.Value = r.Value
			}
		}
	}
	m.status = s.Name + ": " + settingValue(s) + ", " + s.Apply
	if s.Live && s.Key == "LEA_SOUND" && m.sound != "" {
		api, on := m.api, value == "1"
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			defer cancel()
			st, err := api.SetSound(ctx, on)
			return soundMsg{st, err}
		}
	}
	return m, nil
}

// settingValue is how the screen shows a value.
func settingValue(s settings.Setting) string {
	switch {
	case s.Kind == settings.Toggle && s.Value == "0":
		return "off"
	case s.Kind == settings.Toggle:
		return "on"
	case s.Value == "":
		return "none"
	}
	return s.Value
}

// sourceText says where a value comes from.
var sourceText = map[string]string{settings.SourceDefault: "default", settings.SourceFile: "your file",
	settings.SourceEnvironment: "environment"}

// settingsLines are the screen's lines, inner cells wide, room lines at most (0: no limit): the
// title, then the groups, scrolled so that the selected setting and its details show.
func (m Model) settingsLines(inner, room int) []string {
	c := m.cs()
	title := padRight(" "+lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render("Settings"), inner-11) +
		muted.Render(padLeft("esc closes ", 11))
	var lines []string
	selTop, selEnd, group := 0, 0, ""
	for i, s := range m.setList {
		if s.Group != group {
			group = s.Group
			lines = append(lines, padRight(" "+lipgloss.NewStyle().Foreground(theme.Muted).Bold(true).Render(fit(safe(group, c), inner-1, c)), inner))
		}
		val := settingValue(s)
		if s.Locked != "" {
			val += " (env)"
		}
		val = tail(safe(val, c), min(16, inner/2))
		name := fit(safe(s.Name, c), inner-3-lipgloss.Width(val)-1, c)
		row := "  " + padRight(name, inner-3-lipgloss.Width(val)) + val + " "
		if i != m.setSel {
			lines = append(lines, plain.Render(row))
			continue
		}
		selTop = len(lines)
		row = c.mark + " " + row[2:]
		lines = append(lines, lipgloss.NewStyle().Foreground(theme.Accent).Background(theme.Sel).Bold(true).Render(row))
		for _, l := range wrapWords(safe(s.Help, c), inner-5, 3) {
			lines = append(lines, padRight("    "+muted.Render(l), inner))
		}
		apply := s.Apply
		if s.Locked != "" {
			apply = "read-only"
		}
		for _, l := range []string{"from: " + sourceText[s.Source], apply} {
			lines = append(lines, padRight("    "+muted.Render(fit(l, inner-4, c)), inner))
		}
		selEnd = len(lines)
	}
	if room > 1 && len(lines) > room-1 {
		off := 0
		if selEnd > room-1 {
			off = min(selEnd-(room-1), selTop)
		}
		lines = lines[off:min(len(lines), off+room-1)]
	}
	return append([]string{title}, lines...)
}

// wrapWords cuts s into at most n lines of w cells at spaces; the last one ends with … when cut.
func wrapWords(s string, w, n int) []string {
	var out []string
	words := strings.Fields(s)
	line := ""
	for i, word := range words {
		switch {
		case line == "":
			line = word
		case lipgloss.Width(line)+1+lipgloss.Width(word) <= w:
			line += " " + word
		default:
			if len(out) == n-1 {
				return append(out, head(line+" "+strings.Join(words[i:], " "), w))
			}
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, head(line, w))
	}
	return out
}
