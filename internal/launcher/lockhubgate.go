package launcher

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lockkit/ticket"
)

// LockHTTP's gate asks the hub (lockhub's docs/lox-up.md, "Why lockhttp is different"): the gate
// runs hub-attached only, for the lockhttp lock `lox up` runs for the projects directory. lea's
// gate (beside lea's proxy, in its network namespace) mounts that lock's socket volume on
// /run/lockhttp and its gate volume as its --home, reads the hub's key, the host facades' keys
// and, for a lock that pins them (locks.<name>.devices), the approval devices from lea's own
// read-only copies under /run/lockfamily, and only the proxy joins the hub's network, whose
// addresses the gate refuses as targets: no agent reaches the hub through it. Everything up.json
// names is checked first (the volumes' and the network's labels, the key's form); anything lea
// cannot trust, or no up.json naming a lockhttp lock, stops lea with why, and says to run `lox
// up`: there is no gate without the hub.

// gateHubDir is where the gate reads lea's copies of the hub's inputs (read-only).
const gateHubDir = "/run/lockfamily"

// gateHub is what the gate needs to ask the hub: the lock's instance and volumes, the hub's URL,
// network (its subnets) and key, and the facade keys and devices the gate pins.
type gateHub struct {
	instance, url, network string
	profile                string
	socket, home           string
	subnets                []string
	hubKey                 string
	facades, devices       []byte
	requireDevice          []string
	sealClear              bool
}

// gateProfile is a console profile's name (lockhub's lockfamily.yaml rule).
var gateProfile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// gateHubURL is the hub on its network: lox up's service lockhub (a name no resolver outside the
// network is asked for), on a port.
var gateHubURL = regexp.MustCompile(`^http://lockhub:([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$`)

