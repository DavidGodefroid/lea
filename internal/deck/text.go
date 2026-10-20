package deck

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// A row's mark: a red ball when it needs you, a spinner while it works, a green ball when its
// turn is over, an empty ball once it has been quiet for restAfter, ? when unknown.
var (
	spinner      = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	asciiSpinner = []string{".", "o", "O", "o"} // a pulse: "|" would merge with the row bar
)

// restAfter is how long an agent whose turn is over stays green.
const restAfter = 30 * 60

// stateMark is a row's state mark and its colour at animation frame frame; now and updated are unix
// seconds.
func stateMark(state string, updated, now int64, frame int, ascii bool) (string, lipgloss.AdaptiveColor) {
	pick := func(u, a string) string {
		if ascii {
			return a
		}
		return u
	}
	switch state {
	case "blocked":
		return pick("●", "!"), theme.Blocked
	case "working", "waiting":
		s := spinner
		if ascii {
			s = asciiSpinner
		}
		return s[frame%len(s)], theme.Working
	case "limited":
		// Stopped on its usage limit: lea resumes it, or it waits for the user.
		return pick("◷", "~"), theme.Idle
	case "idle", "done":
		if now-updated >= restAfter {
			return pick("○", "o"), theme.Muted
		}
		return pick("●", "*"), theme.Done
	}
	return "?", theme.Unknown
}

// clean makes text safe on one terminal line: C0, DEL, C1 and invisible characters become '?'.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if textsafe.Unsafe(r) {
			return '?'
		}
		return r
	}, s)
}

// oneLine cleans s and cuts it to 200 characters.
func oneLine(s string) string {
	r := []rune(clean(s))
	if len(r) > 200 {
		r = append(r[:200], '…')
	}
	return string(r)
}

// cluster is one grapheme cluster and the cells it takes: what a terminal draws as one
// character (☺ + U+FE0F is one cluster two cells wide, e + U+0301 one cell).
type cluster struct {
	s string
	w int
}

// clusters splits s, one line of text, into grapheme clusters in one pass.
func clusters(s string) []cluster {
	var out []cluster
	state := -1
	for len(s) > 0 {
		var c string
		var w int
		c, s, w, state = uniseg.FirstGraphemeClusterInString(s, state)
		out = append(out, cluster{c, w})
	}
	return out
}

// head keeps the longest start of s at most w cells wide, never splitting a cluster.
func head(s string, w int) string {
	n, cw := 0, 0
	state := -1
	for rest := s; len(rest) > 0; {
		c, r, cwid, st := uniseg.FirstGraphemeClusterInString(rest, state)
		if cw+cwid > w {
			break
		}
		cw, n, rest, state = cw+cwid, n+len(c), r, st
	}
	return s[:n]
}

// tail keeps the longest end of s at most w cells wide (the end of a long draft line stays
// visible), never splitting a cluster.
func tail(s string, w int) string {
	cs := clusters(s)
	n, cw := 0, 0
	for i := len(cs) - 1; i >= 0 && cw+cs[i].w <= w; i-- {
		cw, n = cw+cs[i].w, n+len(cs[i].s)
	}
	return s[len(s)-n:]
}
