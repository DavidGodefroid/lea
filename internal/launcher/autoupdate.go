package launcher

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// updateEvery is how often the plain lea command looks for a new release.
const updateEvery = 24 * time.Hour

// autoUpdate starts `<core> update --background` (libexec/lib/host.sh lea_update_background) at
// most once per updateEvery, in a session of its own: the deck execs tmux, so the updater must
// outlive the launcher, and it never delays nor fails the launch. Off with LEA_AUTOUPDATE=0, in
// a git checkout (lea update pulls it) and for a dev build. The time of the last check is the
// mtime of <host>/update-check; the updater's output goes to <host>/update.log.
func (l *Launcher) autoUpdate() {
	if l.Cfg.Get("LEA_AUTOUPDATE") == "0" || !strings.HasPrefix(l.Version, "v") || exists(l.Root+"/.git") {
		return
	}
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	host := l.Cfg.HostDir()
	stamp := host + "/update-check"
	// A stamp in the future (a clock set back) counts as no stamp.
	if fi, err := os.Stat(stamp); err == nil && !fi.ModTime().After(now) && now.Sub(fi.ModTime()) < updateEvery {
		return
	}
	if Mkdirs(l.Cfg.DataDir(), l.Cfg.DataDir(), host) != nil || WriteFile(l.Cfg.DataDir(), stamp, nil) != nil ||
		os.Chtimes(stamp, now, now) != nil {
		return
	}
	logPath := host + "/update.log"
	if SafePath(l.Cfg.DataDir(), logPath) != nil {
		return
	}
	// The log keeps the latest runs only.
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > 256<<10 {
		os.Remove(logPath)
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	c := exec.Command(l.Core, "update", "--background")
	c.Env, c.Stdout, c.Stderr = l.env(), f, f
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() == nil {
		_ = c.Process.Release()
	}
}
