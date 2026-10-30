package launcher

import (
	"os"
	"strings"
	"testing"
)

const upProject = "lockfamily-1a2b3c4d"

func writeUp(t *testing.T, w *world, locks string) {
	t.Helper()
	d := w.proj + "/.lockfamily"
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d+"/up.json", []byte(`{"version":1,"project":"`+upProject+`","locks":{`+locks+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// labels makes the fake docker answer lock up's labels for the socket volume of name (type typ).
func labels(w *world, name, typ, role string) {
	vol := upProject + "_" + name + "-run"
	w.env["FAKE_DOCKER_OUT_VOLUME_INSPECT_AT_"+fakeKey(vol)] = `{"com.docker.compose.project":"` + upProject +
		`","com.docker.compose.volume":"` + name + `-run","dev.lockfamily.role":"` + role + `","dev.lockfamily.instance":"` + name +
		`","dev.lockfamily.type":"` + typ + `"}`
}

func lockUp(name, typ string) string {
	return `"` + name + `":{"type":"` + typ + `","socket_volume":"` + upProject + "_" + name + `-run"}`
}

// Locks of lock up: their socket volumes replace the fixed ones, for the tools that are on.
func TestLockUpSocketVolumes(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq")+","+lockUp("docs", "lockfile"))
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	labels(w, "docs", "lockfile", "socket")
	args := lockArgs(t, w)
	for _, m := range []string{"type=volume,src=" + upProject + "_crm-run,dst=/run/locksql",
		"type=volume,src=" + upProject + "_events-run,dst=/run/lockmq",
		"type=volume,src=" + upProject + "_docs-run,dst=/run/lockfile"} {
		if !contains(args, m) {
			t.Errorf("missing %q in %v", m, args)
		}
	}
	for _, a := range args {
		if strings.Contains(a, "src=locksql-run") || strings.Contains(a, "src=lockmq-run") || strings.Contains(a, "src=lockfile-run") ||
			strings.Contains(a, "-home") || strings.Contains(a, "lockhub") {
			t.Errorf("fixed, home or hub mount: %s", a)
		}
	}
	if strings.Contains(w.errb.String(), "warning") {
		t.Errorf("warnings: %s", w.errb.String())
	}
}

// Whatever up.json says, only a volume docker labels as lock up's socket volume of that lock is
// mounted: not a lock's home, not a volume of another project or instance.
func TestLockUpRefusesOtherVolumes(t *testing.T) {
	for name, c := range map[string]struct{ up, role string }{
		"home volume":     {`"crm":{"type":"locksql","socket_volume":"` + upProject + `_crm-home"}`, "socket"},
		"role not socket": {lockUp("crm", "locksql"), "gate-home"},
		"no labels":       {lockUp("crm", "locksql"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, repoRoot(t))
			writeUp(t, w, c.up)
			if c.role != "" {
				labels(w, "crm", "locksql", c.role)
			}
			args := lockArgs(t, w)
			for _, a := range args {
				if strings.Contains(a, upProject) {
					t.Errorf("mounted %s", a)
				}
			}
			if !contains(args, "type=volume,src=locksql-run,dst=/run/locksql") {
				t.Error("fixed locksql-run not kept")
			}
			if !strings.Contains(w.errb.String(), "warning") {
				t.Error("no warning")
			}
		})
	}
}

// Two locks of one tool: the agents reach neither (lea mounts one socket per tool).
func TestLockUpTwoOfATool(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("a", "locksql")+","+lockUp("b", "locksql"))
	labels(w, "a", "locksql", "socket")
	labels(w, "b", "locksql", "socket")
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProject) {
			t.Errorf("mounted %s", a)
		}
	}
	if !strings.Contains(w.errb.String(), "2 locksql locks") {
		t.Errorf("warning: %s", w.errb.String())
	}
}

// A tool that is off stays off, lock up or not.
func TestLockUpToolOff(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("events", "lockmq"))
	labels(w, "events", "lockmq", "socket")
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "lockmq") {
			t.Errorf("lockmq mounted while off: %s", a)
		}
	}
}

// An up.json others could write is not lock up's.
func TestLockUpFileMustBeTheUsers(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUp(t, w, lockUp("crm", "locksql"))
	labels(w, "crm", "locksql", "socket")
	os.Chmod(w.proj+"/.lockfamily/up.json", 0o666)
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProject) {
			t.Errorf("mounted %s", a)
		}
	}
	if !strings.Contains(w.errb.String(), "writable by others") {
		t.Errorf("warning: %s", w.errb.String())
	}
	// A link is not followed.
	os.Remove(w.proj + "/.lockfamily/up.json")
	os.WriteFile(w.proj+"/elsewhere.json", []byte(`{"version":1,"project":"`+upProject+`","locks":{`+lockUp("crm", "locksql")+`}}`), 0o644)
	os.Symlink(w.proj+"/elsewhere.json", w.proj+"/.lockfamily/up.json")
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, upProject) {
			t.Errorf("mounted through a link: %s", a)
		}
	}
}