// lockUpGate is what the gate needs to ask the hub of the projects directory proj; an error (why,
// and to run `lox up`) without lox up's trusted up.json naming a lockhttp lock, or when lea cannot
// trust what it says of it.
func (l *Launcher) lockUpGate(proj string) (*gateHub, error) {
	lf, err := lockfamilyDir(proj)
	if err != nil {
		return nil, err
	}
	s, p := l.readLockUp(proj, lf)
	if s == nil {
		return nil, die("LEA_LOCKHTTP=1: no trusted %s naming a lockhttp lock: run `lox up` (the LockHTTP gate asks its hub only)", p)
	}
	var names []string
	for n, lk := range s.Locks {
		if lk.Type == "lockhttp" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, die("LEA_LOCKHTTP=1: %s names no lockhttp lock: add one (lox add lockhttp <name>) and run `lox up` (the LockHTTP gate asks its hub only)", p)
	}
	sort.Strings(names)
	warn := func(why string) (*gateHub, error) {
		return nil, die("LEA_LOCKHTTP=1: %s: lockhttp: %s: lea's LockHTTP gate cannot ask the hub, and asks nothing else (fix it, then run `lox up`)", p, why)
	}
	if len(names) > 1 {
		return warn(fmt.Sprintf("lox up runs %q: lea's gate asks for one", names))
	}
	// lox up says what the gate pins (its devices, required and sealed, its reveals) only from
	// lockhttp_gate 1 on: before, a lock's devices: true in lockfamily.yaml would not reach the
	// gate, and it would take the hub's approvals the human meant to be device-signed.
	if s.LockHTTPGate != 1 {
		return warn("lox up does not say what this lock's gate pins (lockhttp_gate 1; update lockhub)")
	}
	name, lk := names[0], s.Locks[names[0]]
	if !lockUpInstance.MatchString(name) {
		return warn("not an instance name")
	}
	if lk.Socket != s.Project+"_"+name+"-run" || lk.GateHome != s.Project+"_"+name+"-gate" {
		return warn("its volumes are not lox up's")
	}
	if why := l.lockUpVolumeWrong(lk.Socket, s.Project, proj, name, "lockhttp"); why != "" {
		return warn(why)
	}
	if why, _ := l.lockUpLabelsWrong("volume", lk.GateHome, "", map[string]string{"com.docker.compose.project": s.Project,
		"com.docker.compose.volume": name + "-gate", "dev.lockfamily.role": "gate-home", "dev.lockfamily.instance": name,
		"dev.lockfamily.type": "lockhttp", "dev.lockfamily.workspace": proj}); why != "" {
		return warn(why)
	}
	if s.HubNetwork != s.Project+"_lockhub" {
		return warn(fmt.Sprintf("hub network %q is not lox up's", s.HubNetwork))
	}
	why, subnets := l.lockUpLabelsWrong("network", s.HubNetwork, "{{range .IPAM.Config}}{{.Subnet}} {{end}}", map[string]string{
		"com.docker.compose.project": s.Project, "com.docker.compose.network": "lockhub", "dev.lockfamily.role": "hub",
		"dev.lockfamily.workspace": proj})
	if why != "" {
		return warn(why)
	}
	if !gateHubURL.MatchString(s.HubURL) {
		return warn(fmt.Sprintf("hub URL %q is not a service on the hub's network", s.HubURL))
	}
	// Exactly the key's base64 on one line, as lockkit's ReadHubKey takes it: Go's decoder skips
	// a CR or LF the gate refuses.
	if k, err := base64.StdEncoding.DecodeString(s.HubKey); err != nil || len(k) != ed25519.PublicKeySize ||
		base64.StdEncoding.EncodeToString(k) != s.HubKey {
		return warn("the hub key is not an Ed25519 public key")
	}
	// The pinned gate's release: a flag it does not know would stop it (a usage error, again and
	// again under --restart): hub mode from v0.3.0, the devices from v0.4.0, reveals from v0.5.0.
	ver := lockHTTPLauncherVersion()
	if !lockHTTPAtLeast(ver, "0.3.0") {
		return warn("the pinned lockhttp " + orUnknown(ver) + " has no hub mode (v0.3.0 or later)")
	}
	// The console's profile, as lox up runs it (its socket's name derives from it).
	prof := lk.Profile
	if prof == "" {
		prof = "default"
	}
	if !gateProfile.MatchString(prof) {
		return warn(fmt.Sprintf("profile %q is not a profile name", prof))
	}
	g := &gateHub{instance: name, url: s.HubURL, network: s.HubNetwork, socket: lk.Socket, home: lk.GateHome, hubKey: s.HubKey, profile: prof}
	for _, f := range strings.Fields(subnets) {
		pf, err := netip.ParsePrefix(f)
		if err != nil {
			return warn("subnet " + f + " of " + s.HubNetwork)
		}
		g.subnets = append(g.subnets, pf.Masked().String())
	}
	if len(g.subnets) == 0 {
		return warn(s.HubNetwork + " has no subnet")
	}
	// The facade keys only for a lock lox up lets reveal (locks.<name>.reveal), checked as the gate
	// reads them, written in canonical form.
	if lk.Reveal && len(s.FacadeKeys) > 0 && string(s.FacadeKeys) != "null" {
		if !lockHTTPAtLeast(ver, "0.5.0") {
			l.logf("warning: %s: lockhttp: the pinned lockhttp %s cannot pin facade keys (v0.5.0 or later): no reveal (Ctrl-O) of its tickets", p, orUnknown(ver))
		} else if g.facades, err = facadesFile(s.FacadeKeys); err != nil {
			return warn("facade_keys: " + err.Error())
		}
	}
	if lk.DevicesOn {
		for _, r := range lk.RequireDevice {
			if _, err := ticket.ParseRisk(r); err != nil {
				return warn("require_device: " + err.Error())
			}
		}
		if g.devices, err = devicesFile(s.Devices); err != nil {
			return warn("devices: " + err.Error())
		}
		// Risk classes that must be device-signed, and lea cannot say so to the gate: no gate, or
		// it would take the hub's approvals of them from any facade (lox up refuses that too).
		switch {
		case g.devices == nil && len(lk.RequireDevice) > 0:
			return warn("require_device " + strings.Join(lk.RequireDevice, ",") + " but no device is paired")
		case g.devices == nil:
			// No device paired yet: the gate pins none, and a device's decision fails closed.
			l.logf("warning: %s: lockhttp: the lock pins the paired devices, but none is paired: decisions from a phone are refused", p)
		case !lockHTTPAtLeast(ver, "0.4.0") && (len(lk.RequireDevice) > 0 || lk.SealClear):
			// Unsealed clear screens to a hub that routes them to the phones, or approvals taken
			// from any facade: no gate.
			return warn("require_device or seal_clear needs lockhttp v0.4.0 or later, and the pinned one is " + orUnknown(ver))
		case !lockHTTPAtLeast(ver, "0.4.0"):
			g.devices = nil
			l.logf("warning: %s: lockhttp: the pinned lockhttp %s cannot pin devices (v0.4.0 or later): the lock pins none", p, orUnknown(ver))
		default:
			g.requireDevice, g.sealClear = lk.RequireDevice, lk.SealClear
		}
	}
	return g, nil
}

