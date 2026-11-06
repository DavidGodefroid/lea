package launcher

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Approvals live outside lea (#300): `lox run`, in a terminal of its own, starts the workspace's
// hub and locks and decides the agents' Lock requests. lea runs no lox command; it only tells
// whether a lox run serves the workspace, so that its start, its deck and lea doctor can say to
// run one. lea starts either way: a request waits at the hub until lox run decides it.

// LoxRunHint is what lea says when the workspace has locks and no lox run serves them.
const LoxRunHint = "Open another terminal (outside lea) and run `lox run` to approve the agents' connections and writes."

// The states LoxRunState reports.
const (
	LoxRunServing = "serving" // a lox run serves the workspace's decisions
	LoxRunOff     = "off"     // none does: the agents' Lock requests wait
	LoxRunUnknown = ""        // lea cannot tell (no state directory, lea-ctl)
)

// loxRunsFile is lox run's session journal in the workspace's state directory (lockhub's
// internal/loxaudit RunsFile): one JSON event per line, "start" when a lox run starts serving,
// "mode" when it switches mode, "stop" when it quits.
const loxRunsFile = "runs.jsonl"

// maxRunsTail is how much of the journal's end lea reads: the last event is all it needs.
const maxRunsTail = 64 << 10

// loxRunProcs reports whether a lox run process of the user runs (ok false: lea cannot tell on
// this host), a variable so tests can fake the process table.
var loxRunProcs = procLoxRun

// LoxRunState is whether a lox run serves the decisions of workspace proj (its physical path),
// from what lox run leaves on the host: its session journal, whose last event is a start (or a
// mode switch) while it serves and a stop once it quits, and, where lea can read the process
// table (/proc), a lox run process of the user, so that a lox run that was killed before it
// wrote its stop does not count. The journal is lox run's
// ($XDG_STATE_HOME/lockfamily/<project>/runs.jsonl, the project named as lockUpComposeProject
// names it). Unknown in lea-ctl, whose container sees neither.
func LoxRunState(getenv func(string) string, proj string) string {
	if getenv("LEA_CTL_INSIDE") == "1" {
		return LoxRunUnknown
	}
	st := getenv("XDG_STATE_HOME")
	if st == "" {
		h := getenv("HOME")
		if h == "" {
			return LoxRunUnknown
		}
		st = filepath.Join(h, ".local", "state")
	}
	ev := lastRunEvent(filepath.Join(st, "lockfamily", lockUpComposeProject(proj), loxRunsFile))
	if ev != "start" && ev != "mode" {
		return LoxRunOff
	}
	if running, ok := loxRunProcs(); ok && !running {
		return LoxRunOff
	}
	return LoxRunServing
}

// lastRunEvent is the event of the journal's last line that reads ("" when none does, or the
// journal is missing or not a regular file).
func lastRunEvent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	if fi.Size() > maxRunsTail {
		if _, err := f.Seek(fi.Size()-maxRunsTail, io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRunsTail))
	if err != nil {
		return ""
	}
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(lines[i], &e) == nil {
			switch e.Event {
			case "start", "mode", "stop":
				return e.Event
			}
		}
	}
	return ""
}

// procLoxRun scans /proc for a process of this user whose command is lox (any path) with run as
// its first argument; ok is false where there is no /proc (macOS).
func procLoxRun() (running, ok bool) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return false, false
	}
	uid := strconv.Itoa(os.Getuid())
	seen := false
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		seen = true
		if !procOwnedBy("/proc/"+e.Name()+"/status", uid) {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		if isLoxRun(strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")) {
			return true, true
		}
	}
	return false, seen
}

// isLoxRun is whether argv is a lox run: lox (any path), then run.
func isLoxRun(argv []string) bool {
	return len(argv) >= 2 && filepath.Base(argv[0]) == "lox" && argv[1] == "run"
}

// procOwnedBy is whether the process status file says its real uid is uid.
func procOwnedBy(status, uid string) bool {
	b, err := os.ReadFile(status)
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(ln, "Uid:"); ok {
			f := strings.Fields(rest)
			return len(f) > 0 && f[0] == uid
		}
	}
	return false
}
