package launcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/locks"
)

func shippedSet(t *testing.T) *locks.Set {
	t.Helper()
	s, err := locks.LoadSet(repoRoot(t) + "/" + locks.ManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// writeFamily writes lockfamily.yaml with the locks name: image given.
func writeFamily(t *testing.T, w *world, imgs map[string][2]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("version: 1\nhub:\n  image: ghcr.io/davidgodefroid/lockhub@sha256:" + strings.Repeat("c", 64) + "\nlocks:\n")
	for name, ti := range imgs {
		b.WriteString("  " + name + ":\n    type: " + ti[0] + "\n    image: \"" + ti[1] + "\"  # lox add\n    allowed_uids:\n      - 1000\n")
	}
	b.WriteString("vault:\n  address: https://vault.example\n  image: not-a-lock\n")
	if err := os.MkdirAll(w.proj+"/.lockfamily", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.proj+"/.lockfamily/lockfamily.yaml", []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDeclaredImages(t *testing.T) {
	got := parseDeclaredImages(strings.NewReader(`version: 1
hub:
  image: ghcr.io/davidgodefroid/lockhub@sha256:aa
locks:
  # crm first
  crm-prod:
    type: locksql
    image: 'ghcr.io/davidgodefroid/locksql@sha256:bb' # pinned
    nested:
      image: no
  "events":
    image: ghcr.io/davidgodefroid/lockmq@sha256:cc
    type: lockmq
other:
  image: ghcr.io/x
`))
	if len(got) != 2 || got["crm-prod"] != "ghcr.io/davidgodefroid/locksql@sha256:bb" || got["events"] != "ghcr.io/davidgodefroid/lockmq@sha256:cc" {
		t.Errorf("%v", got)
	}
}

// lea doctor compares each lock lox runs with lea's client of its type: one that runs the set's
// image is compatible without asking it; one of another image is asked its handshake (lea's core in
// lea's image, the lock's socket volume alone mounted), and an incompatible pair fails with the fix.
func TestDoctorLockCompatibility(t *testing.T) {
	set := shippedSet(t)
	sq, mq, lf := set.Pins["locksql"], set.Pins["lockmq"], set.Pins["lockfile"]
	other := func(typ string) string { return "ghcr.io/davidgodefroid/" + typ + "@sha256:" + strings.Repeat("d", 64) }
	w := newWorld(t, repoRoot(t))
	// events serves the profile p: the probe names its socket (a 2710 directory cannot be listed).
	writeUp(t, w, lockUp("crm", "locksql")+`,"events":{"type":"lockmq","socket_volume":"PROJECT_events-run","profile":"p"},`+lockUp("docs", "lockfile"))
	writeFamily(t, w, map[string][2]string{"crm": {"locksql", sq.Image}, "events": {"lockmq", other("lockmq")}, "docs": {"lockfile", other("lockfile")}})
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	labels(w, "docs", "lockfile", "socket")
	w.env["FAKE_LOX_VERSION"] = strings.TrimPrefix(set.Lox, "v")
	// lockmq answers the handshake of an older release, lockfile of another protocol.
	w.env["FAKE_DOCKER_OUT_RUN___RM_AT_USER_P_SOCK"] = `[{"socket":"x-default.sock","hello":{"protocol_major":1,"console_version":"0.6.0","profile":"default"}}]`
	// lockfile's console speaks protocol 2: it refuses lea's handshake as lockkit's does, and the
	// probe's answer is lea lock-hello's (locks.AskHello's) on such a console.
	w.env["FAKE_DOCKER_OUT_RUN___RM_AT__RUN_LOCKFILE"] = refusingConsoleHello(t, lf.Protocol, lf.Protocol+1)
	l := w.launcher()
	err := l.doctorLocks(w.proj)
	o := w.out.String()
	if err == nil {
		t.Errorf("an incompatible pair passed: %s", o)
	}
	for _, want := range []string{
		"ok    Lock clients: one set, " + set.Name + ": locksql " + sq.Version,
		"ok    lox " + set.Lox,
		"ok    lock crm (locksql): runs locksql " + sq.Version + ", the release of lea's locksql client " + sq.Version,
		"warn  lock events (lockmq): runs lockmq v0.6.0 (its console's handshake), older than lea's lockmq client " + mq.Version +
			" (same protocol 1): brew upgrade lox, then lox add lockmq events --version " + mq.Version + ", then lox up",
		"FAIL  lock docs (lockfile): incompatible: its console refuses the handshake of lea's lockfile client " + lf.Version +
			", which speaks protocol 1 (the console speaks protocol 2), so every call of lea's agents is refused: lea update (a lea whose set speaks protocol 2), or lox add lockfile docs --version " + lf.Version,
	} {
		if !strings.Contains(o, want) {
			t.Errorf("doctor lacks %q:\n%s", want, o)
		}
	}
	// The set's image is not asked; the others are, in lea's image, without network or
	// capability, with only their socket volume and lea's core mounted.
	if w.hasCall("[/run/locksql]") {
		t.Error("the lock of the set's image was asked")
	}
	for _, typ := range []string{"lockmq", "lockfile"} {
		reg, _ := locks.Get(typ)
		name := map[string]string{"lockmq": "events", "lockfile": "docs"}[typ]
		want := "docker [run] [--rm] [--pull] [never] [--network] [none] [--read-only] [--cap-drop] [ALL] [--security-opt] [no-new-privileges]"
		ok := false
		for _, c := range w.calls() {
			if strings.HasPrefix(c, want) && strings.Contains(c, "[--group-add] ["+reg.GID+"]") &&
				strings.Contains(c, "[type=volume,src="+upProjectOf(w)+"_"+name+"-run,dst=/run/"+typ+",readonly]") &&
				strings.Contains(c, "[type=bind,src="+w.core+",dst=/usr/local/bin/lea,readonly]") &&
				strings.HasSuffix(c, "[lea:dev] [lock-hello] [--uid] ["+reg.UID+"] [--protocol] ["+strconv.Itoa(set.Pins[typ].Protocol)+"] [/run/"+typ+"]"+map[string]string{"lockmq": " [" + strings.Join(locks.SocketNames("/run/lockmq", w.proj, "p"), "] [") + "]"}[typ]) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s: no handshake probe as expected:\n%s", typ, strings.Join(w.calls(), "\n"))
		}
	}
}

// refusingConsoleHello is lea lock-hello's output (locks.AskHello's JSON) for a handshake of
// protocol sent to a console of protocol speaks that refuses it as lockkit's console does
// (console/server.go hello: invalid-request, "protocol mismatch: client speaks N, console speaks
// M"; it never answers its own major).
func refusingConsoleHello(t *testing.T, sent, speaks int) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "x-default.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		var req struct {
			Params struct {
				ProtocolMajor int `json:"protocol_major"`
			} `json:"params"`
		}
		_ = json.Unmarshal([]byte(line), &req)
		if req.Params.ProtocolMajor == speaks {
			fmt.Fprintf(c, `{"jsonrpc":"2.0","id":1,"result":{"protocol_major":%d,"console_version":"9.1.0","profile":"default"}}`+"\n", speaks)
			return
		}
		fmt.Fprintf(c, `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"protocol mismatch: client speaks %d, console speaks %d; use the same lockfile version on both sides"}}`+"\n",
			req.Params.ProtocolMajor, speaks)
	}()
	rs, err := locks.AskHello(dir, nil, os.Getuid(), sent, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rs)
	return string(b)
}

// lea doctor fails, status 1, when a lock lox runs speaks another protocol than lea's client: its
// console refuses lea's handshake as lockkit's does; the same lock speaking lea's protocol passes.
func TestDoctorFailsOnARefusedHandshake(t *testing.T) {
	set := shippedSet(t)
	lf := set.Pins["lockfile"]
	for _, c := range []struct {
		speaks, status int
		want           string
	}{
		{lf.Protocol + 1, 1, "FAIL  lock docs (lockfile): incompatible: its console refuses the handshake of lea's lockfile client " + lf.Version},
		{lf.Protocol, 0, "warn  lock docs (lockfile): runs lockfile v9.1.0 (its console's handshake), newer than lea's lockfile client " + lf.Version},
	} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, "")
		writeUp(t, w, lockUp("docs", "lockfile"))
		writeFamily(t, w, map[string][2]string{"docs": {"lockfile", "ghcr.io/davidgodefroid/lockfile@sha256:" + strings.Repeat("d", 64)}})
		labels(w, "docs", "lockfile", "socket")
		w.env["FAKE_LOX_VERSION"] = strings.TrimPrefix(set.Lox, "v")
		w.env["FAKE_DOCKER_OUT_RUN___RM_AT__RUN_LOCKFILE"] = refusingConsoleHello(t, lf.Protocol, c.speaks)
		st := w.launcher().Main([]string{"doctor"})
		o := w.out.String()
		if !strings.Contains(o, c.want) {
			t.Errorf("console of protocol %d: doctor lacks %q:\n%s", c.speaks, c.want, o)
		}
		if c.status == 1 && st != 1 {
			t.Errorf("a refused handshake: lea doctor exits %d", st)
		}
		if c.status == 0 && (st != 0 || strings.Contains(o, "FAIL")) {
			t.Errorf("a compatible lock: status %d:\n%s", st, o)
		}
	}
}

