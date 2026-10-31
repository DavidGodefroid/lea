package launcher

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// lea config export|import: one JSON file that carries a team's configuration from one
// workstation to another. The file is data: it is never sourced, every key and file it may hold
// is allow-listed below and checked with the rules of LoadConfig, and the configuration it would
// give is loaded once more before anything is written. Paths, LEA_PROJECTS_DIR and secrets are
// never part of it.

// shareFormat is the version of the file this lea reads and writes.
const shareFormat = 1

// shareFile is the file's content.
type shareFile struct {
	Format  int               `json:"lea_config_format"`
	Version string            `json:"lea_version,omitempty"`
	Keys    map[string]string `json:"keys"`
	Files   map[string]string `json:"files,omitempty"`
}

// What a change needs on top of --yes: anything that widens what the agents reach, pins the SSH
// host keys they trust, or chooses the code lea runs.
const (
	classEgress   = "egress"
	classHostKeys = "host key"
	classCode     = "code"
	classLock     = "lock tool"
)

var classFlags = map[string]string{classEgress: "--allow-egress", classHostKeys: "--allow-host-keys", classCode: "--allow-code", classLock: "--allow-lock"}

// When a change applies.
const (
	noteStop  = "applies to agents started after lea stop --all"
	noteProxy = "applies once the proxy restarts (lea stop --all, then lea)"
	noteInit  = "applies after lea init"
)

type shareKey struct {
	class    string
	personal bool // exported with --include-personal only; kept by --replace
	note     string
	check    func(k, v string) error
}

// shareKeys are the keys a share file may hold. Every other key, LEA_PROJECTS_DIR and the paths
// included, stays on its workstation.
var shareKeys = map[string]shareKey{
	"LEA_GIT_SSH":          {classEgress, false, noteInit, func(_, v string) error { return CheckGitSSH(v) }},
	"LEA_EGRESS_ALLOW":     {classEgress, false, noteProxy, func(_, v string) error { _, err := egressAllow(v); return err }},
	"LEA_OTEL_UPSTREAM":    {classEgress, false, noteProxy, func(_, v string) error { return CheckOtelUpstream(v) }},
	"LEA_IMAGE":            {classCode, false, noteInit, checkImage},
	"LEA_PROXY_IMAGE":      {classCode, false, noteInit, checkImage},
	"LEA_LOCKSQL_IMAGE":    {classCode, false, noteInit, checkImage},
	"LEA_LOCKSQL_REF":      {classCode, false, noteInit, checkLocksqlRef},
	"LEA_RELEASES_URL":     {classCode, false, "applies at the next lea update", checkReleases},
	"LEA_LOCKSQL":          {"", false, noteInit, checkBit("LEA_LOCKSQL must be 1 (wire lea for a locksql console) or 0")},
	"LEA_NESTED_NETWORKS":  {"", false, noteStop, checkNetworks},
	"LEA_K8S_NAMESPACE":    {"", false, "applies at the next lea kube refresh", checkNamespace},
	"LEA_APPROVALS":        {"", false, noteStop, checkApprovals},
	"LEA_APPROVAL_TIMEOUT": {"", false, noteStop, checkTimeout},
	"LEA_SOUND":            {"", true, noteStop, checkBit("LEA_SOUND must be 1 or 0")},
}

// The files a share file may hold, by name, and the key that points at each one.
const (
	fileKnownHosts    = "git_known_hosts"
	fileInternalHosts = "internal-hosts"
)

// machineKeys are user configuration keys export skips without a warning: they belong to the
// workstation (the two files go by content).
var machineKeys = map[string]bool{"LEA_PROJECTS_DIR": true, "LEA_CONFIG": true, "LEA_PREFIX": true,
	"LEA_GIT_KNOWN_HOSTS": true, "LEA_GITLAB_KNOWN_HOSTS": true, "LEA_INTERNAL_HOSTS": true,
	// What agents may ask of this workstation's lock consoles is its user's choice alone.
	"LEA_LOCKSQL_AGENT_PROFILES": true}

