package launcher

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lockkit/ticket"
)

// testHubKey is an Ed25519 public key (32 bytes) in base64, as up.json's hub_key.
var testHubKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

// testFacades are two host facades' keys (lockkit's ticket.FacadeSet), not in canonical form.
var testFacades = `{"v": 1, "facades": [{"key": "` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) +
	`", "facade": "tty"}, {"facade": "deck", "key": "` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)) + `"}]}`

// testDevices is one paired phone (lockkit's ticket.DeviceSet) as lock up would embed it: "v"
// before "devices", not lockkit's canonical form.
var testDevices = func() string {
	p, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	cred := []byte("credential-of-the-phone")
	d := ticket.Device{V: 1, ID: ticket.DeviceID(cred), Credential: base64.RawURLEncoding.EncodeToString(cred), Name: "phone",
		RPID: "hub.example.net", Origin: "https://hub.example.net", ES256: base64.RawURLEncoding.EncodeToString(p.PublicKey().Bytes()),
		X25519: base64.RawURLEncoding.EncodeToString(x.PublicKey().Bytes())}
	b, err := json.Marshal(struct {
		V       int             `json:"v"`
		Devices []ticket.Device `json:"devices"`
	}{1, []ticket.Device{d}})
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// hubUp writes lock up's up.json with the hub and the lockhttp lock web (its extra fields: lock),
// with facade keys and devices when given, and makes the fake docker answer lock up's labels of
// its volumes and network.
func hubUp(t *testing.T, w *world, lock, facades, devices string) {
	t.Helper()
	// A launcher of a release with every flag lea passes (TestLockHTTPGateFollowsThePinnedRelease
	// tries others).
	if err := os.WriteFile(lockHTTPLauncher(), []byte(fakeLockHTTPLauncher("0.5.0", testDigest)), 0o755); err != nil {
		t.Fatal(err)
	}
	d := w.proj + "/.lockfamily"
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := upProjectOf(w)
	s := `{"version":1,"project":"` + pr + `","workspace":"` + w.proj + `","hub_network":"` + pr + `_lockhub",` +
		`"hub_url":"http://lockhub:8443","hub_key":"` + testHubKey + `","lockhttp_gate":1,`
	if facades != "" {
		s += `"facade_keys":` + facades + `,`
	}
	if devices != "" {
		s += `"devices":` + devices + `,`
	}
	s += `"locks":{"web":{"type":"lockhttp","socket_volume":"` + pr + `_web-run","gate_home_volume":"` + pr + `_web-gate","client_gid":"10020"` + lock + `}}}`
	if err := os.WriteFile(d+"/up.json", []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	labels(w, "web", "lockhttp", "socket")
	w.env["FAKE_DOCKER_OUT_VOLUME_INSPECT_AT_"+fakeKey(pr+"_web-gate")] = `{"com.docker.compose.project":"` + pr +
		`","com.docker.compose.volume":"web-gate","dev.lockfamily.role":"gate-home","dev.lockfamily.instance":"web",` +
		`"dev.lockfamily.type":"lockhttp","dev.lockfamily.workspace":"` + w.proj + `"}|`
	w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT_AT_"+fakeKey(pr+"_lockhub")] = `{"com.docker.compose.project":"` + pr +
		`","com.docker.compose.network":"lockhub","dev.lockfamily.role":"hub","dev.lockfamily.workspace":"` + w.proj + `"}|172.30.0.0/16 fd00:30::/64 `
}

func gateRun(t *testing.T, w *world) string {
	t.Helper()
	r := gateRuns(w)
	if len(r) != 1 {
		t.Fatalf("gate runs %q:\n%s", r, strings.Join(w.calls(), "\n"))
	}
	return r[0]
}

func hubFile(t *testing.T, w *world, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(lockhttp.GateDir(w.launcher().Cfg.DataDir()), "hub", name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

// lock up runs a lockhttp lock that pins the paired devices: lea's gate asks the hub, from lock
// up's socket volume and gate volume, with the hub's key, the facade keys and the devices in its
// read-only /run/lockfamily, the hub's subnets refused, sealed and device-signed as hub.mobile
// says; only the proxy joins the hub's network.
func TestLockHTTPGateAsksTheHubOfLockUp(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	hubUp(t, w, `,"reveal":true,"devices":true,"require_device":["prod-write","write"],"seal_clear":true`, testFacades, testDevices)
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	pr := upProjectOf(w)
	run := gateRun(t, w)
	hubDir := filepath.Join(lockhttp.GateDir(w.launcher().Cfg.DataDir()), "hub")
	for _, want := range []string{
		"[--mount] [type=volume,src=" + pr + "_web-run,dst=/run/lockhttp]",
		"[--mount] [type=volume,src=" + pr + "_web-gate,dst=/lockhttp-home]",
		"[--mount] [type=bind,src=" + hubDir + ",dst=/run/lockfamily,readonly]",
		"[--hub] [http://lockhub:8443] [--hub-plain-http] [--hub-key-file] [/run/lockfamily/hub.pub] [--instance] [web] [--home] [/lockhttp-home]",
		"[--hub-net] [172.30.0.0/16] [--hub-net] [fd00:30::/64]",
		"[--facade-keys-file] [/run/lockfamily/facades.json]",
		"[--devices-file] [/run/lockfamily/devices.json] [--require-device] [prod-write,write] [--seal-clear]",
		"[--label] [lea.lockhttp-hub=",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("gate run lacks %s:\n%s", want, run)
		}
	}
	if strings.Contains(run, "src=lockhttp-run") {
		t.Errorf("the fixed console socket: %s", run)
	}
	if !w.hasCall("docker [network] [connect] [" + pr + "_lockhub] [lea-proxy]") {
		t.Errorf("the proxy did not join the hub's network:\n%s", strings.Join(w.calls(), "\n"))
	}
	for _, c := range w.calls() {
		if strings.Contains(c, "[network] [connect]") && !strings.HasSuffix(c, "[lea-proxy]") {
			t.Errorf("another container joins a network: %s", c)
		}
	}
	if k, _ := hubFile(t, w, "hub.pub"); k != testHubKey+"\n" {
		t.Errorf("hub.pub %q", k)
	}
	// Both as the gate reads them: lockkit's parsers, devices in canonical form only.
	if f, _ := hubFile(t, w, "facades.json"); func() error { _, err := ticket.ParseFacades([]byte(f)); return err }() != nil ||
		!strings.Contains(f, `"facade":"deck"`) || strings.Contains(f, " ") {
		t.Errorf("facades.json %q", f)
	}
	if d, _ := hubFile(t, w, "devices.json"); func() error { _, err := ticket.ParseDevices([]byte(d)); return err }() != nil {
		t.Errorf("devices.json %q is not what the gate reads", d)
	}
	if fi, err := os.Stat(hubDir + "/devices.json"); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("devices.json mode %v %v", fi, err)
	}
	if strings.Contains(w.errb.String(), "warning") {
		t.Errorf("warnings: %s", w.errb.String())
	}
}

// A lock that does not pin the devices gets none of them, whatever up.json lists; one that pins
// them while none is paired gets none either (a phone's decision is refused), and lea says so.
func TestLockHTTPGateDevicesOnlyForALockThatPinsThem(t *testing.T) {
	for name, c := range map[string]struct {
		lock, devices string
		warn          bool
	}{
		"not pinned":  {`,"require_device":["write"],"seal_clear":true`, testDevices, false},
		"none paired": {`,"devices":true,"seal_clear":true`, "", true},
		"empty list":  {`,"devices":true,"seal_clear":true`, `{"v":1,"devices":[]}`, true},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			// The facade keys, but no reveal for this lock: no --facade-keys-file.
			hubUp(t, w, c.lock, testFacades, c.devices)
			if err := w.launcher().ensureProxy(w.proj); err != nil {
				t.Fatal(err)
			}
			run := gateRun(t, w)
			if !strings.Contains(run, "[--hub] [http://lockhub:8443]") {
				t.Fatalf("no hub: %s", run)
			}
			for _, f := range []string{"--devices-file", "--require-device", "--seal-clear", "--facade-keys-file"} {
				if strings.Contains(run, "["+f+"]") {
					t.Errorf("%s: %s", f, run)
				}
			}
			for _, f := range []string{"devices.json", "facades.json"} {
				if _, ok := hubFile(t, w, f); ok {
					t.Errorf("%s written", f)
				}
			}
			if got := strings.Contains(w.errb.String(), "none is paired"); got != c.warn {
				t.Errorf("warning %v: %s", got, w.errb.String())
			}
		})
	}
}