// A lock whose socket volume docker does not label as lox up's is never mounted for the probe;
// the dev overrides, an obsolete key and an old lox are warned about.
func TestDoctorLockCompatibilityWarnings(t *testing.T) {
	set := shippedSet(t)
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKSQL_REF=v0.16.0\nLEA_LOCKSQL_IMAGE=locksql:0.16.0\n")
	writeUp(t, w, lockUp("events", "lockmq"))
	writeFamily(t, w, map[string][2]string{"events": {"lockmq", "ghcr.io/davidgodefroid/lockmq@sha256:" + strings.Repeat("e", 64)}})
	w.env["FAKE_LOX_VERSION"] = "0.1.0"
	if err := w.launcher().doctorLocks(w.proj); err != nil {
		t.Errorf("warnings fail: %v", err)
	}
	o := w.out.String()
	for _, want := range []string{
		"warn  LEA_LOCKSQL_REF: v0.16.0 overrides the set's locksql " + set.Pins["locksql"].Version + " (a dev override",
		"warn  LEA_LOCKSQL_IMAGE: obsolete",
		"warn  lox: v0.1.0 is older than " + set.Lox + ", the release lea's clients are tested with: brew upgrade lox",
		"warn  lock events (lockmq): cannot tell whether lea's lockmq client " + set.Pins["lockmq"].Version + " speaks its protocol",
		"cannot read the labels of " + upProjectOf(w) + "_events-run",
	} {
		if !strings.Contains(o, want) {
			t.Errorf("doctor lacks %q:\n%s", want, o)
		}
	}
	if w.hasCall("docker [run]") {
		t.Error("a volume docker does not vouch for was mounted")
	}
}

