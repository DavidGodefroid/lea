package launcher

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The copy-only clipboard bridge (ported from lib/clipboard.sh). One host loop serves every lea
// session; it is recognised by its command line (`sh -c … lea-clipboard <fifo> …`) and its pid
// file host/clipboard.pid, so a second launch starts no second one.

const clipboardMax = "1048576"

// clipboardLoop is the loop of lea_clipboard_start, word for word.
const clipboardLoop = `
    f=$1 max=$2 notify=$3; shift 3
    while [ -p "$f" ] && [ ! -L "$f" ]; do
      t=$(mktemp) || exit 1
      head -c "$max" <"$f" >"$t"
      if [ -s "$t" ]; then
        setsid -f "$@" <"$t" >/dev/null 2>&1
        if [ "$notify" = 1 ] && command -v notify-send >/dev/null 2>&1; then
          notify-send -a lea -i edit-copy lea "lea wrote to the clipboard" >/dev/null 2>&1 || true
        fi
      fi
      rm -f "$t"
    done`

func (l *Launcher) clipboardPidfile() string { return l.Cfg.HostDir() + "/clipboard.pid" }

// clipboardLoopPid is lea_clipboard_loop_pid: the running loop, never a recycled pid.
func (l *Launcher) clipboardLoopPid() (int, bool) {
	pf := l.clipboardPidfile()
	if SafePath(l.Cfg.DataDir(), pf) != nil || !isRegular(pf) || isSymlink(pf) {
		return 0, false
	}
	b, _ := os.ReadFile(pf)
	if len(b) > 16 {
		b = b[:16]
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, string(b))
	pid, err := strconv.Atoi(digits)
	if err != nil || pid <= 0 || syscall.Kill(pid, 0) != nil {
		return 0, false
	}
	cmd, err := os.ReadFile("/proc/" + digits + "/cmdline")
	if err != nil || !strings.Contains(strings.ReplaceAll(string(cmd), "\x00", " "), " lea-clipboard "+l.clipboardFifo()+" ") {
		return 0, false
	}
	return pid, true
}

func (l *Launcher) clipboardHostCmd() []string {
	if l.Cfg.Get("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return []string{"wl-copy"}
		}
	}
	if l.Cfg.Get("DISPLAY") != "" {
		if _, err := exec.LookPath("xclip"); err == nil {
			return []string{"xclip", "-selection", "clipboard", "-in"}
		}
	}
	return nil
}

// clipboardStart is lea_clipboard_start: reuses the running loop, or starts one in a session of
// its own. A symlink on the FIFO or the pid file path is fatal, with lea_safe_path's message;
// unlike the shell (whose check of the pid file dies only in a subshell), no loop is started
// before that.
func (l *Launcher) clipboardStart() error {
	if nonEmpty(l.Cfg.Get("LEA_CLIPBOARD"), "1") == "0" {
		return nil
	}
	f := l.clipboardFifo()
	if err := SafePath(l.Cfg.DataDir(), f); err != nil {
		return die("%v", err)
	}
	if !isFifo(f) {
		return nil
	}
	hc := l.clipboardHostCmd()
	if hc == nil {
		l.logf("no wl-copy (Wayland) or xclip (X11) on the host: copying from lea reaches no clipboard")
		return nil
	}
	if err := SafePath(l.Cfg.DataDir(), l.clipboardPidfile()); err != nil {
		return die("%v", err)
	}
	if _, ok := l.clipboardLoopPid(); ok {
		return nil
	}
	if l.refused() {
		return errInterrupted
	}
	notify := nonEmpty(l.Cfg.Get("LEA_CLIPBOARD_NOTIFY"), "0")
	c := l.cmd("sh", append([]string{"-c", clipboardLoop, "lea-clipboard", f, clipboardMax, notify}, hc...)...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() != nil {
		return nil // as a failed `setsid sh -c … &`: nothing to record
	}
	pid := c.Process.Pid
	_ = c.Process.Release()
	if err := Mkdirs(l.Cfg.DataDir(), l.Cfg.HostDir()); err != nil {
		return die("%v", err)
	}
	if err := WriteFile(l.Cfg.DataDir(), l.clipboardPidfile(), []byte(strconv.Itoa(pid)+"\n")); err != nil {
		return die("%v", err)
	}
	return nil
}

// deckRunning is lea__deck_running: a deck session exists on lea's tmux server.
func (l *Launcher) deckRunning() bool {
	s, ok := l.tmuxServer()
	return ok && l.tmuxClient(s, "list-sessions") == nil
}

// clipboardStop is lea_clipboard_stop: the loop stops once no lea session and no deck session
// is left; force stops it regardless. A symlink on the sessions directory is fatal, with
// lea_safe_path's message; one on the pid file is reported and the loop left alone, as the
// shell's check dies there only in the $(lea_clipboard_loop_pid) subshell.
func (l *Launcher) clipboardStop(force bool) error {
	if !force {
		if l.deckRunning() {
			return nil
		}
		d := l.Cfg.HostDir() + "/sessions"
		if err := SafePath(l.Cfg.DataDir(), d); err != nil {
			return die("%v", err)
		}
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if !e.IsDir() || isSymlink(d+"/"+e.Name()) {
				continue
			}
			n, err := l.sessionCount(e.Name())
			if err != nil {
				l.logf("error: %v", err) // `$(lea_session_count … || printf 1)`: reported, counts as open
			}
			if err != nil || n != 0 {
				return nil
			}
		}
	}
	if err := SafePath(l.Cfg.DataDir(), l.clipboardPidfile()); err != nil {
		l.logf("error: %v", err)
		return nil
	}
	pid, ok := l.clipboardLoopPid()
	if !ok {
		return nil
	}
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	os.Remove(l.clipboardPidfile())
	return nil
}