// What up.json says of the hub is checked: anything lea cannot trust stops lea, which says why and
// to run `lox up`: no gate, no proxy on a hub network, no hub input written.
func TestLockHTTPGateRefusesAnUntrustedHub(t *testing.T) {
	for name, c := range map[string]struct {
		mod  func(w *world)
		lock string
		want string
	}{
		"hub network labels": {mod: func(w *world) {
			w.env["FAKE_DOCKER_OUT_NETWORK_INSPECT_AT_"+fakeKey(upProjectOf(w)+"_lockhub")] = `{"com.docker.compose.project":"x"}|10.0.0.0/8`
		}, want: "is not lock up's"},
		"gate volume role": {mod: func(w *world) {
			w.env["FAKE_DOCKER_OUT_VOLUME_INSPECT_AT_"+fakeKey(upProjectOf(w)+"_web-gate")] = `{"dev.lockfamily.role":"socket"}|`
		}, want: "is not lock up's"},
		"socket volume role": {mod: func(w *world) { labels(w, "web", "lockhttp", "gate-home") }, want: "is not lock up's"},
		"no subnet": {mod: func(w *world) {
			k := "FAKE_DOCKER_OUT_NETWORK_INSPECT_AT_" + fakeKey(upProjectOf(w)+"_lockhub")
			w.env[k] = strings.SplitN(w.env[k], "|", 2)[0] + "|"
		}, want: "has no subnet"},
		"bad risk":     {lock: `,"devices":true,"require_device":["--seal-clear"]`, want: "require_device"},
		"unknown risk": {lock: `,"devices":true,"require_device":["admin"]`, want: "require_device"},
		"bad devices":  {lock: `,"devices":true`, mod: func(w *world) { rewriteUp(t, w, `"devices":`, `"devices":[1],"x":`) }, want: "devices: not a JSON object"},
		"bad device": {lock: `,"devices":true`, mod: func(w *world) { rewriteUp(t, w, `"name":"phone"`, `"name":"phone","key":"x"`) },
			want: "devices: not a JSON object"},
		"device id": {lock: `,"devices":true`, mod: func(w *world) { rewriteUp(t, w, `"id":"webauthn:`, `"id":"webauthn:0`) }, want: "devices: "},
		"facade key": {lock: `,"reveal":true`, mod: func(w *world) {
			rewriteUp(t, w, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "dHR5")
		}, want: "facade_keys: "},
		"facade name": {lock: `,"reveal":true`, mod: func(w *world) { rewriteUp(t, w, `"facade": "deck"`, `"facade": "slack"`) }, want: "facade_keys: "},
		"hub key":     {mod: func(w *world) { rewriteUp(t, w, testHubKey, "AAAA") }, want: "not an Ed25519 public key"},
		"hub url":     {mod: func(w *world) { rewriteUp(t, w, "http://lockhub:8443", "https://evil.example") }, want: "not a service on the hub's network"},
		"required none paired": {lock: `,"devices":true,"require_device":["prod-write"]`, mod: func(w *world) { rewriteUp(t, w, `"devices":{`, `"x":{`) },
			want: "require_device prod-write but no device is paired"},
		"bad profile":      {lock: `,"profile":"-x"`, want: "not a profile name"},
		"no gate contract": {mod: func(w *world) { rewriteUp(t, w, `"lockhttp_gate":1,`, "") }, want: "lockhttp_gate 1"},
		"gate contract 2":  {mod: func(w *world) { rewriteUp(t, w, `"lockhttp_gate":1,`, `"lockhttp_gate":2,`) }, want: "lockhttp_gate 1"},
		"hub host":         {mod: func(w *world) { rewriteUp(t, w, "http://lockhub:8443", "http://evil:8443") }, want: "not a service on the hub's network"},
		"hub port":         {mod: func(w *world) { rewriteUp(t, w, "http://lockhub:8443", "http://lockhub:65536") }, want: "not a service on the hub's network"},
		"hub key newline": {mod: func(w *world) { rewriteUp(t, w, testHubKey, testHubKey[:20]+`\r\n`+testHubKey[20:]) },
			want: "not an Ed25519 public key"},
		"network workspace": {mod: func(w *world) {
			k := "FAKE_DOCKER_OUT_NETWORK_INSPECT_AT_" + fakeKey(upProjectOf(w)+"_lockhub")
			w.env[k] = strings.Replace(w.env[k], w.proj, "/elsewhere", 1)
		}, want: "label dev.lockfamily.workspace"},
		"two locks": {mod: func(w *world) {
			rewriteUp(t, w, `"locks":{`, `"locks":{"web2":{"type":"lockhttp","socket_volume":"x"},`)
		}, want: "lea's gate asks for one"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			hubUp(t, w, c.lock, testFacades, testDevices)
			if c.mod != nil {
				c.mod(w)
			}
			err := w.launcher().ensureProxy(w.proj)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "run `lox up`") {
				t.Fatalf("error %v", err)
			}
			if len(gateRuns(w)) != 0 {
				t.Errorf("a gate started:\n%s", strings.Join(w.calls(), "\n"))
			}
			if w.hasCall("[network] [connect]") {
				t.Errorf("the proxy joined a network")
			}
			if _, ok := hubFile(t, w, "hub.pub"); ok {
				t.Error("hub.pub written")
			}
		})
	}
}

