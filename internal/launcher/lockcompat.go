package launcher

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/locks"
)

// Each Lock's client in lea's image comes from one set of releases, config/locks.lock (lea#134):
// lea doctor compares each lock lox runs for the projects directory with lea's client of its type
// (lockfamily.yaml's image, else the lock console's hello handshake) and says what to run when
// they differ: `lea update` for a lock newer than lea's client, `brew upgrade lox` and `lox add
// TYPE NAME --version` for an older one. lea's start says it when lockfamily.yaml pins a lock to
// an image that is not the set's. lea never installs, starts or reviews a Lock.

// lockSet is config/locks.lock of lea's root.
func (l *Launcher) lockSet() (*locks.Set, error) {
	return locks.LoadSet(l.Root + "/" + locks.ManifestFile)
}

// lockClient is lea's client of the Lock typ: the set's pin, at the version of a dev override
// LEA_<LOCK>_REF when one is set (a checkout: no version).
func (l *Launcher) lockClient(set *locks.Set, typ string) locks.Client {
	c := locks.Client{Pin: set.Pins[typ], Version: set.Pins[typ].Version}
	if ref := l.Cfg.Get(lockSwitch(typ) + "_REF"); ref != "" {
		c.Version = ""
		if locks.Version.MatchString(ref) {
			c.Version = ref
		}
	}
	return c
}

// declaredImages are the images lockfamily.yaml of the Lock Family's directory lf pins, by lock
// (locks.<name>.image, what lox runs), read as lea__declared_locks reads the file: nothing
// without it, or for a file lea cannot trust (lockUpUntrusted).
func declaredImages(lf string) map[string]string {
	p := lf + "/lockfamily.yaml"
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || lockUpUntrusted(lf, p, fi) != "" {
		return nil
	}
	return parseDeclaredImages(io.LimitReader(f, 1<<20))
}

var yamlKey = regexp.MustCompile(`^( *)([A-Za-z0-9_.-]+|"[^"]*"|'[^']*'):(.*)$`)

// parseDeclaredImages is the image of each lock of a lockfamily.yaml (its top-level locks:
// mapping, each lock's own image: key).
func parseDeclaredImages(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	inLocks, lockIndent, fieldIndent, name := false, -1, -1, ""
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		m := yamlKey.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ind, key := len(m[1]), strings.Trim(m[2], `"'`)
		val := strings.TrimSpace(m[3])
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		val = strings.Trim(val, `"'`)
		switch {
		case ind == 0:
			inLocks, lockIndent, fieldIndent, name = key == "locks" && val == "", -1, -1, ""
		case !inLocks:
		case lockIndent < 0 || ind <= lockIndent:
			lockIndent, fieldIndent, name = ind, -1, key
		case name != "" && (fieldIndent < 0 || ind == fieldIndent):
			fieldIndent = ind
			if key == "image" && val != "" {
				out[name] = val
			}
		}
	}
	return out
}

// lockCompatAtStart warns, at a container's start, about each lock whose socket the agents get
// (ups) that lockfamily.yaml pins to an image that is not the set's release of its type (lea
// doctor asks its console): never fatal, and silent when every lock runs the set's release.
func (l *Launcher) lockCompatAtStart(proj string, ups map[string][]lockUpSocket) {
	if len(ups) == 0 {
		return
	}
	set, err := l.lockSet()
	if err != nil {
		l.logf("warning: %v: lea cannot tell whether its Lock clients speak the locks' protocol", err)
		return
	}
	imgs := declaredImages(proj + "/.lockfamily")
	for _, typ := range locks.Names() {
		for _, s := range ups[typ] {
			img := imgs[s.Instance]
			c := l.lockClient(set, typ)
			if _, ok := c.Pin.ImageOf(img); img == "" || (ok && c.Version == c.Pin.Version) {
				continue
			}
			v := locks.Check(c, locks.Observed{Type: typ, Instance: s.Instance, Image: img})
			l.logf("warning: %s: %s (lea doctor asks its console)", v.Subject, v.Detail)
		}
	}
}

