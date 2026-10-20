package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (l *Launcher) daemonPidfile() string { return l.Cfg.HostDir() + "/daemon.pid" }

// daemonAnswers is `lea agents >/dev/null 2>&1`: the daemon's API answers.
func (l *Launcher) daemonAnswers() bool { return l.quiet(l.Core, "agents") == nil }

// providersStamp is what a daemon records of the provider settings: one started with others is
// restarted (DaemonProvidersStamp, the same line).
func (l *Launcher) providersStamp() string {
	return DaemonProvidersStamp(l.Cfg.Get)
}

// DaemonProvidersStamp is the provider settings as one line: the default provider and model, the
// providers file and the internal-hosts file it is checked against.
func DaemonProvidersStamp(get func(string) string) string {
	return strings.Join([]string{get("LEA_PROVIDER"), get("LEA_MODEL"), get("LEA_PROVIDERS"), get("LEA_INTERNAL_HOSTS")}, " ")
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimRight(string(b), "\n")
}

// ensureDaemon is lea_daemon_ensure: one daemon per user, started on demand; restarted when it
// drives another tmux, runs another build (daemon.version, written by the daemon, against
// `<core> version`) or recorded other approval settings (its agents keep running under abduco and
// are reattached at start).
func (l *Launcher) ensureDaemon() error {
	l.tmuxPin()
	host := l.Cfg.HostDir()
	if l.daemonAnswers() {
		tb := l.Cfg.Get("LEA_TMUX_BIN")
		appr := nonEmpty(l.Cfg.Get("LEA_APPROVALS"), "deck") + " " + nonEmpty(l.Cfg.Get("LEA_APPROVAL_TIMEOUT"), "60")
		// `$(core version 2>/dev/null || true)`: a core that fails or prints nothing counts as "".
		ver, _ := l.output(false, l.Core, "version")
		if (tb == "" || readTrim(host+"/daemon.tmux") == tb) && readTrim(host+"/daemon.version") == ver &&
			readTrim(host+"/daemon.approvals") == appr && readTrim(host+"/daemon.providers") == l.providersStamp() {
			return nil
		}
		l.stopDaemon()
		for dw := 0; l.daemonAnswers(); {
			dw++
			if dw >= 50 {
				return die("the running lea daemon did not stop (kill it, then retry)")
			}
			l.Sleep(100 * time.Millisecond)
		}
	}
	if err := Mkdirs(l.Cfg.DataDir(), l.Cfg.DataDir(), host); err != nil {
		return die("%v", err)
	}
	logPath := host + "/daemon.log"
	if err := SafePath(l.Cfg.DataDir(), logPath); err != nil {
		return die("%v", err)
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return die("%v", err)
	}
	// argv[0] is the full core path: lea_daemon_stop recognises "<core> daemon".
	c := exec.Command(l.Core, "daemon")
	c.Env, c.Stdout, c.Stderr = l.env(), f, f
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = c.Start()
	f.Close()
	if err != nil {
		return die("could not start the lea daemon: %v", err)
	}
	pid := c.Process.Pid
	_ = c.Process.Release()
	if err := WriteFile(l.Cfg.DataDir(), l.daemonPidfile(), []byte(strconv.Itoa(pid)+"\n")); err != nil {
		return die("%v", err)
	}
	for di := 0; !l.daemonAnswers(); {
		di++
		if di >= 50 {
			return die("the lea daemon did not start in 5 s (see %s/daemon.log)", host)
		}
		l.Sleep(100 * time.Millisecond)
	}
	return nil
}

// stopDaemon is lea_daemon_stop: kill only the lea daemon (after a crash or reboot the pid may
// belong to an unrelated process), then drop the pid file once the daemon is gone; a daemon that
// outlives the wait keeps its pid file, so a retry can still stop it.
func (l *Launcher) stopDaemon() {
	pf := l.daemonPidfile()
	if !exists(pf) {
		return
	}
	s := readTrim(pf)
	if pid, err := strconv.Atoi(s); err == nil && pid > 0 && strings.Trim(s, "0123456789") == "" && l.isDaemon(pid) {
		syscall.Kill(pid, syscall.SIGTERM)
		for i := 0; l.isDaemon(pid); i++ {
			if i >= 50 {
				return
			}
			l.Sleep(100 * time.Millisecond)
		}
	}
	os.Remove(pf)
}

// isDaemon tells whether pid runs "<core> daemon": this core, or the core of another install
// next to it (<install dir>/<version>/build/lea, #34: an update leaves the previous version's
// daemon running). A gone process, or a zombie (empty command line), is no daemon.
func (l *Launcher) isDaemon(pid int) bool {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	argv := strings.Split(string(b), "\x00")
	if len(argv) < 2 || argv[1] != "daemon" {
		return false
	}
	if argv[0] == l.Core {
		return true
	}
	bin := filepath.Dir(argv[0])
	return filepath.Base(argv[0]) == "lea" && filepath.Base(bin) == "build" &&
		filepath.Dir(filepath.Dir(bin)) == filepath.Dir(l.Root)
}
