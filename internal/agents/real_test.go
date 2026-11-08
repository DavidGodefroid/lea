package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/tmux"
)

// TestRealLayoutRepair runs the layout repair against a real tmux server (make go-test-tmux):
// the user's broken session, a pane split in the deck window, the deck swapped to the right and
// the deck stacked over the slot are all put back.
func TestRealLayoutRepair(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	dir, err := os.MkdirTemp("", "lt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf, ""); err != nil {
		t.Fatal(err)
	}
	// The deck and the placeholder run this stand-in for lea.
	bin := filepath.Join(dir, "lea")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const sock = "lea-test-agents"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := NewManager(Config{Store: st, Term: Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: &fakeContainers{live: map[string][]string{}}, LeaBin: bin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})

	// What the user's session was left as: one untagged pane running the placeholder.
	tm("new-session", "-d", "-s", C, "-n", "deck", "-x", "200", "-y", "50", bin, "deck", "--placeholder")
	check := func(what string) {
		t.Helper()
		if err := m.Ensure(bg, C, ""); err != nil {
			t.Fatalf("%s: Ensure: %v", what, err)
		}
		lines := strings.Split(tm("list-panes", "-s", "-t", "="+C, "-F",
			"#{@lea_role}|#{@lea_agent}|#{@lea_deck}|#{pane_at_left}|#{pane_at_top}|#{pane_width}|#{window_id}"), "\n")
		inDeck, deckWins := 0, map[string]bool{}
		for _, l := range lines {
			f := strings.Split(l, "|")
			if f[0] == "" && f[1] == "" {
				t.Errorf("%s: an untagged pane is left: %q", what, lines)
			}
			if f[2] == "1" {
				inDeck++
				deckWins[f[6]] = true
			}
			if f[0] == roleDeck && (f[2] != "1" || f[3] != "1" || f[4] != "1" || f[5] != "34") {
				t.Errorf("%s: deck pane %q", what, l)
			}
			if f[0] == rolePlace && f[2] == "1" && f[3] == "1" {
				t.Errorf("%s: the slot touches the left edge: %q", what, lines)
			}
		}
		if inDeck != 2 || len(deckWins) != 1 {
			t.Errorf("%s: %d panes in %d windows marked deck: %q", what, inDeck, len(deckWins), lines)
		}
		// The repair settles: its own notifications bring no further change.
		before := tm("list-panes", "-s", "-t", "="+C, "-F", "#{pane_id}|#{window_id}|#{pane_at_left}|#{pane_width}|#{@lea_deck}")
		if err := m.Ensure(bg, C, ""); err != nil {
			t.Fatalf("%s: second Ensure: %v", what, err)
		}
		if after := tm("list-panes", "-s", "-t", "="+C, "-F", "#{pane_id}|#{window_id}|#{pane_at_left}|#{pane_width}|#{@lea_deck}"); after != before {
			t.Errorf("%s: a second Ensure changed the session:\n%s\n->\n%s", what, before, after)
		}
	}
	check("the user's session")

	tm("split-window", "-h", "-t", "="+C+":deck", "sleep", "600")
	check("a pane split in the deck window")

	deckPane := tm("display-message", "-p", "-t", "="+C+":deck.0", "#{pane_id}")
	slotPane := tm("display-message", "-p", "-t", "="+C+":deck.1", "#{pane_id}")
	if tm("display-message", "-p", "-t", deckPane, "#{@lea_role}") != roleDeck {
		t.Fatal("the window named deck is not the deck's: the test proves nothing")
	}
	tm("swap-pane", "-d", "-s", deckPane, "-t", slotPane)
	check("the deck swapped to the right")

	tm("break-pane", "-d", "-s", slotPane, "-n", "placeholder")
	tm("join-pane", "-v", "-d", "-s", slotPane, "-t", deckPane)
	check("the deck stacked over the slot")
}

