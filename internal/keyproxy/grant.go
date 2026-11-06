package keyproxy

// Grant tokens and the tree file (#110): what lets the forge forwarder itself enforce the push
// and merge rules of a tower's tree, whatever the agent ran (a script, a Makefile, a raw curl to
// git-receive-pack) and whatever a hook saw of it.
//
//   - The daemon writes TreeFile in the forge key directory (on the host; the proxy mounts it
//     read-only, no agent container does): for each address of the proxy's on a lea container's
//     internal network, the container and whether a tower's tree lives in it.
//   - The daemon mints a grant token (GrantKeyFile, the HMAC key, in the same directory) for each
//     pinned push it builds (that ref of that repository, from that container) and each pinned
//     merge (that PR). The pinned git push sends it in GrantHeader, the pinned gh merge as its
//     GH_TOKEN. A token is single use and expires after GrantTTL.
//   - From a tree container, the forwarder refuses any git-receive-pack without a token naming
//     exactly the one ref it updates, and any update or delete of a production branch (the
//     default branch, main, master, a branch .lea/production names on the default branch or on
//     the pushed branch; read on GitHub, a missing file only on a 404, any other error refuses);
//     a merge (REST pulls/N/merge, GraphQL mergePullRequest) without a merge token for that PR;
//     an auto-merge, a merge-upstream, a contents write, a git refs write (but the delete of a
//     branch that is not a production one, which gh pr merge --delete-branch makes).

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// GrantKeyFile is the HMAC key of the grant tokens, in the forge key directory.
	GrantKeyFile = "grant.key"
	// TreeFile names the containers of the towers' trees, in the forge key directory.
	TreeFile = "tree.json"
	// TreeVersion is TreeFile's format.
	TreeVersion = 1
	// GrantHeader carries a pinned push's grant token; the forwarder never forwards it.
	GrantHeader = "X-Lea-Grant"
	// GrantTTL is how long a grant token lives: the pinned merge reads the PR, its CI and its
	// files first.
	GrantTTL = 10 * time.Minute
	// GrantPush and GrantMerge are the kinds of grant tokens.
	GrantPush  = "push"
	GrantMerge = "merge"

	grantPrefix = "lea-grant."
)

// Grant is what a grant token allows: one push of Ref (refs/heads/<branch>) or one merge of PR,
// in Repo (owner/name, lower-cased), from Container.
type Grant struct {
	Container string `json:"c"`
	Repo      string `json:"r"`
	Kind      string `json:"k"`
	Ref       string `json:"ref,omitempty"`
	PR        int64  `json:"pr,omitempty"`
	Nonce     string `json:"n"`
	Exp       int64  `json:"e"`
}

// MintGrant is a token for g, signed with key; g's nonce and expiry are set here (now + GrantTTL).
func MintGrant(key []byte, g Grant, now time.Time) (string, error) {
	if len(key) < 32 {
		return "", errors.New("no grant key")
	}
	var n [16]byte
	if _, err := rand.Read(n[:]); err != nil {
		return "", err
	}
	g.Repo = strings.ToLower(g.Repo)
	g.Nonce, g.Exp = hex.EncodeToString(n[:]), now.Add(GrantTTL).Unix()
	b, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	return grantPrefix + p + "." + base64.RawURLEncoding.EncodeToString(grantMAC(key, p)), nil
}

func grantMAC(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(grantPrefix + payload))
	return m.Sum(nil)
}

// VerifyGrant is parseGrant for lea's other parts (tests): what tok allows, when key signed it
// and it has not expired.
func VerifyGrant(key []byte, tok string, now time.Time) (Grant, error) { return parseGrant(key, tok, now) }

// parseGrant checks tok's signature and expiry; it does not spend it.
func parseGrant(key []byte, tok string, now time.Time) (Grant, error) {
	rest, ok := strings.CutPrefix(tok, grantPrefix)
	p, sig, ok2 := strings.Cut(rest, ".")
	if !ok || !ok2 || len(key) < 32 {
		return Grant{}, errors.New("not a grant token")
	}
	s, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(s, grantMAC(key, p)) {
		return Grant{}, errors.New("a grant token lea did not sign")
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return Grant{}, errors.New("not a grant token")
	}
	var g Grant
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&g); err != nil || g.Nonce == "" || g.Container == "" || g.Repo == "" {
		return Grant{}, errors.New("not a grant token")
	}
	if now.Unix() > g.Exp {
		return Grant{}, errors.New("an expired grant token")
	}
	return g, nil
}

// spent remembers the nonces of the grant tokens used, until they expire: a token is used once.
type spent struct {
	mu sync.Mutex
	m  map[string]int64
}

// spend marks g used; false when it already was.
func (s *spent) spend(g Grant, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]int64{}
	}
	for n, e := range s.m {
		if now.Unix() > e {
			delete(s.m, n)
		}
	}
	if _, ok := s.m[g.Nonce]; ok {
		return false
	}
	s.m[g.Nonce] = g.Exp
	return true
}

// LoadGrantKey is the grant key in dir (the forge key directory on the host), created (0600)
// when missing.
func LoadGrantKey(dir string) ([]byte, error) {
	p := filepath.Join(dir, GrantKeyFile)
	if b, err := os.ReadFile(p); err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) < 32 {
			return nil, errors.New("the grant key " + p + " is not 32 hex bytes")
		}
		return k, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadGrantKey(dir)
		}
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(hex.EncodeToString(k) + "\n"); err != nil {
		return nil, err
	}
	return k, nil
}

// readGrantKey is the grant key the forwarder reads at each check (nil when it cannot).
func readGrantKey(dir string) []byte {
	b, err := os.ReadFile(filepath.Join(dir, GrantKeyFile))
	if err != nil {
		return nil
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) < 32 {
		return nil
	}
	return k
}

// Tree is TreeFile: per address of the proxy's (one per lea container's internal network), the
// container on that network and whether a tower's tree lives in it.
type Tree struct {
	Version int                 `json:"version"`
	Addrs   map[string]TreeAddr `json:"addrs"`
}

// TreeAddr is a container as the forwarder sees it.
type TreeAddr struct {
	Container string `json:"container"`
	Tree      bool   `json:"tree"`
}

// WriteTree writes t to dir's TreeFile atomically (0600).
func WriteTree(dir string, t Tree) error {
	t.Version = TreeVersion
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, TreeFile)
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".tree-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// treeSource says, for a connection that arrived on the proxy's address local, the container
// it came from and whether the tree rules apply to it. No TreeFile: none apply (no daemon wrote
// one: no tower). An unreadable or malformed one, or an address it does not name (a loopback, a
// network the daemon has not seen yet) while some container holds a tree: they apply (fail
// closed), with no container (no grant token matches it).
func treeSource(dir string, local net.IP) (container string, tree bool) {
	b, err := os.ReadFile(filepath.Join(dir, TreeFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	var t Tree
	if err != nil || json.Unmarshal(b, &t) != nil || t.Version != TreeVersion {
		return "", true
	}
	if local != nil {
		if a, ok := t.Addrs[local.String()]; ok {
			return a.Container, a.Tree
		}
	}
	for _, a := range t.Addrs {
		if a.Tree {
			return "", true
		}
	}
	return "", false
}
