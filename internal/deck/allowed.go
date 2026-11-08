package deck

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/DavidGodefroid/lea/internal/grants"
)

// The Allowed screen (g, #319): what agents of this lea may do without asking (lea allow), one
// row per allowance with its capabilities, repositories and pins, time left and last use. j/k
// move, x revokes the selected allowance after the usual confirmation, g, esc or q close it. It
// reads and revokes through the host socket's allow_list and allow_off, which no container
// reaches; it records nothing.

// Allowance is one row of the screen.
type Allowance struct {
	ID int64
	// Allows is the capabilities ("deliver" for the preset), Where the repositories and pins,
	// the branch glob and the agent; Until "revoked" or the time left; Last the last use.
	Allows, Where, Until, Last string
}

// AllowAPI is the part of the API the screen uses (Client has it).
type AllowAPI interface {
	Allowances(ctx context.Context, container string) ([]Allowance, error)
	Revoke(ctx context.Context, container string, id int64) error
}

// Allowances is allow_list's sets of container, as rows.
func (c Client) Allowances(ctx context.Context, container string) ([]Allowance, error) {
	var v struct {
		Sets    []grants.Set
		LastUse map[string]int64 `json:"last_use"`
		Now     int64
	}
	if err := c.call(ctx, "allow_list", map[string]any{"container": container}, &v); err != nil {
		return nil, err
	}
	return allowanceRows(v.Sets, v.LastUse, v.Now), nil
}

// Revoke is lea allow off N.
func (c Client) Revoke(ctx context.Context, container string, id int64) error {
	return c.call(ctx, "allow_off", map[string]any{"container": container, "set": id}, nil)
}

// allowanceRows are the screen's rows of sets at now.
func allowanceRows(sets []grants.Set, last map[string]int64, now int64) []Allowance {
	out := []Allowance{}
	for _, s := range sets {
		var where []string
		for _, r := range s.Repos(now) {
			where = append(where, r[0]+" ("+r[1]+")")
		}
		for _, g := range s.Grants {
			if !g.Active(now) {
				continue
			}
			if g.Branch != "" && g.Branch != "*" {
				where = append(where, "branches "+g.Branch)
			}
			if g.Agent != "" {
				where = append(where, "for "+g.Agent+" and its children")
			}
			break
		}
		if len(where) == 0 {
			where = []string{"every agent"}
		}
		until := "until revoked"
		if !s.UntilRevoked() {
			until = (time.Duration(s.Expires-now) * time.Second).Round(time.Minute).String() + " left"
		}
		used := "never used"
		if at := last[strconv.FormatInt(s.ID, 10)]; at > 0 {
			used = "used " + time.Unix(at, 0).Format("2006-01-02 15:04")
		}
		out = append(out, Allowance{ID: s.ID, Allows: grants.Short(s.ActiveCapabilities(now)), Where: strings.Join(where, ", "), Until: until, Last: used})
	}
	return out
}

// allowedMsg is the screen's rows, read again.
type allowedMsg struct {
	rows []Allowance
	err  error
}

// allowPrefix marks the confirmation modal's key as an allowance's (m.confirm).
const allowPrefix = "allow:"

func isAllowKey(k string) bool { return strings.HasPrefix(k, allowPrefix) }

// allowKeyID is the allowance an allow key names, 0 when none.
func allowKeyID(k string) int64 {
	id, _ := strconv.ParseInt(strings.TrimPrefix(k, allowPrefix), 10, 64)
	return id
}

// hasAllowance says whether the screen lists allowance id.
func (m Model) hasAllowance(id int64) bool {
	for _, a := range m.allowList {
		if a.ID == id {
			return true
		}
	}
	return false
}

// readAllowed reads the rows again.
func (m Model) readAllowed() tea.Cmd {
	api, ok := m.api.(AllowAPI)
	if !ok {
		return nil
	}
	c := m.container
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		rows, err := api.Allowances(ctx, c)
		return allowedMsg{rows, err}
	}
}