func rewriteUp(t *testing.T, w *world, old, new string) {
	t.Helper()
	p := w.proj + "/.lockfamily/up.json"
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), old) {
		t.Fatalf("up.json %s lacks %q (%v)", b, old, err)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The gate runs hub-attached only: without lox up's trusted up.json naming a lockhttp lock, lea
// stops before it changes anything (no proxy, no gate, no hub input) and says to run `lox up`.
func TestLockHTTPGateNeedsLoxUp(t *testing.T) {
	for name, c := range map[string]struct {
		mod  func(t *testing.T, w *world)
		want string
	}{
		"no up.json": {nil, "no trusted"},
		"no lockhttp lock": {func(t *testing.T, w *world) {
			hubUp(t, w, "", "", "")
			rewriteUp(t, w, `"type":"lockhttp"`, `"type":"lockmq"`)
		}, "names no lockhttp lock"},
		"up.json others may write": {func(t *testing.T, w *world) {
			hubUp(t, w, "", "", "")
			if err := os.Chmod(w.proj+"/.lockfamily/up.json", 0o666); err != nil {
				t.Fatal(err)
			}
		}, "no trusted"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorldNoUp(t)
			if c.mod != nil {
				c.mod(t, w)
			}
			err := w.launcher().ensureProxy(w.proj)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "run `lox up`") {
				t.Fatalf("error %v", err)
			}
			for _, c := range w.calls() {
				if strings.HasPrefix(c, "docker [run]") || strings.Contains(c, "[network] [connect]") || strings.Contains(c, "[rm] [-f]") {
					t.Errorf("changed: %s", c)
				}
			}
			if _, ok := hubFile(t, w, "hub.pub"); ok {
				t.Error("hub.pub written")
			}
		})
	}
}

