package launcher

import (
	"os"
	"strings"
	"testing"
)

// stubDeckTerminal makes the launcher see a terminal (or not) for the deck question.
func stubDeckTerminal(t *testing.T, tty bool) {
	t.Helper()
	old := deckTerminal
	deckTerminal = func(*Launcher) bool { return tty }
	t.Cleanup(func() { deckTerminal = old })
}

// leaShLogsDeckAsk is a stand-in lea-sh that logs its call and the LEA_DECK_ASK it was given.
const leaShLogsDeckAsk = leaShLog + "printf 'ask=%s\\n' \"${LEA_DECK_ASK:-}\" >>\"$FAKE_LOG\"\n"

// The first run asks the deck question (#335) only when no LEA_DECK is configured: lea init
// --first-run gets LEA_DECK_ASK=1, and asks it on a terminal. A LEA_DECK in the user config or
// the environment is an answer already.
func TestFirstRunAsksTheDeckQuestionWhenUnset(t *testing.T) {
	for _, c := range []struct {
		name, conf, env, want string
	}{
		{"unset", "LEA_HUB=0\n", "", "ask=1"},
		{"user config", "LEA_HUB=0\nLEA_DECK=0\n", "", "ask="},
		{"environment", "LEA_HUB=0\n", "1", "ask="},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubDeckTerminal(t, false)
			g, _ := runGoSide(t, copyRoot(t), commandScenario{unset: []string{"LEA_PROJECTS_DIR"}, setup: func(w *world) {
				writeUserConf(t, w, c.conf)
				if c.env != "" {
					w.env["LEA_DECK"] = c.env
				}
				fakeLeaSh(w, initSetsProjects(w)+"printf 'ask=%s\\n' \"${LEA_DECK_ASK:-}\" >>\"$FAKE_LOG\"\n")
			}})
			if !strings.Contains(g.calls, "lea-sh [init] [--first-run]\n"+c.want+"\n") {
				t.Fatalf("calls:\n%s\n%s", g.calls, g.lea)
			}
		})
	}
}

// deckAnswer is a stand-in lea-sh whose deck-question records answer (a user config line).
func deckAnswer(answer string) string {
	return leaShLogsDeckAsk + "[ \"$1\" = deck-question ] && printf '%s\\n' '" + answer + "' >>\"$XDG_CONFIG_HOME/lea/lea.conf\"\nexit 0\n"
}

// An install made before the deck (no LEA_DECK in its user config) gets the question once, at
// its next lea on a terminal (#335): the answer applies at once, and the next lea asks nothing.
func TestUpgradeAsksTheDeckQuestionOnce(t *testing.T) {
	root := copyRoot(t)
	stubDeckTerminal(t, true)
	// Arguments after lea: the deck would refuse them, the tool chosen gets them.
	w, args, cwd := commandSetup(t, root, commandScenario{args: []string{"--version"}, setup: func(w *world) {
		fakeLeaSh(w, deckAnswer("LEA_DECK='0'"))
		// The fake daemon already runs as the answer wants it (a real one restarts).
		if err := os.WriteFile(w.hostDir()+"/daemon.deck", []byte("off"), 0o600); err != nil {
			t.Fatal(err)
		}
	}})
	l := w.launcher()
	l.Cwd = cwd
	if st := l.Main(args); st != 0 {
		t.Fatalf("status %d\n%s", st, w.errb.String())
	}
	calls := strings.Join(w.calls(), "\n")
	if !strings.Contains(calls, "lea-sh [deck-question]\nask=1") {
		t.Fatalf("no deck question:\n%s", calls)
	}
	// Answered no: the tool runs in the container, no deck opens.
	if strings.Contains(calls, "core [deck-open]") || !strings.Contains(calls, "[lea-session] [--claude] [--version]") {
		t.Fatalf("the answer did not apply:\n%s", calls)
	}
	n := len(w.calls())
	l = w.launcher()
	l.Cwd = cwd
	if st := l.Main(args); st != 0 {
		t.Fatalf("second lea: status %d\n%s", st, w.errb.String())
	}
	if again := strings.Join(w.calls()[n:], "\n"); strings.Contains(again, "deck-question") {
		t.Fatalf("asked twice:\n%s", again)
	}
}

// No terminal, or a LEA_DECK already configured: no question, the deck as before.
func TestNoDeckQuestionWithoutTerminalOrAnswer(t *testing.T) {
	for _, c := range []struct {
		name, conf string
		tty        bool
	}{
		{"no terminal", "LEA_HUB=0\n", false},
		{"answered", "LEA_HUB=0\nLEA_DECK=1\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubDeckTerminal(t, c.tty)
			g, _ := runGoSide(t, copyRoot(t), commandScenario{setup: func(w *world) {
				writeUserConf(t, w, c.conf)
				fakeLeaSh(w, deckAnswer("LEA_DECK='0'"))
			}})
			if strings.Contains(g.calls, "deck-question") || !strings.Contains(g.calls, "core [deck-open]") {
				t.Fatalf("calls:\n%s", g.calls)
			}
		})
	}
}
