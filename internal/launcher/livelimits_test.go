package launcher

import (
	"slices"
	"testing"

	livelimits "github.com/DavidGodefroid/lea/internal/limits"
)

// No daemon stamp reads a live limit (#420): setting one in the user configuration never makes
// the next lea restart the daemon, which applies it live instead. Nor does the launcher export
// one to the daemon: the daemon reads them from the files itself (limits_set writes them there).
func TestLiveLimitsAreInNoDaemonStamp(t *testing.T) {
	base := map[string]string{"LEA_AUTO_RESUME": "1", "LEA_HEAVY_SLOTS": "4", "LEA_DECK": "1"}
	stamps := func(env map[string]string) []string {
		get := func(k string) string { return env[k] }
		return []string{DaemonResumeStamp(get), DaemonLockHTTPStamp(get), DaemonHubStamp(get), DaemonStacksStamp(get),
			DaemonAdmitStamp(get), DaemonDeckStamp(get)}
	}
	before := stamps(base)
	for _, d := range livelimits.Defs {
		for _, v := range []string{"0", "1", "100", d.Default} {
			env := map[string]string{d.Env: v}
			for k, x := range base {
				env[k] = x
			}
			if got := stamps(env); !slices.Equal(got, before) {
				t.Fatalf("%s=%s changes a daemon stamp: %q, was %q", d.Env, v, got, before)
			}
		}
		if slices.Contains(ExportedKeys, d.Env) {
			t.Fatalf("%s is exported to the daemon", d.Env)
		}
	}
}
