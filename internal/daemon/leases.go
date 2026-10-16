package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/DavidGodefroid/lea/internal/store"
)

const (
	maxLeaseTTL = 3600
	maxLeases   = 16
	maxGlob     = 256
	// boardLeases keeps a board_list answer under the wire limit.
	boardLeases = 200
)

// checkGlob accepts a lease pattern: path.Match syntax, relative to the repository, without "..",
// one line of at most 256 bytes.
func checkGlob(g string) error {
	if g == "" || len(g) > maxGlob {
		return fmt.Errorf("glob must be 1 to %d bytes", maxGlob)
	}
	if err := checkLine("glob", g, maxGlob); err != nil {
		return err
	}
	if strings.HasPrefix(g, "/") {
		return errors.New("glob must be relative to the repository")
	}
	for _, seg := range strings.Split(g, "/") {
		if seg == ".." {
			return errors.New("glob must not contain ..")
		}
	}
	if strings.Contains(g, "\\") {
		return errors.New("glob must not contain a backslash")
	}
	if strings.Contains(g, "**") {
		return errors.New("glob must not contain **: * does not cross /")
	}
	n := 0
	for i := 0; i < len(g); i++ {
		if c := g[i]; c == '*' || c == '?' || c == '[' {
			n++
		}
	}
	if n > store.MaxGlobWild {
		return fmt.Errorf("glob has %d wildcards (at most %d)", n, store.MaxGlobWild)
	}
	if _, err := path.Match(g, ""); err != nil {
		return errors.New("glob is not a valid pattern")
	}
	return nil
}

// leaseAcquire claims files for the calling agent (spec §6.1): advisory, between the agents of
// one container.
func (d *Daemon) leaseAcquire(o Origin, me string, raw json.RawMessage) (any, error) {
	if _, err := d.spawnedCaller(o, me, "lease_acquire"); err != nil {
		return nil, err
	}
	var p struct {
		Glob string `json:"glob"`
		TTL  int64  `json:"ttl"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := checkGlob(p.Glob); err != nil {
		return nil, err
	}
	if p.TTL < 1 || p.TTL > maxLeaseTTL {
		return nil, fmt.Errorf("ttl must be 1 to %d seconds", maxLeaseTTL)
	}
	if err := d.rates.allow("lease_acquire", me, d.now()); err != nil {
		return nil, err
	}
	l, err := d.st.AcquireLease(o.Container, me, p.Glob, p.TTL, d.now(), maxLeases)
	if err != nil {
		return nil, err
	}
	d.changed()
	return map[string]any{"id": l.ID, "glob": l.Glob, "expires": l.Expires}, nil
}

// leaseRelease ends one of the calling agent's leases.
func (d *Daemon) leaseRelease(o Origin, me string, raw json.RawMessage) (any, error) {
	if _, err := d.spawnedCaller(o, me, "lease_release"); err != nil {
		return nil, err
	}
	var p struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := d.rates.allow("lease_release", me, d.now()); err != nil {
		return nil, err
	}
	if err := d.st.ReleaseLease(p.ID, me); errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no lease #%d of yours", p.ID)
	} else if err != nil {
		return nil, err
	}
	d.changed()
	return struct{}{}, nil
}
