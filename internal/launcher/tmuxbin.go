package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/DavidGodefroid/lea/internal/tmux"
	"golang.org/x/sys/unix"
)

// TmuxMin is the oldest tmux the deck drives (control-mode client flags, format subscriptions).
const TmuxMin = "3.2"

var tmuxVersion = regexp.MustCompile(`^tmux [^0-9]*([0-9]+)\.([0-9]+)`)

// TmuxVersionOK is lea_tmux_version_ok: `tmux -V` output for 3.2 or later.
func TmuxVersionOK(line string) bool {
	m := tmuxVersion.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return maj > 3 || maj == 3 && min >= 2
}

func executable(p string) bool { return isRegular(p) && unix.Access(p, unix.X_OK) == nil }

func (l *Launcher) tmuxBuilt() string {
	return nonEmpty(l.Cfg.Get("LEA_TMUX_BUILT"), l.Root+"/build/tmux")
}

// tmuxV is `tmux -V`, the one tmux invocation outside tmux.Server (Client, AttachArgv): it
// prints the version and exits without contacting a server or reading any configuration, so it
// needs neither -L nor -f. It runs before the binary is known to be a usable tmux at all, as
// lea_tmux_bin does. Any other tmux call goes through tmux.Server.
func (l *Launcher) tmuxV(bin string) string {
	out, _ := l.output(false, bin, "-V")
	return out
}

// tmuxBin is lea_tmux_bin: $LEA_TMUX_BIN if set (it must be an executable file); else the host
// tmux when it is 3.2 or later; else lea's own build; else the (too old) host tmux.
func (l *Launcher) tmuxBin() (string, bool) {
	if b := l.Cfg.Get("LEA_TMUX_BIN"); b != "" {
		return b, executable(b)
	}
	host, _ := exec.LookPath("tmux")
	if host != "" && TmuxVersionOK(l.tmuxV(host)) {
		return host, true
	}
	if b := l.tmuxBuilt(); executable(b) {
		return b, true
	}
	return host, host != ""
}

func (l *Launcher) tmuxInstallHint() string {
	if runtime.GOOS == "darwin" {
		return "brew install tmux"
	}
	b, _ := os.ReadFile(nonEmpty(l.Cfg.Get("LEA_OS_RELEASE"), "/etc/os-release"))
	var ids []string
	for _, line := range strings.Split(string(b), "\n") {
		for _, k := range []string{"ID=", "ID_LIKE="} {
			if strings.HasPrefix(line, k) {
				ids = append(ids, strings.Fields(strings.ReplaceAll(strings.TrimPrefix(line, k), `"`, ""))...)
			}
		}
	}
	for _, id := range ids {
		switch {
		case id == "debian" || id == "ubuntu":
			return "sudo apt install tmux"
		case id == "fedora" || id == "rhel" || id == "centos":
			return "sudo dnf install tmux"
		case id == "arch":
			return "sudo pacman -S tmux"
		case strings.HasPrefix(id, "opensuse") || id == "suse" || id == "sles":
			return "sudo zypper install tmux"
		case id == "alpine":
			return "sudo apk add tmux"
		}
	}
	return "install tmux " + TmuxMin + " or later with your package manager"
}

func (l *Launcher) tmuxBuildHint() string { return fmt.Sprintf(`make -C "%s" tmux-build`, l.Root) }

func (l *Launcher) tmuxMissing() string {
	if b := l.Cfg.Get("LEA_TMUX_BIN"); b != "" {
		return fmt.Sprintf("LEA_TMUX_BIN=%s is not an executable file. Unset it, or point it at tmux %s or later", b, TmuxMin)
	}
	return fmt.Sprintf("tmux is not installed. Install it with: %s, or build a static one with: %s", l.tmuxInstallHint(), l.tmuxBuildHint())
}

// tmuxPin is lea_tmux_pin: best effort; a daemon started next drives the same tmux.
func (l *Launcher) tmuxPin() {
	if b, ok := l.tmuxBin(); ok {
		l.Cfg.Export("LEA_TMUX_BIN", b)
	}
}

// tmuxCheck is lea_tmux_check: the deck needs tmux 3.2 or later.
func (l *Launcher) tmuxCheck() error {
	b, ok := l.tmuxBin()
	if !ok {
		return die("the deck needs tmux %s or later on the host, and %s", TmuxMin, l.tmuxMissing())
	}
	v := l.tmuxV(b)
	if !TmuxVersionOK(v) {
		return die("the deck needs tmux %s or later; found '%s'. Upgrade it with: %s, or build a static one with: %s",
			TmuxMin, nonEmpty(v, "unknown"), l.tmuxInstallHint(), l.tmuxBuildHint())
	}
	l.Cfg.Export("LEA_TMUX_BIN", b)
	return nil
}

// tmuxServer is lea's tmux server with lea's configuration, its clients run with the launcher's
// environment (as lea__tmux does); false when there is no tmux.
func (l *Launcher) tmuxServer() (tmux.Server, bool) {
	b, ok := l.tmuxBin()
	return tmux.Server{Bin: b, Socket: tmux.DefaultSocket, Conf: l.Cfg.HostDir() + "/tmux.conf", Env: l.env()}, ok
}