// The proxy already on the hub's network is not joined again. A running gate without the hub's
// stamp (or with another) is replaced; one with it is kept.
func TestLockHTTPGateFollowsTheHub(t *testing.T) {
	image := "ghcr.io/davidgodefroid/lockhttp@" + testDigest
	// lox up's hub: the gate with the hub's stamp is kept, on a proxy already joined.
	w, _ := lockHTTPWorld(t)
	pr := upProjectOf(w)
	g, err := w.launcher().lockUpGate(w.proj)
	if err != nil {
		t.Fatalf("no hub: %v", err)
	}
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^1"
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_PROXY"] = "lea-egress " + pr + "_lockhub "
	for name, c := range map[string]struct {
		label   string
		replace bool
	}{"current": {" hub:" + g.stamp(), false}, "no hub": {"", true}, "other hub": {" hub:0123456789ab", true}} {
		w.env["FAKE_DOCKER_OUT_INSPECT___TYPE"] = "true|2026-10-28T10:00:00Z|\ntrue|2026-10-28T10:00:01Z|" + image + c.label
		os.WriteFile(w.log, nil, 0o644)
		if err := w.launcher().ensureProxy(w.proj); err != nil {
			t.Fatal(err)
		}
		if got := len(gateRuns(w)) == 1; got != c.replace {
			t.Errorf("%s: gate replaced = %v:\n%s", name, got, strings.Join(w.calls(), "\n"))
		}
		if w.hasCall("[network] [connect]") || w.hasCall("[network] [disconnect]") {
			t.Errorf("%s: the proxy's networks changed:\n%s", name, strings.Join(w.calls(), "\n"))
		}
	}
	// A stamp follows what the gate pins: the devices, the facade keys.
	h2 := *g
	h2.facades = []byte(`{"v":1}`)
	if h2.stamp() == g.stamp() {
		t.Error("the stamp ignores the facade keys")
	}
}