// facadesFile is up.json's facade keys as the gate reads them (ticket.ParseFacades), in canonical
// form; an error for anything it would refuse.
func facadesFile(raw json.RawMessage) ([]byte, error) {
	if len(raw) > 64<<10 {
		return nil, errTooLarge
	}
	fs, err := ticket.ParseFacades(raw)
	if err != nil {
		return nil, err
	}
	set := ticket.FacadeSet{V: ticket.Version, Facades: []ticket.FacadeKey{}}
	for _, f := range fs {
		set.Facades = append(set.Facades, ticket.FacadeKey{Facade: f.Facade, Key: base64.StdEncoding.EncodeToString(f.Key)})
	}
	b, err := ticket.Canonical(set)
	if err != nil {
		return nil, err
	}
	if _, err := ticket.ParseFacades(b); err != nil {
		return nil, err
	}
	return b, nil
}

// devicesFile is up.json's paired devices as the gate reads them (ticket.ParseDevices: canonical
// form only), nil when none is paired; an error for a device the gate would refuse.
func devicesFile(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if len(raw) > 64<<10 {
		return nil, errTooLarge
	}
	var set ticket.DeviceSet
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&set); err != nil || dec.More() {
		return nil, errNotObject
	}
	if set.V != ticket.Version {
		return nil, gateErr("unsupported version")
	}
	if len(set.Devices) == 0 {
		return nil, nil
	}
	for i := range set.Devices {
		if err := set.Devices[i].Validate(); err != nil {
			return nil, err
		}
	}
	b, err := ticket.MarshalDevices(set.Devices)
	if err != nil {
		return nil, err
	}
	if _, err := ticket.ParseDevices(b); err != nil {
		return nil, err
	}
	return b, nil
}

// lockHTTPLauncherVersion is the launcher_version of lockhttp's host launcher, "" when unknown.
func lockHTTPLauncherVersion() string {
	f, err := os.Open(lockHTTPLauncher())
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	if v := lockHTTPVersionLine.FindSubmatch(head[:n]); v != nil {
		return string(v[1])
	}
	return ""
}

