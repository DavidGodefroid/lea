package launcher

import (
	"strings"
	"testing"
)

// The agents' CLIs update themselves in the container (image/lea-cli-update): on by default, so
// the container gets nothing; LEA_CLI_AUTOUPDATE=0 tells its entrypoint not to.
func TestRunArgsCLIAutoUpdate(t *testing.T) {
	for conf, want := range map[string]bool{"": false, "LEA_CLI_AUTOUPDATE=1\n": false, "LEA_CLI_AUTOUPDATE=0\n": true} {
		w := newWorld(t, repoRoot(t))
		if conf != "" {
			writeUserConf(t, w, conf)
		}
		args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
		if err != nil {
			t.Fatalf("%q: %v", conf, err)
		}
		if got := contains(args, "LEA_CLI_AUTOUPDATE=0"); got != want {
			t.Errorf("%q: -e LEA_CLI_AUTOUPDATE=0 is %v, want %v", conf, got, want)
		}
	}
}

func TestCLIAutoUpdateIsValidated(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_CLI_AUTOUPDATE=yes\n")
	if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_CLI_AUTOUPDATE must be 1 (the agents' CLIs update themselves) or 0, not 'yes'") {
		t.Errorf("err = %v", err)
	}
}
