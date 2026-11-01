package launcher

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestLoxCheckMissing(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["PATH"] = w.dir + "/bin:/usr/bin:/bin"
	err := w.launcher().loxCheck()
	want := "lox not found: lea starts the Lock Family hub with it: brew install davidgodefroid/tap/lox, then run lea again"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckTooOld(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "0.4.0"
	err := w.launcher().loxCheck()
	want := "lox 0.4.0 is older than 0.5.0, which lea needs to start the hub: brew upgrade lox, then run lea again"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckOK(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().loxCheck(); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("lox [version]") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestLoxCheckDevWarns(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "dev"
	if err := w.launcher().loxCheck(); err != nil {
		t.Fatal(err)
	}
	if got := w.errb.String(); got != "lea: warning: lox dev: its version is not checked\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestFirstRunNotNeededMakesNoCall(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().firstRun(); err != nil {
		t.Fatal(err)
	}
	if w.hasCall("docker [version]") || w.hasCall("lea-sh") {
		t.Fatalf("calls: %v", w.calls())
	}
	if want := "docker [image] [inspect] [-f] [{{.Id}}] [lea:dev]\ndocker [image] [inspect] [-f] [{{.Id}}] [lea-proxy:dev]"; strings.Join(w.calls(), "\n") != want {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestFirstRunDockerDownSaysHow(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	os.Remove(w.data + "/lea/state/claude.json")
	w.env["FAKE_DOCKER_RC_VERSION___FORMAT"] = "1"
	want := "Docker does not answer: start the Docker engine (sudo systemctl start docker), then run lea again"
	if runtime.GOOS == "darwin" {
		want = "Docker does not answer: start Docker Desktop (open -a Docker), then run lea again"
	}
	err := w.launcher().firstRun()
	if err == nil || err.Error() != want {
		t.Fatalf("firstRun() = %v, want %q", err, want)
	}
	if w.hasCall("docker [run]") || w.hasCall("lea-sh") {
		t.Fatalf("calls: %v", w.calls())
	}
}

// dockerCalledBeyondFirstRunProbes: the world called docker for more than the image checks every
// command opens with (the tests that say "before docker" mean before anything docker does).
func (w *world) dockerCalledBeyondFirstRunProbes() bool {
	for _, c := range withoutFirstRunProbes(w.calls()) {
		if strings.HasPrefix(c, "docker") {
			return true
		}
	}
	return false
}