// The pinned gate's release decides: before v0.3.0 no hub mode, hence no gate (lea stops); before
// v0.4.0 no device flags; before v0.5.0 no facade keys (each a flag that gate would refuse, and
// stop on); dev (a clone) has them all.
func TestLockHTTPGateFollowsThePinnedRelease(t *testing.T) {
	for _, c := range []struct {
		version               string
		hub, devices, facades bool
	}{
		{"0.2.1", false, false, false}, {"0.3.0", true, false, false}, {"0.4.0", true, true, false},
		{"0.5.0", true, true, true}, {"0.5.0-rc.1", true, true, false}, {"dev", true, true, true},
	} {
		t.Run(c.version, func(t *testing.T) {
			w, root := lockHTTPWorld(t)
			lock := `,"reveal":true,"devices":true`
			if c.devices {
				lock += `,"require_device":["write"],"seal_clear":true`
			}
			hubUp(t, w, lock, testFacades, testDevices)
			if err := os.WriteFile(root+"/usr/local/bin/lockhttp", []byte(fakeLockHTTPLauncher(c.version, testDigest)), 0o755); err != nil {
				t.Fatal(err)
			}
			err := w.launcher().ensureProxy(w.proj)
			if !c.hub {
				if err == nil || !strings.Contains(err.Error(), "has no hub mode") || len(gateRuns(w)) != 0 {
					t.Errorf("no hub mode: %v\n%s", err, strings.Join(w.calls(), "\n"))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			run := gateRun(t, w)
			for flag, want := range map[string]bool{"[--hub]": c.hub, "[--devices-file]": c.devices, "[--seal-clear]": c.devices,
				"[--facade-keys-file]": c.facades} {
				if strings.Contains(run, flag) != want {
					t.Errorf("%s: %v, want %v: %s", flag, !want, want, run)
				}
			}
		})
	}
	// Devices that must sign a risk class, or seal the clear screens, and a release without those
	// flags: no gate.
	for _, lock := range []string{`,"devices":true,"require_device":["write"]`, `,"devices":true,"seal_clear":true`} {
		w, root := lockHTTPWorld(t)
		hubUp(t, w, lock, "", testDevices)
		if err := os.WriteFile(root+"/usr/local/bin/lockhttp", []byte(fakeLockHTTPLauncher("0.3.0", testDigest)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := w.launcher().ensureProxy(w.proj); err == nil || !strings.Contains(err.Error(), "needs lockhttp v0.4.0") || len(gateRuns(w)) != 0 {
			t.Errorf("%s: a gate without its device flags: %v\n%s", lock, err, strings.Join(w.calls(), "\n"))
		}
	}
	for v, want := range map[string]bool{"0.10.0": true, "v0.5.1": true, "1.0.0": true, "0.4.9": false, "": false, "x": false, "0.5": false,
		"0.5.0-rc.1": false, "0.5.1-rc.1": true, "0.5.0+build": true} {
		if got := lockHTTPAtLeast(v, "0.5.0"); got != want {
			t.Errorf("lockHTTPAtLeast(%q) = %v", v, got)
		}
	}
}

// The gate asks the console of the profile lock up runs (its socket is named after it).
func TestLockHTTPGateAsksTheConsolesProfile(t *testing.T) {
	for prof, want := range map[string]string{"": "default", "crm.prod": "crm.prod"} {
		w, _ := lockHTTPWorld(t)
		lock := ""
		if prof != "" {
			lock = `,"profile":"` + prof + `"`
		}
		hubUp(t, w, lock, "", "")
		if err := w.launcher().ensureProxy(w.proj); err != nil {
			t.Fatal(err)
		}
		if run := gateRun(t, w); !strings.Contains(run, "[--profile] ["+want+"]") || !strings.Contains(run, "[--hub]") {
			t.Errorf("%q: %s", prof, run)
		}
	}
	// In the stamp: another profile restarts the gate.
	a, b := gateHub{profile: "a"}, gateHub{profile: "b"}
	if a.stamp() == b.stamp() {
		t.Error("the stamp ignores the profile")
	}
}

// The proxy on a hub network lea cannot take it off, or whose networks lea cannot read: lea stops
// (the gate would not refuse that hub's addresses).
func TestLockHTTPGateStopsOnAHubNetworkItCannotLeave(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"disconnect fails": {"FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_PROXY": "lea-egress lockfamily-0badc0de_lockhub ", "FAKE_DOCKER_RC_NETWORK_DISCONNECT": "1"},
		"inspect fails":    {"FAKE_DOCKER_RC_INSPECT___TYPE_AT_LEA_PROXY": "1"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			for k, v := range env {
				w.env[k] = v
			}
			if err := w.launcher().ensureProxy(w.proj); err == nil {
				t.Fatal("no error")
			}
			if len(gateRuns(w)) != 0 {
				t.Error("the gate started")
			}
		})
	}
}

// A reused proxy joins the hub's network only once the old gate (an earlier lea's, which asked a
// console of its own, or another hub's, which does not refuse this network's addresses) is gone:
// from an earlier lea's gate, and from another hub, which it leaves first.
func TestLockHTTPGateLeavesNoOldGateOnTheHubNetwork(t *testing.T) {
	image := "ghcr.io/davidgodefroid/lockhttp@" + testDigest
	for name, c := range map[string]struct{ nets, label string }{
		"from an earlier lea's gate": {"lea-egress ", ""},
		"from another hub": {"lea-egress lockfamily-0badc0de_lockhub ", " hub:0123456789ab"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			hubUp(t, w, "", "", "")
			w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
			w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^1"
			w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_PROXY"] = c.nets
			w.env["FAKE_DOCKER_OUT_INSPECT___TYPE"] = "true|2026-10-28T10:00:00Z|\ntrue|2026-10-28T10:00:01Z|" + image + c.label
			if err := w.launcher().ensureProxy(w.proj); err != nil {
				t.Fatal(err)
			}
			at := func(sub string) int {
				for i, c := range w.calls() {
					if strings.Contains(c, sub) {
						return i
					}
				}
				return -1
			}
			rm, connect := at("docker [rm] [-f] [lea-lockhttp-gate]"), at("[network] [connect] ["+upProjectOf(w)+"_lockhub] [lea-proxy]")
			run := at("[--name] [lea-lockhttp-gate]")
			if rm < 0 || connect < 0 || run < 0 || !(rm < connect && connect < run) {
				t.Errorf("rm %d, connect %d, run %d:\n%s", rm, connect, run, strings.Join(w.calls(), "\n"))
			}
			if c.label != "" {
				if d := at("[network] [disconnect] [lockfamily-0badc0de_lockhub]"); d < 0 || d > connect {
					t.Errorf("disconnect %d, connect %d", d, connect)
				}
			}
		})
	}
}

// The one gate of a prefix changes whose hub it asks only while no agent of another projects
// directory runs: their requests would be approved by another hub. Agents of this directory only,
// or a gate already asking this hub, switch nothing.
func TestLockHTTPGateDoesNotSwitchUnderAnotherDirectorysAgents(t *testing.T) {
	for name, c := range map[string]struct {
		gateFor  string
		sessions string
		refused  bool
	}{
		"hub to another hub":     {"for=/other|hub=x", "/other\n", true},
		"earlier lea to a hub":   {"for=-|hub=", "/other\n", true},
		"earlier lea, no label":  {"for=|hub=", "/other\n", true},
		"a hub but whose":        {"for=|hub=x", "/other\n", true},
		"unreadable":             {"", "/other\n", true},
		"only this directory":    {"for=/other|hub=x", "PROJ\n", false},
		"no agent runs":          {"for=/other|hub=x", "", false},
		"already this directory": {"for=PROJ|hub=x", "/other\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			w.env["FAKE_DOCKER_OUT_PS__A"] = "lea-lockhttp-gate"
			w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_LOCKHTTP_GATE"] = strings.ReplaceAll(c.gateFor, "PROJ", w.proj)
			w.env["FAKE_DOCKER_OUT_PS___FILTER"] = strings.ReplaceAll(c.sessions, "PROJ", w.proj)
			err := w.launcher().ensureProxy(w.proj)
			if got := err != nil; got != c.refused {
				t.Fatalf("refused = %v (%v)", got, err)
			}
			if c.refused {
				if !strings.Contains(err.Error(), "the agents of /other run") || len(gateRuns(w)) != 0 || w.hasCall("[network] [connect]") ||
					w.hasCall("docker [rm] [-f] [lea-lockhttp-gate]") {
					t.Errorf("%v:\n%s", err, strings.Join(w.calls(), "\n"))
				}
			} else if len(gateRuns(w)) != 1 {
				t.Errorf("no gate:\n%s", strings.Join(w.calls(), "\n"))
			}
		})
	}
}

