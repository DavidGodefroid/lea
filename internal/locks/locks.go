// Package locks is what lea knows of the Locks whose client its image ships (lea#134): one
// registry of their wiring (Registry: switch, socket directory, clients group, client binary, MCP
// server), and the set of releases lea pins them to (config/locks.lock, Set), which lea doctor
// compares with the locks lox runs (Check). lea never installs, starts or reviews a Lock: lox
// does.
package locks

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Lock is a Lock whose client is in lea's image: how lea wires its agents to the lock lox runs.
type Lock struct {
	// Name is the Lock's type (locksql, lockmq, lockfile), the client's binary in lea's image
	// (/usr/local/bin/<Name>) and its socket directory there (/run/<Name>).
	Name string
	// Switch is the configuration key that turns lea's wiring of it on or off (LEA_LOCKSQL ...).
	Switch string
	// GID is its clients group in lea's image and in the lock's own: the agents get it with
	// --group-add, and lox run writes it in up.json (client_gid).
	GID string
	// UID is its console account in both images: the socket must be served by it.
	UID string
	// What is what the Lock guards, for lea's messages.
	What string
	// ProfileDir is the projects directory's profile directory lea mounts read-only beside the
	// socket ("" for none): the client reads its profiles there.
	ProfileDir string
	// Hub reports that the lock runs hub-attached (pending results through the hub, lockkit
	// v0.9.0): lea's suspend hook waits on its tickets.
	Hub bool
}

// Client is the client's path in lea's image.
func (l Lock) Client() string { return "/usr/local/bin/" + l.Name }

// SocketDir is where the agents find the lock's socket: /run/<name> for one lock of the type,
// /run/<name>/<instance> for each of several.
func (l Lock) SocketDir() string { return "/run/" + l.Name }

// MCPServer is the name of the lock's MCP server for an agent: <name>, or <name>-<instance>
// when lox runs several locks of the type.
func (l Lock) MCPServer(instance string) string {
	if instance == "" {
		return l.Name
	}
	return l.Name + "-" + instance
}

// Registry are the Locks lea wires, in the order lea lists them everywhere.
var Registry = []Lock{
	{Name: "locksql", Switch: "LEA_LOCKSQL", GID: "10000", UID: "10001", What: "SQL databases", ProfileDir: ".locksql", Hub: true},
	{Name: "lockmq", Switch: "LEA_LOCKMQ", GID: "10010", UID: "10011", What: "RabbitMQ brokers", Hub: true},
	{Name: "lockfile", Switch: "LEA_LOCKFILE", GID: "10030", UID: "10031", What: "files on S3, SFTP or a local directory", Hub: true},
}

// Get is the Lock of type name.
func Get(name string) (Lock, bool) {
	for _, l := range Registry {
		if l.Name == name {
			return l, true
		}
	}
	return Lock{}, false
}

// Names are the Registry's types, in its order.
func Names() []string {
	out := make([]string, 0, len(Registry))
	for _, l := range Registry {
		out = append(out, l.Name)
	}
	return out
}

// ManifestFile is the set's file, relative to lea's root.
const ManifestFile = "config/locks.lock"

// Pin is one Lock's release in the set.
type Pin struct {
	Name     string
	Version  string // vX.Y.Z
	Protocol int    // the protocol major of its hello handshake
	// Image is the release's image by digest, Variants its variant images (vault ...) by name.
	Image    string
	Variants map[string]string
	Identity string            // the cosign certificate identity that signed the release
	SHA256   map[string]string // the client archive's sha256, by architecture (amd64, arm64)
}

// Digest is the sha256:… part of an image reference by digest, "" without one.
func Digest(ref string) string {
	_, d, ok := strings.Cut(ref, "@")
	if !ok || !digestRe.MatchString(d) {
		return ""
	}
	return d
}

// ImageOf reports whether the image reference ref is the release's image or one of its variants,
// compared by digest, and which ("" for the plain image).
func (p Pin) ImageOf(ref string) (variant string, ok bool) {
	d := Digest(ref)
	if d == "" {
		return "", false
	}
	if repo, _, _ := strings.Cut(ref, "@"); repo != imageRepo(p.Name) {
		return "", false
	}
	if d == Digest(p.Image) {
		return "", true
	}
	for name, img := range p.Variants {
		if d == Digest(img) {
			return name, true
		}
	}
	return "", false
}

func imageRepo(name string) string { return "ghcr.io/davidgodefroid/" + name }

// Set is config/locks.lock: the releases lea's clients come from, as one set.
type Set struct {
	Name        string // the lockkit release the set is built on (lockkit-vX.Y.Z)
	Lox         string // the lox release (lockhub) the set is tested with
	LoxIdentity string
	Pins        map[string]Pin // by Lock type: the Registry's and lockhttp (the gate's image)
}

var (
	// Version is a release: vX.Y.Z.
	Version    = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)
	digestRe   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sumRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	keyRe      = regexp.MustCompile(`^LOCKS_[A-Z0-9_]+$`)
	pinKeyRe   = regexp.MustCompile(`^LOCKS_(LOCKSQL|LOCKMQ|LOCKFILE|LOCKHTTP)_(VERSION|PROTOCOL|IMAGE|IMAGE_[A-Z0-9]+|IDENTITY|SHA256_AMD64|SHA256_ARM64)$`)
	setNameRe  = regexp.MustCompile(`^lockkit-v[0-9]+\.[0-9]+\.[0-9]+$`)
	pinnedLock = []string{"locksql", "lockmq", "lockfile", "lockhttp"}
)