// Without a Lock Family, lea doctor only names the set.
func TestDoctorLockCompatibilityWithoutLocks(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().doctorLocks(w.proj); err != nil {
		t.Fatal(err)
	}
	if o := w.out.String(); !strings.HasPrefix(o, "ok    Lock clients: one set, lockkit-v") || strings.Count(o, "\n") != 1 || w.hasCall("lox") {
		t.Errorf("%s", o)
	}
}

// lea's start warns about a mounted lock that lockfamily.yaml pins to an image of another
// release, and is silent for the set's.
func TestStartWarnsAboutALockOfAnotherRelease(t *testing.T) {
	set := shippedSet(t)
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKSQL=1\nLEA_LOCKMQ=1\n")
	writeUp(t, w, lockUp("crm", "locksql")+","+lockUp("events", "lockmq"))
	writeFamily(t, w, map[string][2]string{"crm": {"locksql", set.Pins["locksql"].Variants["vault"]},
		"events": {"lockmq", "ghcr.io/davidgodefroid/lockmq@sha256:" + strings.Repeat("f", 64)}})
	labels(w, "crm", "locksql", "socket")
	labels(w, "events", "lockmq", "socket")
	lockArgs(t, w)
	e := w.errb.String()
	if !strings.Contains(e, "warning: lock events (lockmq): cannot tell whether lea's lockmq client") || !strings.Contains(e, "(lea doctor asks its console)") {
		t.Errorf("no warning for events: %s", e)
	}
	if strings.Contains(e, "lock crm") {
		t.Errorf("a warning for the set's vault image: %s", e)
	}
	if w.hasCall("docker [run]") || w.hasCall("lox") {
		t.Error("the start asked a console")
	}
}