const configUsage = `Usage:
  lea config export [--output FILE] [--include-personal]
  lea config import FILE|- [--replace] [--dry-run] [--yes]
                    [--allow-egress] [--allow-host-keys] [--allow-code] [--allow-lock]
`

// maxShareSize bounds a share file and each file it carries.
const maxShareSize = 1 << 20

func (l *Launcher) config(args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		_, err := io.WriteString(l.Stdout, configUsage)
		return err
	}
	usage := func() error { io.WriteString(l.Stderr, configUsage); return exitError{2} }
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "export":
		var out string
		personal := false
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--include-personal":
				personal = true
			case args[i] == "--output" && i+1 < len(args):
				i++
				out = args[i]
			default:
				return usage()
			}
		}
		return l.configExport(out, personal)
	case "import":
		var o importOpts
		for _, a := range args[1:] {
			switch a {
			case "--replace":
				o.replace = true
			case "--dry-run":
				o.dryRun = true
			case "--yes":
				o.yes = true
			case "--allow-egress":
				o.allow = append(o.allow, classEgress)
			case "--allow-host-keys":
				o.allow = append(o.allow, classHostKeys)
			case "--allow-code":
				o.allow = append(o.allow, classCode)
			case "--allow-lock":
				o.allow = append(o.allow, classLock)
			default:
				if o.file != "" || strings.HasPrefix(a, "-") && a != "-" {
					return usage()
				}
				o.file = a
			}
		}
		if o.file == "" {
			return usage()
		}
		return l.configImport(o)
	}
	return usage()
}

// userAssignments are the keys the user configuration itself assigns, read as data.
func (l *Launcher) userAssignments() map[string]string {
	user := l.Cfg.UserConfigFile(l.Cwd)
	m := map[string]string{}
	for k, v := range l.Cfg.file {
		if l.Cfg.from[k] == user {
			m[k] = v
		}
	}
	return m
}

func (l *Launcher) configExport(out string, personal bool) error {
	f := shareFile{Format: shareFormat, Version: l.Version, Keys: map[string]string{}, Files: map[string]string{}}
	user := l.userAssignments()
	if v, ok := user["LEA_GITLAB_SSH"]; ok {
		// The key of older lea: LEA_GIT_SSH when that one is unset.
		if _, set := user["LEA_GIT_SSH"]; !set {
			user["LEA_GIT_SSH"] = v
		}
		delete(user, "LEA_GITLAB_SSH")
	}
	keys := make([]string, 0, len(user))
	for k := range user {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := user[k]
		d, shared := shareKeys[k]
		switch {
		case machineKeys[k]:
		case !shared:
			l.logf("warning: %s is not a shared key: not exported", k)
		case d.personal && !personal:
		default:
			if err := checkShareValue(k, v); err != nil {
				l.logf("warning: %v: not exported", err)
				continue
			}
			f.Keys[k] = v
		}
	}
	for name, path := range map[string]string{fileKnownHosts: l.Cfg.Get("LEA_GIT_KNOWN_HOSTS"), fileInternalHosts: l.Cfg.Get("LEA_INTERNAL_HOSTS")} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s strings.Builder
		for i, line := range strings.Split(string(b), "\n") {
			e, ok, err := parseShareLine(name, line)
			if err != nil {
				l.logf("warning: %s line %d: %v: not exported", name, i+1, err)
			} else if ok {
				s.WriteString(e.line + "\n")
			}
		}
		if s.Len() > 0 {
			f.Files[name] = s.String()
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return err
	}
	if out == "" {
		_, err := l.Stdout.Write(buf.Bytes())
		return err
	}
	return ReplaceUserConfig(out, buf.Bytes())
}

type importOpts struct {
	file                 string
	replace, dryRun, yes bool
	allow                []string
}

// keyChange is one key of the user configuration the import sets (set) or removes.
type keyChange struct {
	key, old, new string
	had, set      bool
	class, note   string
}

// fileChange is one of the two files the import writes, at path.
type fileChange struct {
	name, path, content string
	added, removed      []string
	class, note         string
}