// The old gate that cannot be removed stops lea before the proxy joins the hub's network; a
// current gate on a proxy not yet joined gets the network, and runs on.
func TestLockHTTPGateJoinsOnlyBehindAGateThatRefusesTheHub(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	hubUp(t, w, "", "", "")
	w.env["FAKE_DOCKER_RC_RM__F_AT_LEA_LOCKHTTP_SIGNER"] = "1"
	if err := w.launcher().ensureProxy(w.proj); err == nil || w.hasCall("[network] [connect]") || len(gateRuns(w)) != 0 {
		t.Fatalf("%v:\n%s", err, strings.Join(w.calls(), "\n"))
	}

	w, _ = lockHTTPWorld(t)
	g, err := w.launcher().lockUpGate(w.proj)
	if err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/davidgodefroid/lockhttp@" + testDigest
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	w.env["FAKE_DOCKER_OUT_INSPECT___FORMAT"] = "@" + w.launcher().proxyRules() + "^1"
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_PROXY"] = "lea-egress "
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE"] = "true|2026-10-28T10:00:00Z|\ntrue|2026-10-28T10:00:01Z|" + image + " hub:" + g.stamp()
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [network] [connect] ["+upProjectOf(w)+"_lockhub] [lea-proxy]") || len(gateRuns(w)) != 0 {
		t.Errorf("calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}

// Whether the gate exists, and its labels, lea must read before it switches it: a failure stops
// lea, but a gate that does not exist switches nothing.
func TestLockHTTPGateSwitchFailsClosed(t *testing.T) {
	for name, c := range map[string]struct {
		env     map[string]string
		refused bool
	}{
		"ps fails":      {map[string]string{"FAKE_DOCKER_RC_PS__A": "1"}, true},
		"inspect fails": {map[string]string{"FAKE_DOCKER_OUT_PS__A": "lea-lockhttp-gate", "FAKE_DOCKER_RC_INSPECT___TYPE_AT_LEA_LOCKHTTP_GATE": "1"}, true},
		"no gate":       {map[string]string{"FAKE_DOCKER_OUT_PS__A": ""}, false},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			hubUp(t, w, "", "", "")
			w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "/other"
			for k, v := range c.env {
				w.env[k] = v
			}
			err := w.launcher().ensureProxy(w.proj)
			if (err != nil) != c.refused || (c.refused && len(gateRuns(w)) != 0) {
				t.Errorf("%v:\n%s", err, strings.Join(w.calls(), "\n"))
			}
		})
	}
}

