package launcher

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// testHubKey is an Ed25519 public key (32 bytes) in base64, as up.json's hub_key.
var testHubKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

const (
	testFacades = `{"v": 1, "facades": [{"facade": "tty", "key": "dHR5"}, {"facade": "deck", "key": "ZGVjaw=="}]}`
	testDevices = `{"v": 1, "devices": [{"id": "phone-1", "key": "cGhvbmU="}]}`
)

// hubUp writes lock up's up.json with the hub and the lockhttp lock web (its extra fields: lock),
// with facade keys and devices when given, and makes the fake docker answer lock up's labels of
// its volumes and network.
func hubUp(t *testing.T, w *world, lock, facades, devices string) {
	t.Helper()
	d := w.proj + "/.lockfamily"
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	pr := upProjectOf(w)
	s := `{"version":1,"project":"` + pr + `","workspace":"` + w.proj + `","hub_network":"` + pr + `_lockhub",` +
		`"hub_url":"http://lockhub:8443","hub_key":"` + testHubKey + `",`
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
		`","com.docker.compose.network":"lockhub","dev.lockfamily.role":"hub"}|172.30.0.0/16 fd00:30::/64 `
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
	hubUp(t, w, `,"devices":true,"require_device":["prod-write","write"],"seal_clear":true`, testFacades, testDevices)
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
	if f, _ := hubFile(t, w, "facades.json"); !strings.Contains(f, `"facade":"deck"`) || strings.Contains(f, " ") {
		t.Errorf("facades.json %q", f)
	}
	if d, _ := hubFile(t, w, "devices.json"); !strings.Contains(d, `"id":"phone-1"`) {
		t.Errorf("devices.json %q", d)
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
		"none paired": {`,"devices":true,"require_device":["write"],"seal_clear":true`, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			hubUp(t, w, c.lock, "", c.devices)
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
			if _, ok := hubFile(t, w, "devices.json"); ok {
				t.Error("devices.json written")
			}
			if got := strings.Contains(w.errb.String(), "none is paired"); got != c.warn {
				t.Errorf("warning %v: %s", got, w.errb.String())
			}
		})
	}
}

// What up.json says of the hub is checked: anything lea cannot trust leaves the gate as without
// lock up (its own console, no hub, the proxy on no hub network), and lea says why.
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
		"bad risk":    {lock: `,"devices":true,"require_device":["--seal-clear"]`, want: "not a risk class"},
		"bad devices": {lock: `,"devices":true`, mod: func(w *world) { rewriteUp(t, w, `"devices":`, `"devices":[1],"x":`) }, want: "devices: not a JSON object"},
		"hub key":     {mod: func(w *world) { rewriteUp(t, w, testHubKey, "AAAA") }, want: "not an Ed25519 public key"},
		"hub url":     {mod: func(w *world) { rewriteUp(t, w, "http://lockhub:8443", "https://evil.example") }, want: "not a service on the hub's network"},
		"two locks": {mod: func(w *world) {
			rewriteUp(t, w, `"locks":{`, `"locks":{"web2":{"type":"lockhttp","socket_volume":"x"},`)
		}, want: "lea's gate asks for one"},
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := lockHTTPWorld(t)
			hubUp(t, w, c.lock, "", testDevices)
			if c.mod != nil {
				c.mod(w)
			}
			if err := w.launcher().ensureProxy(w.proj); err != nil {
				t.Fatal(err)
			}
			run := gateRun(t, w)
			if strings.Contains(run, "[--hub]") || !strings.Contains(run, "src=lockhttp-run,dst=/run/lockhttp") || strings.Contains(run, "/lockhttp-home") {
				t.Errorf("hub mode: %s", run)
			}
			if w.hasCall("[network] [connect]") {
				t.Errorf("the proxy joined a network")
			}
			if !strings.Contains(w.errb.String(), c.want) {
				t.Errorf("warning: %s", w.errb.String())
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

// Without lock up, the proxy leaves a hub network an earlier gate joined; with it, the proxy
// already on the hub's network is not joined again. A running gate without the hub's stamp (or
// with another) is replaced; one with it is kept.
func TestLockHTTPGateFollowsTheHub(t *testing.T) {
	image := "ghcr.io/davidgodefroid/lockhttp@" + testDigest
	w, _ := lockHTTPWorld(t)
	stale := "lockfamily-0badc0de_lockhub"
	w.env["FAKE_DOCKER_OUT_INSPECT___TYPE_AT_LEA_PROXY"] = "lea-egress " + stale + " "
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("docker [network] [disconnect] [" + stale + "] [lea-proxy]") {
		t.Errorf("the proxy stays on %s:\n%s", stale, strings.Join(w.calls(), "\n"))
	}

	// lock up's hub: the gate with the hub's stamp is kept, on a proxy already joined.
	w, _ = lockHTTPWorld(t)
	hubUp(t, w, "", "", "")
	pr := upProjectOf(w)
	g := w.launcher().lockUpGate(w.proj)
	if g == nil {
		t.Fatalf("no hub: %s", w.errb.String())
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