func (l *Launcher) configImport(o importOpts) error {
	f, err := l.readShare(o.file)
	if err != nil {
		return err
	}
	if err := checkShare(f); err != nil {
		return err
	}
	keys, files, err := l.importPlan(f, o.replace)
	if err != nil {
		return err
	}
	// Every file the import may write, changed or not: a symbolic link there is refused even when
	// this run has nothing to change, so that the next one does not find it.
	userPath := l.Cfg.UserConfigFile(l.Cwd)
	targets := []string{userPath}
	for name := range f.Files {
		targets = append(targets, l.shareDir()+"/"+name)
	}
	for _, p := range targets {
		for _, q := range []string{p, p + ".bak"} {
			if isSymlink(q) {
				return die("refusing to write %s: it is a symbolic link", q)
			}
		}
	}
	if len(keys) == 0 && len(files) == 0 {
		fmt.Fprintln(l.Stdout, "nothing to change")
		return nil
	}
	need := map[string]bool{}
	for _, c := range keys {
		fmt.Fprintf(l.Stdout, "%s: %s -> %s%s\n", c.key, shown(c.old, c.had), shown(c.new, c.set), tag(c.class))
		need[c.class] = true
	}
	for _, fc := range files {
		fmt.Fprintf(l.Stdout, "%s (%s):\n", fc.name, fc.path)
		for _, a := range fc.added {
			fmt.Fprintf(l.Stdout, "+ %s%s\n", a, tag(fc.class))
		}
		for _, r := range fc.removed {
			fmt.Fprintf(l.Stdout, "- %s\n", r)
		}
		if len(fc.added) > 0 {
			need[fc.class] = true
		}
	}
	var flags, missing []string
	for _, c := range []string{classEgress, classHostKeys, classCode, classLock} {
		if need[c] {
			flags = append(flags, classFlags[c])
			if !hasString(o.allow, c) {
				missing = append(missing, classFlags[c])
			}
		}
	}
	if o.dryRun {
		if len(flags) > 0 {
			fmt.Fprintf(l.Stdout, "applying this import needs --yes %s\n", strings.Join(flags, " "))
		}
		return nil
	}
	if len(missing) > 0 {
		return die("this import changes what the agents reach, the host keys they trust, the code lea runs or the lock tools it wires: "+
			"check the changes above, then rerun with %s", strings.Join(missing, " "))
	}
	if !o.yes {
		if o.file == "-" || !IsTerminal(l.Stdin) {
			return die("no terminal to confirm the import on: rerun with --yes once you have checked the changes (--dry-run shows them)")
		}
		fmt.Fprint(l.Stderr, "apply these changes? [y/N] ")
		a, _ := bufio.NewReader(l.Stdin).ReadString('\n')
		if a = strings.TrimSpace(a); a != "y" && a != "yes" {
			return die("import cancelled: nothing written")
		}
	}
	conf, err := l.newUserConfig(userPath, keys)
	if err != nil {
		return err
	}
	if err := l.checkLoads(userPath, conf); err != nil {
		return err
	}
	var writes []pendingWrite
	if len(keys) > 0 {
		writes = append(writes, pendingWrite{userPath, conf})
	}
	for _, fc := range files {
		writes = append(writes, pendingWrite{fc.path, []byte(fc.content)})
	}
	if err := writeAll(writes); err != nil {
		return err
	}
	notes := map[string]bool{}
	for _, c := range keys {
		if c.set && Protected(c.key) && l.Cfg.getenv(c.key) != "" {
			l.logf("warning: %s is set in the environment, which beats the configuration files: unset it there for the imported value to apply", c.key)
		}
		if c.note != "" {
			notes[c.note] = true
		}
	}
	for _, fc := range files {
		notes[fc.note] = true
	}
	var ns []string
	for n := range notes {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	for _, n := range ns {
		l.logf("note: some changes %s", n)
	}
	l.logf("imported %s; backups of the files it replaced are next to them (*.bak)", o.file)
	return nil
}

func shown(v string, set bool) string {
	if !set {
		return "(unset)"
	}
	if v == "" {
		return "''"
	}
	return v
}

func tag(class string) string {
	if class == "" {
		return ""
	}
	return "  [" + class + "]"
}

func hasString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// readShare reads and decodes the share file strictly: one JSON object, no unknown field.
func (l *Launcher) readShare(name string) (*shareFile, error) {
	var r io.Reader = l.Stdin
	if name != "-" {
		fh, err := os.Open(name)
		if err != nil {
			return nil, die("cannot read %s: %v", name, err)
		}
		defer fh.Close()
		r = fh
	}
	b, err := io.ReadAll(io.LimitReader(r, maxShareSize+1))
	if err != nil {
		return nil, die("cannot read %s: %v", name, err)
	}
	if len(b) > maxShareSize {
		return nil, die("%s is larger than %d bytes: not a lea configuration file", name, maxShareSize)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f shareFile
	if err := dec.Decode(&f); err != nil {
		return nil, die("%s is not a lea configuration file (JSON, from lea config export): %v", name, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, die("%s must hold one JSON object, from lea config export", name)
	}
	return &f, nil
}

// checkShare checks the whole file before anything is compared or written.
func checkShare(f *shareFile) error {
	switch {
	case f.Format > shareFormat:
		return die("this configuration file has format %d, newer than this lea reads (%d): upgrade lea", f.Format, shareFormat)
	case f.Format != shareFormat:
		return die("lea_config_format is missing or invalid: not a file from lea config export")
	}
	for k, v := range f.Keys {
		if _, ok := shareKeys[k]; !ok {
			return die("%s is not a shared key: lea config import only takes %s", printable(k), strings.Join(sharedKeyNames(), ", "))
		}
		if err := checkShareValue(k, v); err != nil {
			return die("%v", err)
		}
	}
	for name, content := range f.Files {
		if name != fileKnownHosts && name != fileInternalHosts {
			return die("%s is not a shared file: lea config import only takes %s and %s", printable(name), fileKnownHosts, fileInternalHosts)
		}
		if len(content) > maxShareSize {
			return die("%s is larger than %d bytes", name, maxShareSize)
		}
		for i, line := range strings.Split(content, "\n") {
			if _, _, err := parseShareLine(name, line); err != nil {
				return die("%s line %d: %v", name, i+1, err)
			}
		}
	}
	return nil
}

func sharedKeyNames() []string {
	var ks []string
	for k := range shareKeys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// printable is s when it holds printable ASCII only, else s quoted: nothing a file holds reaches
// the terminal as a control sequence.
func printable(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return strconv.Quote(s)
		}
	}
	return s
}

// checkShareValue checks one value of a shared key: one line of printable ASCII, then the key's
// own rule.
func checkShareValue(k, v string) error {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return fmt.Errorf("%s must be one line of printable ASCII characters", k)
		}
	}
	return shareKeys[k].check(k, v)
}