// openAllowed shows the screen and reads its rows.
func (m Model) openAllowed() (tea.Model, tea.Cmd) {
	if _, ok := m.api.(AllowAPI); !ok {
		m.status = "no allowances here"
		return m, nil
	}
	m.allowOpen, m.allowSel, m.status = true, 0, ""
	m.menuFor, m.confirm, m.roleFor, m.editFor, m.hoverFor = "", "", "", "", ""
	return m, m.readAllowed()
}

// allowedKey is a key typed while the screen is open (the modal aside).
func (m Model) allowedKey(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "j", "down":
		m.allowSel = min(m.allowSel+1, max(0, len(m.allowList)-1))
	case "k", "up":
		m.allowSel = max(m.allowSel-1, 0)
	case "g", "esc", "q", "ctrl+c":
		m.allowOpen, m.allowList, m.status = false, nil, ""
	case "x":
		if m.allowSel < len(m.allowList) {
			m.confirm, m.confirmYes, m.status = allowPrefix+strconv.FormatInt(m.allowList[m.allowSel].ID, 10), false, ""
		}
	}
	return m, nil
}

// revokeAllowed revokes allowance id, then reads the rows again.
func (m Model) revokeAllowed(id int64) tea.Cmd {
	api, ok := m.api.(AllowAPI)
	if !ok {
		return nil
	}
	c := m.container
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		if err := api.Revoke(ctx, c, id); err != nil {
			return allowedMsg{err: err}
		}
		rows, err := api.Allowances(ctx, c)
		return allowedMsg{rows, err}
	}
}

// takeAllowed takes the rows read in.
func (m Model) takeAllowed(msg allowedMsg) Model {
	if msg.err != nil {
		m.status = oneLine(msg.err.Error())
		return m
	}
	if !m.allowOpen {
		return m
	}
	m.allowList = msg.rows
	m.allowSel = min(m.allowSel, max(0, len(m.allowList)-1))
	if isAllowKey(m.confirm) && !m.hasAllowance(allowKeyID(m.confirm)) {
		m.confirm, m.confirmYes = "", false
	}
	return m
}

// allowedLines are the screen's lines, inner cells wide, room lines at most (0: no limit).
func (m Model) allowedLines(inner, room int) []string {
	c := m.cs()
	title := padRight(" "+lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render("Allowed"), inner-11) +
		muted.Render(padLeft("esc closes ", 11))
	var lines []string
	if len(m.allowList) == 0 {
		for _, l := range wrapWords("Nothing: agents ask as usual. On the host, lea allow deliver lets them push, open and merge their own PRs.", inner-2, 4) {
			lines = append(lines, padRight(" "+muted.Render(l), inner))
		}
	}
	selTop, selEnd := 0, 0
	for i, a := range m.allowList {
		id := fmt.Sprintf("#%d ", a.ID)
		right := safe(a.Until, c)
		name := fit(safe(id+a.Allows, c), inner-3-lipgloss.Width(right)-1, c)
		row := "  " + padRight(name, inner-3-lipgloss.Width(right)) + right + " "
		if i != m.allowSel {
			lines = append(lines, plain.Render(row))
		} else {
			selTop = len(lines)
			row = c.mark + " " + row[2:]
			lines = append(lines, lipgloss.NewStyle().Foreground(theme.Accent).Background(theme.Sel).Bold(true).Render(row))
		}
		for _, l := range wrapWords(safe(a.Where, c), inner-5, 3) {
			lines = append(lines, padRight("    "+muted.Render(l), inner))
		}
		lines = append(lines, padRight("    "+muted.Render(fit(safe(a.Last, c), inner-4, c)), inner))
		if i == m.allowSel {
			selEnd = len(lines)
		}
	}
	if len(m.allowList) > 0 {
		lines = append(lines, padRight(" "+muted.Render(fit("x revokes the selected one", inner-1, c)), inner))
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
