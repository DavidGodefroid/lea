package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lea update with a pull that fails: the cosign lea downloaded (no cosign on PATH) is removed, and
// lea says what to retry.
func TestRefreshImagesFailedPullRemovesCosign(t *testing.T) {
	if _, err := exec.LookPath("cosign"); err == nil {
		t.Skip("cosign is installed on this host")
	}
	w := newWorld(t, repoRoot(t))
	bin, tmp := w.dir+"/nocosign", w.dir+"/tmp"
	for _, d := range []string{bin, tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fakes, _ := filepath.Glob(repoRoot(t) + "/tests/fakes/*")
	for _, f := range fakes {
		if filepath.Base(f) != "cosign" {
			if err := os.Symlink(f, bin+"/"+filepath.Base(f)); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.env["PATH"] = bin + ":/usr/bin:/bin"
	w.env["TMPDIR"] = tmp
	w.env["FAKE_DOCKER_RC_PULL_REG_A_1"] = "1"
	// lea's pinned cosign, downloaded into the temporary directory lea__co_get made.
	script := "lea__cosign() { cp '" + repoRoot(t) + "/tests/fakes/cosign' \"$1/cosign\"; echo \"$1/cosign\"; }\n" +
		"LEA_IMAGE=reg/a:1 LEA_PROXY_IMAGE=reg/b:1 LEA_LOCKSQL=0\nlea_refresh_images"
	_, se, sc := shellEval(t, w.root, w.environ(), w.proj, script)
	if sc == 0 || !strings.Contains(se, "cannot pull reg/a:1: check your network or registry (docker pull reg/a:1), then run lea update again") {
		t.Fatalf("status %d: %s", sc, se)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("left in TMPDIR: %v", left)
	}
}