// lockHTTPAtLeast reports whether release v (lockhttp's launcher_version: 0.4.0, v0.4.0, dev for
// a clone) is min or later; a pre-release of min is not.
func lockHTTPAtLeast(v, min string) bool {
	if v == "dev" {
		return true
	}
	pre := strings.ContainsRune(strings.SplitN(v, "+", 2)[0], '-')
	parse := func(s string) ([3]int, bool) {
		s = strings.TrimPrefix(s, "v")
		if i := strings.IndexAny(s, "-+"); i >= 0 {
			s = s[:i]
		}
		var out [3]int
		f := strings.Split(s, ".")
		if len(f) != 3 {
			return out, false
		}
		for i, x := range f {
			n, err := strconv.Atoi(x)
			if err != nil || n < 0 {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	a, ok1 := parse(v)
	b, ok2 := parse(min)
	if !ok1 || !ok2 {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return !pre // a pre-release comes before its release
}

func orUnknown(v string) string {
	if v == "" {
		return "(version unknown)"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

// lockUpLabelsWrong is why the docker object kind name lacks one of labels, or "", and what the
// template more says of it (after its labels, in the same inspect).
func (l *Launcher) lockUpLabelsWrong(kind, name, more string, labels map[string]string) (string, string) {
	o, err := l.output(false, "docker", kind, "inspect", "--format", "{{json .Labels}}|"+more, name)
	if err != nil {
		return "no " + kind + " " + name + " (run lox up)", ""
	}
	js, rest, _ := strings.Cut(o, "|")
	var lb map[string]string
	if json.Unmarshal([]byte(js), &lb) != nil {
		return "cannot read the labels of " + name, ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if lb[k] != labels[k] {
			return kind + " " + name + " is not lox up's (label " + k + ")", ""
		}
	}
	return "", rest
}

type gateErr string

func (e gateErr) Error() string { return string(e) }

const (
	errNotObject = gateErr("not a JSON object")
	errTooLarge  = gateErr("larger than 64 KiB")
)

// files are the gate's inputs in lea's copy, by name under /run/lockfamily.
func (g *gateHub) files() map[string][]byte {
	f := map[string][]byte{"hub.pub": []byte(g.hubKey + "\n")}
	if g.facades != nil {
		f["facades.json"] = g.facades
	}
	if g.devices != nil {
		f["devices.json"] = g.devices
	}
	return f
}

// args are the gate's hub flags.
func (g *gateHub) args() []string {
	a := []string{"--hub", g.url, "--hub-plain-http", "--hub-key-file", gateHubDir + "/hub.pub",
		"--instance", g.instance, "--home", "/lockhttp-home"}
	for _, s := range g.subnets {
		a = append(a, "--hub-net", s)
	}
	if g.facades != nil {
		a = append(a, "--facade-keys-file", gateHubDir+"/facades.json")
	}
	if g.devices != nil {
		a = append(a, "--devices-file", gateHubDir+"/devices.json")
		if len(g.requireDevice) > 0 {
			a = append(a, "--require-device", strings.Join(g.requireDevice, ","))
		}
		if g.sealClear {
			a = append(a, "--seal-clear")
		}
	}
	return a
}

// stamp names what the gate runs with in hub mode (its label): a change restarts it.
func (g *gateHub) stamp() string {
	if g == nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(g.socket + "\x00" + g.home + "\x00" + g.network + "\x00" + g.profile + "\x00" + strings.Join(g.args(), "\x00")))
	fs := g.files()
	names := make([]string, 0, len(fs))
	for n := range fs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte("\x00" + n + "\x00"))
		h.Write(fs[n])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// writeGateHub writes the gate's inputs in lea's data directory (a directory of their own, the
// gate's read-only /run/lockfamily), and removes those g no longer has; nil g empties it.
func (l *Launcher) writeGateHub(g *gateHub) (string, error) {
	data := l.Cfg.DataDir()
	dir := filepath.Join(lockhttp.GateDir(data), "hub")
	if err := Mkdirs(data, dir); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", err
	}
	var fs map[string][]byte
	if g != nil {
		fs = g.files()
	}
	for _, n := range []string{"hub.pub", "facades.json", "devices.json"} {
		p := filepath.Join(dir, n)
		b, ok := fs[n]
		if !ok {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			continue
		}
		if err := WriteFile(data, p, b); err != nil {
			return "", err
		}
		if err := os.Chmod(p, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// joinHubNetwork disconnects the proxy from any lox up hub network but g's and, with connect,
// connects it (only it: the gate shares its namespace) to g's; nil g leaves none.
func (l *Launcher) joinHubNetwork(proxy string, g *gateHub, connect bool) error {
	// The proxy (the gate's namespace) on a hub network whose subnets the gate does not refuse
	// would let an agent reach that hub: what cannot be read or left stops lea.
	o, err := l.output(false, "docker", "inspect", "--type", "container", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", proxy)
	if err != nil {
		return die("LEA_LOCKHTTP=1: cannot read the networks of %s", proxy)
	}
	joined := false
	for _, n := range strings.Fields(o) {
		switch {
		case g != nil && n == g.network:
			joined = true
		case lockUpHubNet.MatchString(n):
			if err := l.quiet("docker", "network", "disconnect", n, proxy); err != nil {
				return die("LEA_LOCKHTTP=1: could not take %s off the hub network %s", proxy, n)
			}
		}
	}
	if g == nil || joined || !connect {
		return nil
	}
	if err := l.run("docker", "network", "connect", g.network, proxy); err != nil {
		return die("LEA_LOCKHTTP=1: could not join %s to the hub's network %s", proxy, g.network)
	}
	return nil
}

var lockUpHubNet = regexp.MustCompile(`^lockfamily-[0-9a-f]{8}_lockhub$`)

// staleGate is err (lea cannot run the gate under lox up: it stops before it changes anything),
// told with what still runs: a standalone gate of an earlier lea (no lea.lockhttp-hub label: it
// asks a console of its own, on lockhttp-run, not lox up's hub) keeps serving the agents that
// run. lea removes it only when it is lea's own (its prefix and role labels) and no lea agent of
// this prefix runs, so that nothing goes through it; with agents running it stays (removing it
// would cut their web in the middle of their work) and lea says which agents and how to stop it.
func (l *Launcher) staleGate(err error) error {
	name := l.lockHTTPGateName()
	o, ierr := l.output(false, "docker", "inspect", "--type", "container", "-f",
		`{{.Id}}|{{.State.Running}}|{{index .Config.Labels "lea.prefix"}}|{{index .Config.Labels "lea.role"}}|{{index .Config.Labels "lea.lockhttp-hub"}}|{{range .Mounts}}{{if eq .Destination "/run/lockhttp"}}{{.Name}}{{end}}{{end}}`, name)
	f := strings.Split(strings.TrimSpace(o), "|")
	if ierr != nil || len(f) != 6 || f[1] != "true" || f[4] != "" {
		return err // no gate, a stopped one, or one that asks a hub: nothing serves without lox up
	}
	if f[2] != l.prefix() || f[3] != "lockhttp-gate" || !dockerID.MatchString(f[0]) {
		return die("%v. The container %s runs, and it is not a gate lea started (labels lea.prefix, lea.role): lea leaves it alone; look at it (docker inspect %s)", dieMsg(err), name, name)
	}
	gateID, vol := f[0], f[5]
	if vol == "" {
		vol = "none"
	}
	ps, perr := l.output(false, "docker", "ps", "--filter", "label=lea.prefix="+l.prefix(), "--filter", "label=lea.role=session",
		"--format", `{{.Names}} {{.Label "lea.projects"}}`)
	var running []string
	for _, ln := range strings.Split(ps, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			running = append(running, ln)
		}
	}
	if perr == nil && len(running) == 0 {
		// By the id lea inspected (never the name, which another container could take meanwhile);
		// its signer only when it is lea's too, each removal told on its own.
		if rerr := l.quiet("docker", "rm", "-f", gateID); rerr != nil {
			return die("%s. The standalone LockHTTP gate %s of an earlier lea still runs (it asks the console of the volume %s, not a hub), and lea could not remove it: docker rm -f %s", dieMsg(err), name, vol, gateID)
		}
		l.logf("removed the standalone LockHTTP gate %s of an earlier lea (it asked the console of the volume %s, not a hub); no agent ran through it", name, vol)
		signer := l.lockHTTPSignerName()
		so, serr := l.output(false, "docker", "inspect", "--type", "container", "-f",
			`{{.Id}}|{{index .Config.Labels "lea.prefix"}}|{{index .Config.Labels "lea.role"}}`, signer)
		if g := strings.Split(strings.TrimSpace(so), "|"); serr == nil && len(g) == 3 {
			switch {
			case g[1] != l.prefix() || g[2] != "lockhttp-signer" || !dockerID.MatchString(g[0]):
				l.logf("warning: %s is not a signer lea started (labels lea.prefix, lea.role): left alone", signer)
			case l.quiet("docker", "rm", "-f", g[0]) != nil:
				l.logf("warning: could not remove its signer %s: docker rm -f %s", signer, g[0])
			default:
				l.logf("removed its signer %s", signer)
			}
		}
		return err
	}
	who := "lea's agents"
	if perr != nil {
		who = "lea's agents (lea could not list them)"
	} else if len(running) > 0 {
		who = "the agents of " + strings.Join(running, ", ")
	}
	return die("%s. Meanwhile the standalone LockHTTP gate %s of an earlier lea still runs: it asks the console of the volume %s, not lox up's hub, for %s; lea leaves it, since removing it would cut their web. Once `lox up` runs, lea stop --all (it stops the gate with the proxy), then lea",
		dieMsg(err), name, vol, who)
}

// dieMsg is the message of an error lea dies with, without its wrapping.
func dieMsg(err error) string {
	var d dieError
	if errors.As(err, &d) {
		return d.msg
	}
	return err.Error()
}