// LoadSet reads the set from the file at path.
func LoadSet(path string) (*Set, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := ParseSet(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return s, nil
}

// Identity is the cosign identity of repo's release workflow at tag.
func Identity(repo, tag string) string {
	return "https://github.com/DavidGodefroid/" + repo + "/.github/workflows/release.yml@refs/tags/" + tag
}

// ParseSet reads a set: KEY=VALUE lines (comments and blank lines aside), each key once, every
// Lock complete and consistent (its identity names its version, its images are its own by
// digest, its clients' archives a sha256 for each architecture lea's image is built for).
func ParseSet(r io.Reader) (*Set, error) {
	kv := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(r, 1<<16))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !keyRe.MatchString(k) || strings.ContainsAny(v, " \t'\"$`\\") {
			return nil, fmt.Errorf("line %d: not KEY=VALUE", n)
		}
		if _, dup := kv[k]; dup {
			return nil, fmt.Errorf("line %d: %s set twice", n, k)
		}
		kv[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	s := &Set{Name: kv["LOCKS_SET"], Lox: kv["LOCKS_LOX_VERSION"], LoxIdentity: kv["LOCKS_LOX_IDENTITY"], Pins: map[string]Pin{}}
	if !setNameRe.MatchString(s.Name) {
		return nil, fmt.Errorf("LOCKS_SET %q: want lockkit-vX.Y.Z", s.Name)
	}
	if !Version.MatchString(s.Lox) || s.LoxIdentity != Identity("lockhub", s.Lox) {
		return nil, fmt.Errorf("LOCKS_LOX_VERSION %q and LOCKS_LOX_IDENTITY: want a lockhub release and its release workflow at that tag", s.Lox)
	}
	for k := range kv {
		switch k {
		case "LOCKS_SET", "LOCKS_LOX_VERSION", "LOCKS_LOX_IDENTITY":
			continue
		}
		if !pinKeyRe.MatchString(k) {
			return nil, fmt.Errorf("%s: unknown key", k)
		}
	}
	for _, name := range pinnedLock {
		up := "LOCKS_" + strings.ToUpper(name) + "_"
		p := Pin{Name: name, Version: kv[up+"VERSION"], Image: kv[up+"IMAGE"], Identity: kv[up+"IDENTITY"],
			Variants: map[string]string{}, SHA256: map[string]string{}}
		if !Version.MatchString(p.Version) {
			return nil, fmt.Errorf("%sVERSION %q: want vX.Y.Z", up, p.Version)
		}
		pr, err := strconv.Atoi(kv[up+"PROTOCOL"])
		if err != nil || pr < 1 {
			return nil, fmt.Errorf("%sPROTOCOL %q: want a protocol major", up, kv[up+"PROTOCOL"])
		}
		p.Protocol = pr
		if p.Identity != Identity(name, p.Version) {
			return nil, fmt.Errorf("%sIDENTITY: want %s's release workflow at %s", up, name, p.Version)
		}
		for k, v := range kv {
			if !strings.HasPrefix(k, up+"IMAGE") {
				continue
			}
			repo, _, _ := strings.Cut(v, "@")
			if repo != imageRepo(name) || Digest(v) == "" {
				return nil, fmt.Errorf("%s %q: want %s@sha256:<digest>", k, v, imageRepo(name))
			}
			if variant, ok := strings.CutPrefix(k, up+"IMAGE_"); ok {
				p.Variants[strings.ToLower(variant)] = v
			}
		}
		if p.Image == "" {
			return nil, fmt.Errorf("%sIMAGE: missing", up)
		}
		if _, ok := Get(name); ok {
			// Its client is in lea's image: an archive for each architecture.
			for _, arch := range []string{"amd64", "arm64"} {
				sum := kv[up+"SHA256_"+strings.ToUpper(arch)]
				if !sumRe.MatchString(sum) {
					return nil, fmt.Errorf("%sSHA256_%s %q: want the archive's sha256", up, strings.ToUpper(arch), sum)
				}
				p.SHA256[arch] = sum
			}
		} else if kv[up+"SHA256_AMD64"] != "" || kv[up+"SHA256_ARM64"] != "" {
			return nil, fmt.Errorf("%sSHA256_*: lea's image ships no %s client", up, name)
		}
		s.Pins[name] = p
	}
	return s, nil
}

// Summary is the set in one line: "lockkit-v0.13.0: locksql v0.17.0, lockmq v0.8.0, …".
func (s *Set) Summary() string {
	var b []string
	for _, name := range pinnedLock {
		b = append(b, name+" "+s.Pins[name].Version)
	}
	return s.Name + ": " + strings.Join(b, ", ") + ", lox " + s.Lox
}

// Older reports a < b for two releases vX.Y.Z (false when either is not one).
func Older(a, b string) bool {
	ma, mb := Version.FindStringSubmatch(a), Version.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x < y
		}
	}
	return false
}
