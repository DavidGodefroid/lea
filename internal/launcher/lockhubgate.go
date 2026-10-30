package launcher

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DavidGodefroid/lea/internal/lockhttp"
)

// LockHTTP's gate in hub mode (lockhub's docs/lock-up.md, "Why lockhttp is different"): when lock
// up runs a lockhttp lock for the projects directory, lea's gate (beside lea's proxy, in its
// network namespace) asks the hub instead of a console of its own. It mounts that lock's socket
// volume on /run/lockhttp and its gate volume as its --home, reads the hub's key, the host
// facades' keys and, for a lock that pins them (locks.<name>.devices), the approval devices from
// lea's own read-only copies under /run/lockfamily, and only the proxy joins the hub's network,
// whose addresses the gate refuses as targets: no agent reaches the hub through it. Everything
// up.json names is checked first (the volumes' and the network's labels, the key's form); lea
// then runs the standalone gate, as without lock up, and says why.

// gateHubDir is where the gate reads lea's copies of the hub's inputs (read-only).
const gateHubDir = "/run/lockfamily"

// gateHub is what the gate needs to ask the hub: the lock's instance and volumes, the hub's URL,
// network (its subnets) and key, and the facade keys and devices the gate pins.
type gateHub struct {
	instance, url, network string
	socket, home           string
	subnets                []string
	hubKey                 string
	facades, devices       []byte
	requireDevice          []string
	sealClear              bool
}

var (
	gateHubURL = regexp.MustCompile(`^http://[a-z][a-z0-9-]{0,62}:[0-9]{1,5}$`)
	gateRisk   = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
)

// lockUpGate is the hub mode of the gate for the projects directory proj, nil without lock up, a
// lockhttp lock in it, or when lea cannot trust what up.json says of it (lea says why).
func (l *Launcher) lockUpGate(proj string) *gateHub {
	lf, err := lockfamilyDir(proj)
	if err != nil {
		return nil
	}
	s, p := l.readLockUp(proj, lf)
	if s == nil {
		return nil
	}
	var names []string
	for n, lk := range s.Locks {
		if lk.Type == "lockhttp" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	warn := func(why string) *gateHub {
		l.logf("warning: %s: lockhttp: %s: lea's LockHTTP gate does not ask the hub (it asks a lockhttp console of its own)", p, why)
		return nil
	}
	if len(names) > 1 {
		return warn("lock up runs " + strings.Join(names, ", ") + ": lea's gate asks for one")
	}
	name, lk := names[0], s.Locks[names[0]]
	if !lockUpInstance.MatchString(name) {
		return warn("not an instance name")
	}
	if lk.Socket != s.Project+"_"+name+"-run" || lk.GateHome != s.Project+"_"+name+"-gate" {
		return warn("its volumes are not lock up's")
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
		return warn("hub network " + s.HubNetwork + " is not lock up's")
	}
	why, subnets := l.lockUpLabelsWrong("network", s.HubNetwork, "{{range .IPAM.Config}}{{.Subnet}} {{end}}", map[string]string{
		"com.docker.compose.project": s.Project, "com.docker.compose.network": "lockhub", "dev.lockfamily.role": "hub"})
	if why != "" {
		return warn(why)
	}
	if !gateHubURL.MatchString(s.HubURL) {
		return warn("hub URL " + s.HubURL + " is not a service on the hub's network")
	}
	if k, err := base64.StdEncoding.DecodeString(s.HubKey); err != nil || len(k) != ed25519.PublicKeySize {
		return warn("the hub key is not an Ed25519 public key")
	}
	g := &gateHub{instance: name, url: s.HubURL, network: s.HubNetwork, socket: lk.Socket, home: lk.GateHome, hubKey: s.HubKey}
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
	if g.facades, err = compactJSON(s.FacadeKeys); err != nil {
		return warn("facade_keys: " + err.Error())
	}
	if lk.DevicesOn {
		if g.devices, err = compactJSON(s.Devices); err != nil {
			return warn("devices: " + err.Error())
		}
		for _, r := range lk.RequireDevice {
			if !gateRisk.MatchString(r) {
				return warn("require_device: not a risk class")
			}
		}
		if g.devices == nil {
			// No device paired yet: the gate pins none, and a device's decision fails closed.
			l.logf("warning: %s: lockhttp: the lock pins the paired devices, but none is paired: decisions from a phone are refused", p)
		} else {
			g.requireDevice, g.sealClear = lk.RequireDevice, lk.SealClear
		}
	}
	return g
}

// lockUpLabelsWrong is why the docker object kind name lacks one of labels, or "", and what the
// template more says of it (after its labels, in the same inspect).
func (l *Launcher) lockUpLabelsWrong(kind, name, more string, labels map[string]string) (string, string) {
	o, err := l.output(false, "docker", kind, "inspect", "--format", "{{json .Labels}}|"+more, name)
	if err != nil {
		return "no " + kind + " " + name + " (run lock up)", ""
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
			return kind + " " + name + " is not lock up's (label " + k + ")", ""
		}
	}
	return "", rest
}

// compactJSON is a JSON object of up.json, compact, for a file the gate reads (64 KiB at most, as
// lockkit reads it); nil when absent.
func compactJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil || b.Len() == 0 || b.Bytes()[0] != '{' {
		return nil, errNotObject
	}
	if b.Len() > 64<<10 {
		return nil, errTooLarge
	}
	return b.Bytes(), nil
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
	h.Write([]byte(g.socket + "\x00" + g.home + "\x00" + g.network + "\x00" + strings.Join(g.args(), "\x00")))
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

// joinHubNetwork connects the proxy (only it: the gate shares its namespace) to the hub's
// network of g, and disconnects it from any other lock up hub network; nil g leaves none.
func (l *Launcher) joinHubNetwork(proxy string, g *gateHub) error {
	o, _ := l.output(false, "docker", "inspect", "--type", "container", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", proxy)
	joined := false
	for _, n := range strings.Fields(o) {
		switch {
		case g != nil && n == g.network:
			joined = true
		case lockUpHubNet.MatchString(n):
			_ = l.quiet("docker", "network", "disconnect", n, proxy)
		}
	}
	if g == nil || joined {
		return nil
	}
	if err := l.run("docker", "network", "connect", g.network, proxy); err != nil {
		return die("LEA_LOCKHTTP=1: could not join %s to the hub's network %s", proxy, g.network)
	}
	return nil
}

var lockUpHubNet = regexp.MustCompile(`^lockfamily-[0-9a-f]{8}_lockhub$`)
