package launcher

import (
	"os"
	"strings"
	"testing"
)

func TestTmuxVersions(t *testing.T) {
	for line, ok := range map[string]bool{"tmux 3.2": true, "tmux 3.3a": true, "tmux 3.4": true, "tmux next-3.6": true,
		"tmux 4.0": true, "tmux 3.1c": false, "tmux 2.9": false, "": false, "screen 4.0": false} {
		if TmuxVersionOK(line) != ok {
			t.Errorf("%q: want %v", line, ok)
		}
	}
}

func TestTmuxResolution(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	delete(w.env, "LEA_TMUX_BIN")
	l := w.launcher()
	if b, ok := l.tmuxBin(); !ok || b != w.tmux {
		t.Fatalf("host tmux 3.4 on PATH: %q %v", b, ok)
	}
	w.env["FAKE_TMUX_VERSION"] = "tmux 3.1"
	built := w.dir + "/built-tmux"
	os.WriteFile(built, []byte(fakeTmux), 0o755)
	w.env["LEA_TMUX_BUILT"] = built
	l = w.launcher()
	if b, _ := l.tmuxBin(); b != built {
		t.Fatalf("an old host tmux falls back to the built one, got %q", b)
	}
	w.env["LEA_TMUX_BIN"] = w.dir
	l = w.launcher()
	if _, ok := l.tmuxBin(); ok {
		t.Fatal("a LEA_TMUX_BIN that is a directory is not a tmux")
	}
	if err := l.tmuxCheck(); err == nil || !strings.Contains(err.Error(), "LEA_TMUX_BIN="+w.dir+" is not an executable file") {
		t.Fatalf("err = %v", err)
	}
}

func TestTmuxInstallHintFollowsOSRelease(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	osr := w.dir + "/os-release"
	w.env["LEA_OS_RELEASE"] = osr
	for content, want := range map[string]string{
		"ID=ubuntu\n": "sudo apt install tmux",
		"ID=\"rocky\"\nID_LIKE=\"rhel fedora\"\n": "sudo dnf install tmux",
		"ID=arch\n":  "sudo pacman -S tmux",
		"ID=plan9\n": "install tmux 3.2 or later with your package manager",
	} {
		os.WriteFile(osr, []byte(content), 0o644)
		if got := w.launcher().tmuxInstallHint(); got != want {
			t.Errorf("%q: %q, want %q", content, got, want)
		}
	}
}