// doctorLocks is lea doctor's part on the Lock clients: the set lea's image has, the dev
// overrides of it, lox's release, and each lock lox runs for the projects directory proj against
// lea's client of its type. An incompatible pair (protocols that differ) fails.
func (l *Launcher) doctorLocks(proj string) error {
	ok := func(s string) { fmt.Fprintf(l.Stdout, "ok    %s\n", s) }
	warn := func(s, d string) { fmt.Fprintf(l.Stdout, "warn  %s: %s\n", s, d) }
	fail := func(s, d string) { fmt.Fprintf(l.Stdout, "FAIL  %s: %s\n", s, d) }
	set, err := l.lockSet()
	if err != nil {
		fail("Lock clients", fmt.Sprintf("%v: reinstall lea", err))
		return exitError{1}
	}
	ok("Lock clients: one set, " + set.Summary())
	for _, lk := range locks.Registry {
		key := lk.Switch + "_REF"
		if ref := l.Cfg.Get(key); ref != "" {
			warn(key, fmt.Sprintf("%s overrides the set's %s %s (a dev override, for this release only; lea's image has the set's client): remove it from %s",
				ref, lk.Name, set.Pins[lk.Name].Version, l.Cfg.UserConfigPath()))
		}
	}
	if v := l.Cfg.Get("LEA_LOCKSQL_IMAGE"); v != "" {
		warn("LEA_LOCKSQL_IMAGE", "obsolete (lea builds no locksql image: lox runs the lock's): remove it from "+l.Cfg.UserConfigPath())
	}
	lf := proj + "/.lockfamily"
	s, _ := readLockUpState(func(string, ...any) {}, proj, lf)
	imgs := declaredImages(lf)
	if s == nil && imgs == nil {
		return nil
	}
	l.doctorLox(set, ok, warn)
	if s == nil {
		warn("locks lox runs", "no lox up state for "+proj+" (.lockfamily/up.json): no lock to compare with lea's clients")
		return nil
	}
	on := l.Cfg.ToolsOn(proj, func(string, ...any) {})
	tools, _ := upTools(s, func(t string) bool { return on[t] })
	var rc error
	for _, typ := range locks.Names() {
		t := tools[typ]
		if t == nil {
			continue
		}
		for _, lk := range t.locks {
			c := l.lockClient(set, typ)
			o := locks.Observed{Type: typ, Instance: lk.Instance, Image: imgs[lk.Instance]}
			if _, match := c.Pin.ImageOf(o.Image); !match || c.Version != c.Pin.Version {
				o.Hello, o.HelloErr = l.lockHello(proj, s, lk)
			}
			v := locks.Check(c, o)
			switch v.Status {
			case locks.OK:
				ok(v.Subject + ": " + v.Detail)
			case locks.Warn:
				warn(v.Subject, v.Detail)
			default:
				fail(v.Subject, v.Detail)
				rc = exitError{1}
			}
		}
	}
	return rc
}

var loxVersionLine = regexp.MustCompile(`^lox v?([0-9]+\.[0-9]+\.[0-9]+)\s*$`)

// doctorLox is lea doctor's line on lox, which installs, starts and approves the Locks: on the
// PATH, and not older than the set's release.
func (l *Launcher) doctorLox(set *locks.Set, ok func(string), warn func(string, string)) {
	o, err := l.output(false, "lox", "version")
	if err != nil {
		warn("lox", "not found: lox runs the Locks (brew install davidgodefroid/tap/lox)")
		return
	}
	m := loxVersionLine.FindStringSubmatch(strings.TrimSpace(o))
	switch {
	case m == nil:
		warn("lox", fmt.Sprintf("its version is not a release (%q): lea's clients are tested with lox %s", clipText(strings.TrimSpace(o)), set.Lox))
	case locks.Older("v"+m[1], set.Lox):
		warn("lox", fmt.Sprintf("v%s is older than %s, the release lea's clients are tested with: brew upgrade lox", m[1], set.Lox))
	default:
		ok("lox v" + m[1] + " (lea's clients are tested with " + set.Lox + ")")
	}
}

// lockHello is the hello handshake of the console of the lock lk of up.json s: lea's core binary
// run in lea's image (lea lock-hello) with only the lock's socket volume mounted, once docker
// confirms it is lox up's (lockUpVolumeWrong), as the host user in the Lock's clients group, with
// no network and no capability. nil and why when lea cannot ask it.
func (l *Launcher) lockHello(proj string, s *lockUpState, lk UpLock) (*locks.Hello, string) {
	reg, _ := locks.Get(lk.Type)
	if why := l.lockUpVolumeWrong(lk.Volume, s.Project, proj, lk.Instance, lk.Type); why != "" {
		return nil, why
	}
	if err := noComma(l.Core); err != nil {
		return nil, err.Error()
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--group-add", reg.GID, "--label", "lea.prefix=" + l.prefix(), "--label", "lea.role=lock-hello",
		"--mount", lockUpMount(lockUpSocket{Volume: lk.Volume, Dst: reg.SocketDir()}),
		"--mount", "type=bind,src=" + l.Core + ",dst=/usr/local/bin/lea,readonly",
		"--entrypoint", "/usr/local/bin/lea", l.Cfg.Get("LEA_IMAGE"), "lock-hello", "--uid", reg.UID, reg.SocketDir()}
	// The console's socket, named as lockkit names it (the projects directory's hash and the
	// profile lox up says it serves): a 2710 socket directory cannot be listed.
	if lk.Profile != "" {
		args = append(args, locks.SocketName(reg.SocketDir(), proj, lk.Profile))
	}
	c := l.cmd("docker", args...)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if err := l.exec(c); err != nil {
		why := "lea lock-hello failed in " + l.Cfg.Get("LEA_IMAGE")
		if e := strings.TrimSpace(errb.String()); e != "" {
			why += ": " + clipText(strings.TrimPrefix(e, "lea lock-hello: "))
		}
		return nil, why
	}
	o := out.String()
	var rs []locks.HelloAt
	if json.Unmarshal([]byte(strings.TrimSpace(o)), &rs) != nil || len(rs) == 0 {
		return nil, "lea lock-hello gave no answer lea can read"
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Socket < rs[j].Socket })
	var errs []string
	for _, r := range rs {
		if r.Hello != nil {
			return r.Hello, ""
		}
		errs = append(errs, clipText(r.Err))
	}
	return nil, strings.Join(errs, "; ")
}

// clipText keeps text from a command lea prints short and printable.
func clipText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
