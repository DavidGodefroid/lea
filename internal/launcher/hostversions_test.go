package launcher

import (
	"strings"
	"testing"
)

const engineFormat = "FAKE_DOCKER_OUT_VERSION___FORMAT"

// A Docker Engine older than config/host-minimums refuses the start, unless
// LEA_ALLOW_OUTDATED_HOST=1, which only warns; an engine that is not Docker Engine is let through.
func TestRunArgsRefuseOutdatedDockerEngine(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env[engineFormat] = "Engine=29.4.1\ncontainerd=v2.1.4\nrunc=1.3.0"
	_, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err == nil || !strings.Contains(err.Error(), "Docker Engine 29.4.1 is older than 29.4.2") ||
		!strings.Contains(err.Error(), "LEA_ALLOW_OUTDATED_HOST=1") {
		t.Fatalf("outdated engine: %v", err)
	}

	w.env["LEA_ALLOW_OUTDATED_HOST"] = "1"
	w.errb.Reset()
	if _, err := w.launcher().runArgs(w.proj, "172.18.0.5"); err != nil {
		t.Fatalf("LEA_ALLOW_OUTDATED_HOST=1: %v", err)
	}
	if !strings.Contains(w.errb.String(), "lea: warning: Docker Engine 29.4.1 is older than 29.4.2") {
		t.Errorf("no warning: %q", w.errb.String())
	}
	delete(w.env, "LEA_ALLOW_OUTDATED_HOST")

	for _, out := range []string{"Engine=29.4.2", "Engine=29.10.0\nrunc=1.3.0", ""} {
		w.env[engineFormat] = out
		w.errb.Reset()
		if _, err := w.launcher().runArgs(w.proj, "172.18.0.5"); err != nil || w.errb.Len() != 0 {
			t.Errorf("engine %q: %v %q", out, err, w.errb.String())
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"29.4.2", "29.4.2", true}, {"29.10.0", "29.4.2", true}, {"29.4.1", "29.4.2", false},
		{"v2.1.4", "2.1", true}, {"28.10.3", "29.4.2", false}, {"30.0.0-rc.1", "29.4.2", true},
	} {
		if got := versionAtLeast(c.a, c.b); got != c.want {
			t.Errorf("versionAtLeast(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