// A refused switch changes nothing: the running gate's inputs, the proxy's networks, the proxy
// itself (stale or not). A start in progress in another projects directory (its lock) refuses
// it too.
func TestLockHTTPGateRefusedSwitchChangesNothing(t *testing.T) {
	w, _ := lockHTTPWorld(t)
	hubDir := filepath.Join(lockhttp.GateDir(w.launcher().Cfg.DataDir()), "hub")
	if err := os.MkdirAll(hubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{"hub.pub": "other hub key\n", "devices.json": `{"other":1}`}
	for n, b := range inputs {
		if err := os.WriteFile(filepath.Join(hubDir, n), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The gate asks /other's hub, whose agents run; the proxy is on /other's hub network, and
	// stale (another image).
	w.env["FAKE_DOCKER_OUT_PS__A"] = "lea-lockhttp-gate"
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_LOCKHTTP_GATE"] = "for=/other|hub=x"
	w.env["FAKE_DOCKER_OUT_PS___FILTER"] = "/other"
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_PROXY"] = "lea-egress lockfamily-0123abcd_lockhub "
	w.env["FAKE_DOCKER_OUT_INSPECT__F"] = "true"
	if err := w.launcher().ensureProxy(w.proj); err == nil {
		t.Fatal("not refused")
	}
	for n, b := range inputs {
		if got, err := os.ReadFile(filepath.Join(hubDir, n)); err != nil || string(got) != b {
			t.Errorf("%s: %q %v", n, got, err)
		}
	}
	for _, c := range w.calls() {
		if strings.Contains(c, "[network] [disconnect]") || strings.Contains(c, "[rm] [-f]") || strings.HasPrefix(c, "docker [run]") {
			t.Errorf("changed: %s", c)
		}
	}

	// Another directory's start in progress: its lock.
	w, _ = lockHTTPWorld(t)
	hubUp(t, w, "", "", "")
	w.env["FAKE_DOCKER_OUT_PS__A"] = "lea-lockhttp-gate"
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_LOCKHTTP_GATE"] = "for=-|hub="
	if err := os.MkdirAll(w.hostDir()+"/locks/0123456789", 0o777); err != nil {
		t.Fatal(err)
	}
	if err := w.launcher().ensureProxy(w.proj); err == nil || !strings.Contains(err.Error(), "starting in another one") || len(gateRuns(w)) != 0 {
		t.Fatalf("%v:\n%s", err, strings.Join(w.calls(), "\n"))
	}
	// This directory's own lock (lea holds it while it starts) is no other start.
	os.Remove(w.hostDir() + "/locks/0123456789")
	if err := os.MkdirAll(w.hostDir()+"/locks/"+w.id(), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
}