// TestRealShellExitClosesItsPane opens a shell against a real tmux server and ends it while it is
// in the slot: its pane closes (never left dead), the subscription brings the reconcile, the
// placeholder takes the slot and the shell's row goes.
func TestRealShellExitClosesItsPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	old := debounce
	debounce = 20 * time.Millisecond
	t.Cleanup(func() { debounce = old })
	dir, err := os.MkdirTemp("", "ls")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf, ""); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lea")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The shell: docker exec ... stands in as a process that runs until it is killed.
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const sock = "lea-test-shells"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := NewManager(Config{Store: st, Term: Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: &fakeContainers{live: map[string][]string{}}, LeaBin: bin, Docker: docker, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	if err := m.Ensure(bg, C, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenShell(bg, C); err != nil {
		t.Fatal(err)
	}
	if got := tm("display-message", "-p", "-t", "="+C+":deck.1", "#{@lea_shell}|#{remain-on-exit}"); got != "sh-1|off" {
		t.Fatalf("slot=%q", got)
	}
	// tmux checks subscriptions once a second: let the notifications of the new pane pass, so
	// only the pane's closing can bring the reconcile below.
	time.Sleep(1500 * time.Millisecond)
	pid := tm("display-message", "-p", "-t", "="+C+":deck.1", "#{pane_pid}")
	if err := exec.Command("kill", pid).Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _ := m.Watch(bg, C, 0, 0)
		panes := tm("list-panes", "-s", "-t", "="+C, "-F", "#{@lea_role}|#{@lea_shell}|#{pane_dead}|#{window_name}")
		if len(snap.Agents) == 0 && snap.Shown == "" && !strings.Contains(panes, "sh-1") &&
			strings.Contains(panes, rolePlace+"||0|deck") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the shell exited: snap=%+v panes=%q", snap, panes)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRealRepairSettles counts the reconciles that follow a repair on a real tmux server: the
// notifications of the repair's own commands (window closes, pane counts) must not bring an
// endless chain of repairs, as an agent named deck once did by being broken out again and again.
func TestRealRepairSettles(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	old := debounce
	debounce = 20 * time.Millisecond
	t.Cleanup(func() { debounce = old })
	dir, err := os.MkdirTemp("", "lr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf, ""); err != nil {
		t.Fatal(err)
	}
	stub := []byte("#!/bin/sh\nexec sleep 600\n")
	bin, docker := filepath.Join(dir, "lea"), filepath.Join(dir, "docker")
	for _, p := range []string{bin, docker} {
		if err := os.WriteFile(p, stub, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const sock = "lea-test-settle"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctr := &fakeContainers{live: map[string][]string{}}
	m := NewManager(Config{Store: st, Term: Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: ctr, LeaBin: bin, Docker: docker, UID: 1000, GID: 1000, Now: func() int64 { return 1000 }})
	if err := m.Ensure(bg, C, "/"); err != nil {
		t.Fatal(err)
	}
	// An agent registered before names were reserved: its parking window is named deck.
	st.RegisterAgent(C, "deck", "claude", "/", 900)
	ctr.setLive(C, "deck")
	if err := m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	// tmux checks subscriptions once a second: let the repair's own notifications pass.
	time.Sleep(2 * time.Second)
	n := ctr.calls()
	time.Sleep(2500 * time.Millisecond)
	if got := ctr.calls(); got != n {
		t.Fatalf("%d reconciles in 2.5 s with nothing changing: the repair does not settle", got-n)
	}
}

// TestRealRetiredPanesGo runs against a real tmux server (#300): the panes an earlier lea made for
// its lock consoles and its hub ticket (roles lock and ticket) go at the next layout repair, the
// deck and its slot stay.
func TestRealRetiredPanesGo(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	dir, err := os.MkdirTemp("", "lt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf, ""); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lea")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const sock = "lea-test-ticket"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := NewManager(Config{Store: st, Term: Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: &fakeContainers{live: map[string][]string{}}, LeaBin: bin, UID: 1000, GID: 1000,
		Now: func() int64 { return 1000 }})
	if err := m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"lock", retiredTicket} {
		id := tm("new-window", "-d", "-t", "="+C+":", "-n", role, "-P", "-F", "#{pane_id}", "sleep", "600")
		tm("set-option", "-p", "-t", id, optRole, role)
	}
	if err := m.Ensure(bg, C, ""); err != nil {
		t.Fatal(err)
	}
	got := tm("list-panes", "-s", "-t", "="+C, "-F", "#{@lea_role}")
	for _, role := range []string{"lock", retiredTicket} {
		for _, l := range strings.Split(got, "\n") {
			if l == role {
				t.Fatalf("the %s pane of an earlier lea stays:\n%s", role, got)
			}
		}
	}
	if !strings.Contains(got, roleDeck) || !strings.Contains(got, rolePlace) {
		t.Fatalf("the deck or its placeholder went:\n%s", got)
	}
}

// TestRealLeadsChildRunsInItsLeadsDirectory runs against a real tmux server (#364): after the
// user runs lea again from another repo, a lead's child started without a repo runs where its
// lead works (pane_current_path), not where lea was last launched from.
func TestRealLeadsChildRunsInItsLeadsDirectory(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed: make go-test-tmux runs this test")
	}
	dir, err := os.MkdirTemp("", "lw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	conf := filepath.Join(dir, "tmux.conf")
	if err := tmux.WriteConf(conf, ""); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "lea")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	projects, ran := filepath.Join(real, "projects"), filepath.Join(real, "ran")
	if err := os.Mkdir(ran, 0o755); err != nil {
		t.Fatal(err)
	}
	// docker exec -w DIR ... stands in as a process that runs in DIR until it is killed, and
	// records DIR under the agent's name (tmux cannot always read a pane's cwd in a container).
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nfor a; do case $a in LEA_AGENT=*) name=${a#LEA_AGENT=};; esac; done\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = -w ]; then cd \"$2\" || exit 1; fi; shift; done\n" +
		"pwd -P >" + ran + "/\"$name\"\nexec sleep 600\n"
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"lea", "shop"} {
		if err := os.MkdirAll(filepath.Join(projects, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const sock = "lea-test-workdir"
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	tm := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", sock, "-f", conf}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	st, err := store.Open(filepath.Join(dir, "lea.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctr := &fakeContainers{live: map[string][]string{}, projects: map[string]string{C: projects}}
	m := NewManager(Config{Store: st, Term: Tmux(tmux.Server{Socket: sock, Conf: conf}),
		Containers: ctr, LeaBin: bin, Docker: docker, UID: 1000, GID: 1000, Now: func() int64 { return 1000 }})
	if err := m.Ensure(bg, C, projects); err != nil {
		t.Fatal(err)
	}
	tower, err := m.New(bg, NewRequest{Container: C, Name: "tower", Role: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	boss, err := m.New(bg, NewRequest{Container: C, Name: "boss", Role: "lead", Repo: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	ctr.setLive(C, "tower", "boss")
	// The user runs lea again from the lea checkout.
	if err := m.Ensure(bg, C, filepath.Join(projects, "lea")); err != nil {
		t.Fatal(err)
	}
	cwd := func(name string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if b, err := os.ReadFile(filepath.Join(ran, name)); err == nil && len(b) > 0 {
				got := strings.TrimSpace(string(b))
				// Where tmux can read the pane's cwd, it must agree.
				panes := tm("list-panes", "-s", "-t", "="+C, "-F", "#{@lea_agent}|#{pane_current_path}")
				for _, l := range strings.Split(panes, "\n") {
					if p, ok := strings.CutPrefix(l, name+"|"); ok && p != "" && p != got {
						t.Fatalf("%s: pane_current_path %q, the process runs in %q", name, p, got)
					}
				}
				return got
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never started", name)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, c := range []struct {
		lead, want string
	}{{tower.Key, projects}, {boss.Key, projects}} {
		kid, err := m.New(bg, NewRequest{Container: C, Role: "dev", Parent: c.lead, NamePrefix: "dev", Hidden: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := cwd(kid.Name); got != c.want {
			t.Fatalf("%s's child runs in %q, want %q", c.lead, got, c.want)
		}
	}
	// The repo lead's child is told the lead's repo, not the launch directory's.
	if a, _ := st.AgentByKey(C + "/dev-2"); a.Repo != "shop" {
		t.Fatalf("repo lead's child=%+v", a)
	}
	if solo, err := m.New(bg, NewRequest{Container: C, Name: "solo"}); err != nil {
		t.Fatal(err)
	} else if got := cwd(solo.Name); got != filepath.Join(projects, "lea") {
		t.Fatalf("parentless agent runs in %q", got)
	}
}