var (
	imageRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$`)
	httpsRe     = regexp.MustCompile(`^https://[A-Za-z0-9][A-Za-z0-9.-]*(:[1-9][0-9]{0,4})?(/[A-Za-z0-9._~/%+-]*)?$`)
	refRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	networkRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	namespaceRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

func checkImage(k, v string) error {
	if !imageRe.MatchString(v) {
		return fmt.Errorf("%s must be an image reference (name[:tag][@digest]), not '%s'", k, v)
	}
	return nil
}

func checkHTTPS(k, v string) error {
	if v != "" && !httpsRe.MatchString(v) {
		return fmt.Errorf("%s must be an https:// URL, not '%s'", k, v)
	}
	return nil
}

// checkReleases: lea update needs a releases URL, so it is never empty.
func checkReleases(k, v string) error {
	if v == "" {
		return fmt.Errorf("%s must be an https:// URL, not empty", k)
	}
	return checkHTTPS(k, v)
}

func checkLocksqlRef(k, v string) error {
	if !refRe.MatchString(v) || strings.Contains(v, "..") {
		return fmt.Errorf("%s must be a tag of locksql's repository (a checkout's path is not shared), not '%s'", k, v)
	}
	return nil
}

func checkNetworks(k, v string) error {
	for _, n := range strings.Split(v, " ") {
		if n != "" && !networkRe.MatchString(n) {
			return fmt.Errorf("%s must list network names separated by spaces, got '%s'", k, n)
		}
	}
	return nil
}

func checkNamespace(k, v string) error {
	if !namespaceRe.MatchString(v) {
		return fmt.Errorf("%s must be a Kubernetes namespace name, not '%s'", k, v)
	}
	return nil
}

func checkBit(msg string) func(k, v string) error {
	return func(_, v string) error {
		if v != "0" && v != "1" {
			return fmt.Errorf("%s, not '%s'", msg, v)
		}
		return nil
	}
}

func checkApprovals(_, v string) error {
	if v != "deck" && v != "pane" {
		return fmt.Errorf("LEA_APPROVALS must be deck or pane, not '%s'", v)
	}
	return nil
}

func checkTimeout(_, v string) error {
	if n, ok := plainNumber(v, 3); !ok || n < 1 || n > 600 {
		return fmt.Errorf("LEA_APPROVAL_TIMEOUT must be a number of seconds from 1 to 600 without leading zeros, not '%s'", v)
	}
	return nil
}

// shareEntry is one line of git_known_hosts (hosts, typ, key) or internal-hosts (hosts[0], ports),
// line its normalized form.
type shareEntry struct {
	hosts    []string
	hashed   []string // the hashed names of a local git_known_hosts line
	typ, key string
	line     string
}

// parseShareLine reads one line of a shared file: ok is false for a blank line or a comment.
func parseShareLine(name, line string) (shareEntry, bool, error) {
	if strings.HasSuffix(line, "\r") {
		line = line[:len(line)-1]
	}
	for i := 0; i < len(line); i++ {
		if (line[i] < 0x20 && line[i] != '\t') || line[i] > 0x7e {
			return shareEntry{}, false, errors.New("only printable ASCII characters are read")
		}
	}
	if name == fileInternalHosts {
		return parseInternalHost(line)
	}
	return parseKnownHost(line)
}

// keyTypes are the host key types git_known_hosts may pin.
var keyTypes = map[string]bool{"ssh-ed25519": true, "ssh-rsa": true, "ecdsa-sha2-nistp256": true, "ecdsa-sha2-nistp384": true,
	"ecdsa-sha2-nistp521": true, "sk-ssh-ed25519@openssh.com": true, "sk-ecdsa-sha2-nistp256@openssh.com": true}

var knownHostRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9.-]*|\[[A-Za-z0-9][A-Za-z0-9.:-]*\]:[1-9][0-9]{0,4})$`)

// parseKnownHost reads a known_hosts line: plain host names (or [host]:port) and one key. Markers
// (@cert-authority, @revoked), wildcards and negations would trust far more than one host: they
// are refused; a trailing comment is dropped.
func parseKnownHost(line string) (shareEntry, bool, error) {
	fs := strings.Fields(line)
	if len(fs) == 0 || strings.HasPrefix(fs[0], "#") {
		return shareEntry{}, false, nil
	}
	if strings.HasPrefix(fs[0], "@") {
		return shareEntry{}, false, fmt.Errorf("markers (%s) are not shared: pin each host's own key", fs[0])
	}
	if len(fs) < 3 {
		return shareEntry{}, false, errors.New("expected '<hosts> <key type> <key>'")
	}
	if strings.HasPrefix(fs[0], "|") {
		// A hashed name cannot be compared with the pins here: a key for a host already pinned
		// would pass as a new host.
		return shareEntry{}, false, errors.New("hashed host names are not shared: write the host's name")
	}
	var hosts []string
	for _, h := range strings.Split(fs[0], ",") {
		if !knownHostRe.MatchString(h) {
			return shareEntry{}, false, fmt.Errorf("'%s' is not a plain host name (wildcards and negations are not shared)", printable(h))
		}
		hosts = append(hosts, strings.ToLower(h))
	}
	if !keyTypes[fs[1]] {
		return shareEntry{}, false, fmt.Errorf("'%s' is not an SSH host key type", printable(fs[1]))
	}
	b, err := base64.StdEncoding.DecodeString(fs[2])
	if err != nil || len(b) < 4 || int(binary.BigEndian.Uint32(b)) > len(b)-4 || string(b[4:4+binary.BigEndian.Uint32(b)]) != fs[1] {
		return shareEntry{}, false, fmt.Errorf("the key is not a %s key", fs[1])
	}
	return shareEntry{hosts: hosts, typ: fs[1], key: fs[2], line: fs[0] + " " + fs[1] + " " + fs[2]}, true, nil
}

// pinsHost reports whether a local line pins host h: one of its names, or a hashed name
// (|1|salt|hash, HMAC-SHA1 of the name) that h hashes to.
func pinsHost(x shareEntry, h string) bool {
	if hasString(x.hosts, h) {
		return true
	}
	for _, hh := range x.hashed {
		p := strings.Split(hh, "|")
		if len(p) != 4 || p[1] != "1" {
			continue
		}
		salt, err1 := base64.StdEncoding.DecodeString(p[2])
		sum, err2 := base64.StdEncoding.DecodeString(p[3])
		if err1 != nil || err2 != nil {
			continue
		}
		m := hmac.New(sha1.New, salt)
		m.Write([]byte(h))
		if hmac.Equal(m.Sum(nil), sum) {
			return true
		}
	}
	return false
}

var hostNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// parseInternalHost reads an internal-hosts line as proxy/render-squid-conf.sh does: '<host>
// <ports>' and an optional comment, the host a name or an IPv4 address, the ports from 1 to 65535.
func parseInternalHost(line string) (shareEntry, bool, error) {
	line, _, _ = strings.Cut(line, "#")
	fs := strings.Fields(line)
	if len(fs) == 0 {
		return shareEntry{}, false, nil
	}
	if len(fs) != 2 {
		return shareEntry{}, false, errors.New("expected '<host> <ports>'")
	}
	h := fs[0]
	if strings.Trim(h, "0123456789.") == "" {
		p := strings.Split(h, ".")
		ok := len(p) == 4
		for _, o := range p {
			if n, err := strconv.Atoi(o); err != nil || n > 255 || len(o) > 1 && o[0] == '0' {
				ok = false
			}
		}
		if !ok {
			return shareEntry{}, false, fmt.Errorf("'%s' is not an IPv4 address", h)
		}
	} else if !hostNameRe.MatchString(h) || len(h) > 253 {
		return shareEntry{}, false, fmt.Errorf("'%s' is not a host name", printable(h))
	}
	for _, p := range strings.Split(fs[1], ",") {
		if n, ok := plainNumber(p, 5); !ok || n > 65535 {
			return shareEntry{}, false, fmt.Errorf("bad port list '%s'", printable(fs[1]))
		}
	}
	return shareEntry{hosts: []string{strings.ToLower(h)}, line: h + " " + fs[1]}, true, nil
}

// shareDir is where the import writes the shared files: lea's directory of the user configuration
// (~/.config/lea), where LoadConfig looks for git_known_hosts by default.
func (l *Launcher) shareDir() string {
	return nonEmpty(l.Cfg.Get("XDG_CONFIG_HOME"), l.Cfg.Get("HOME")+"/.config") + "/lea"
}

// importPlan is what the import changes: the keys of the user configuration, and the two files.
func (l *Launcher) importPlan(f *shareFile, replace bool) ([]keyChange, []fileChange, error) {
	c := l.Cfg
	user := l.userAssignments()
	var keys []keyChange
	add := func(k, v string, set bool) {
		old, had := user[k]
		if had == set && old == v {
			return
		}
		d := shareKeys[k]
		ch := keyChange{key: k, old: old, had: had, new: v, set: set, class: d.class, note: d.note}
		if k == "LEA_LOCKSQL" && nonEmpty(v, "0") == "1" && c.Get(k) != "1" {
			// Wiring a locksql console opens a lock tool's socket to the agents.
			ch.class = classLock
		}
		keys = append(keys, ch)
	}
	for _, k := range sharedKeyNames() {
		if v, ok := f.Keys[k]; ok {
			add(k, v, true)
		} else if _, had := user[k]; had && replace && !shareKeys[k].personal {
			add(k, "", false)
		}
	}
	if _, legacy := user["LEA_GITLAB_SSH"]; legacy && (hasKey(f.Keys, "LEA_GIT_SSH") || replace) {
		// The key of older lea sets the endpoints when LEA_GIT_SSH is empty: it goes with it.
		keys = append(keys, keyChange{key: "LEA_GITLAB_SSH", old: user["LEA_GITLAB_SSH"], had: true, class: classEgress, note: noteInit})
	}
	conf := l.shareDir()
	var files []fileChange
	for _, x := range []struct{ name, key, class, note string }{
		{fileKnownHosts, "LEA_GIT_KNOWN_HOSTS", classHostKeys, noteInit},
		{fileInternalHosts, "LEA_INTERNAL_HOSTS", classEgress, noteProxy},
	} {
		in, ok := f.Files[x.name]
		if !ok {
			continue
		}
		target, current := conf+"/"+x.name, c.Get(x.key)
		base := ""
		if current != filepath.Join(l.Root, "config", "internal-hosts") {
			// The shipped internal-hosts holds comments only: it is no base.
			b, err := os.ReadFile(current)
			if err != nil && !os.IsNotExist(err) {
				return nil, nil, die("cannot read %s: %v", current, err)
			}
			base = string(b)
		}
		fc, err := mergeShared(x.name, base, in, replace)
		if err != nil {
			return nil, nil, err
		}
		old, err := os.ReadFile(target)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, die("cannot read %s: %v", target, err)
		}
		if len(fc.added) == 0 && len(fc.removed) == 0 {
			// Nothing to add: the file in use stays, at its own path (an organisation's shared
			// file is not copied).
			continue
		}
		if fc.content != string(old) || err != nil {
			fc.name, fc.path, fc.class, fc.note = x.name, target, x.class, x.note
			files = append(files, fc)
		}
		if current != target {
			add2 := keyChange{key: x.key, old: user[x.key], had: hasKey(user, x.key), new: target, set: true, note: x.note}
			if !add2.had || add2.old != target {
				keys = append(keys, add2)
			}
		}
	}
	return keys, files, nil
}

// mergeShared is the content of a shared file after the import: in merge mode, base with the
// imported lines it lacks appended (a host pinned here to another key is a conflict); with
// replace, the imported lines.
func mergeShared(name, base, in string, replace bool) (fileChange, error) {
	var local, imported []shareEntry
	for _, line := range strings.Split(base, "\n") {
		if e, ok, err := parseShareLine(name, line); err == nil && ok {
			local = append(local, e)
		} else if fs := strings.Fields(line); name == fileKnownHosts && len(fs) >= 3 && strings.HasPrefix(fs[0], "|1|") {
			local = append(local, shareEntry{hashed: strings.Split(fs[0], ","), typ: fs[1], key: fs[2], line: line})
		}
	}
	for _, line := range strings.Split(in, "\n") {
		if e, ok, _ := parseShareLine(name, line); ok {
			imported = append(imported, e)
		}
	}
	var fc fileChange
	if replace {
		var b strings.Builder
		seen := map[string]bool{}
		for _, e := range imported {
			if !seen[e.line] {
				seen[e.line] = true
				b.WriteString(e.line + "\n")
				if !hasEntry(local, e) {
					fc.added = append(fc.added, e.line)
				}
			}
		}
		for _, e := range local {
			if !seen[e.line] && !hasEntry(imported, e) {
				fc.removed = append(fc.removed, e.line)
				seen[e.line] = true
			}
		}
		fc.content = b.String()
		return fc, nil
	}
	b := strings.Builder{}
	b.WriteString(base)
	if base != "" && !strings.HasSuffix(base, "\n") {
		b.WriteString("\n")
	}
	var conflicts []string
	pins := local
	for _, e := range imported {
		if hasEntry(local, e) {
			continue
		}
		if name == fileKnownHosts {
			for _, h := range e.hosts {
				if pinnedElsewhere(pins, h, e) && !hasString(conflicts, h) {
					conflicts = append(conflicts, h)
				}
			}
		}
		local = append(local[:len(local):len(local)], e)
		fc.added = append(fc.added, e.line)
		b.WriteString(e.line + "\n")
	}
	if len(conflicts) > 0 {
		return fc, die("git_known_hosts: %s already pinned here to another key: refused in a merge "+
			"(check the key with the host's administrator; --replace replaces the local pins)", strings.Join(conflicts, ", "))
	}
	fc.content = b.String()
	return fc, nil
}

// hasEntry reports whether es already holds e: the same line, or the same key for every host of e.
func hasEntry(es []shareEntry, e shareEntry) bool {
	for _, x := range es {
		if x.line == e.line {
			return true
		}
		if e.key != "" && x.typ == e.typ && x.key == e.key {
			all := true
			for _, h := range e.hosts {
				all = all && hasString(x.hosts, h)
			}
			if all {
				return true
			}
		}
	}
	return false
}

// pinnedElsewhere reports whether host h has keys in es and none of them is e's.
func pinnedElsewhere(es []shareEntry, h string, e shareEntry) bool {
	pinned := false
	for _, x := range es {
		if pinsHost(x, h) {
			if x.typ == e.typ && x.key == e.key {
				return false
			}
			pinned = true
		}
	}
	return pinned
}

// newUserConfig is the user configuration at path with keys applied, every other line kept.
func (l *Launcher) newUserConfig(path string, keys []keyChange) ([]byte, error) {
	old, err := readUserConfig(path)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(old, "\n") {
		drop := line == ""
		for _, c := range keys {
			drop = drop || SetsKey(line, c.key)
		}
		if !drop {
			b.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				b.WriteString("\n")
			}
		}
	}
	for _, c := range keys {
		if c.set {
			b.WriteString(c.key + "='" + strings.ReplaceAll(c.new, "'", `'\''`) + "'\n")
		}
	}
	return []byte(b.String()), nil
}

// checkLoads loads the configuration the import would give, from a private temporary copy of the
// new user configuration: a file LoadConfig would refuse is never written.
func (l *Launcher) checkLoads(path string, conf []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	t, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".check.")
	if err != nil {
		return err
	}
	defer os.Remove(t.Name())
	_, err = t.Write(conf)
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	getenv := func(k string) string {
		if k == "LEA_CONFIG" {
			return t.Name()
		}
		return l.Cfg.getenv(k)
	}
	if _, err := LoadConfig(getenv, l.Root, l.Cwd); err != nil {
		return die("the imported configuration would not load (nothing written): %v", err)
	}
	return nil
}

type pendingWrite struct {
	path string
	data []byte
}

// writeAll backs every file up (path.bak) and replaces it, each at once and private. When one
// write fails, the files already replaced are put back.
func writeAll(ws []pendingWrite) error {
	type undo struct {
		path string
		old  []byte
		had  bool
	}
	var done []undo
	rollback := func(err error) error {
		for i := len(done) - 1; i >= 0; i-- {
			u := done[i]
			if u.had {
				err = errors.Join(err, ReplaceUserConfig(u.path, u.old))
			} else {
				err = errors.Join(err, os.Remove(u.path))
			}
		}
		return err
	}
	for _, w := range ws {
		old, err := os.ReadFile(w.path)
		had := err == nil
		if err != nil && !os.IsNotExist(err) {
			return rollback(err)
		}
		if had {
			if err := ReplaceUserConfig(w.path+".bak", old); err != nil {
				return rollback(err)
			}
		}
		if err := ReplaceUserConfig(w.path, w.data); err != nil {
			return rollback(err)
		}
		done = append(done, undo{w.path, old, had})
	}
	return nil
}
