package daemon

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// A lock added while lea runs (E11): lox up rewrites <projects>/.lockfamily/up.json, the daemon
// sees it (RunLockWatch) and reads it again with the launcher's trust checks (LockWatch.Read).
// Docker cannot add a mount to a running container: an agent container started from now on gets
// the lock at its start (the launcher's runArgs), and each running container whose real mounts
// lack it is told to restart lea (lea stop, then lea), in the deck (Snapshot.LocksAdded) and in
// its agents' status lines (status_report's answer). It does not depend on the daemon's own
// restart (the providers stamp changes only when a lock turns a tool on): the hint compares
// up.json with each container's mounts, so a daemon that started after the lock was added gives
// it as well.

// upLockFile is lox up's state in the Lock Family's directory (internal/launcher's lockUpFile).
const upLockFile = "up.json"

// UpLock is a lock lox up runs that an agent container started now would mount: its instance and
// its socket volume (launcher.UpLock).
type UpLock struct{ Instance, Volume string }

// LockEvent is what the watch saw: Kind "lock-added", for the lock Name (an instance).
type LockEvent struct{ Kind, Name string }

// LockWatch is the up.json the daemon watches: Dir is the Lock Family's directory
// (<LEA_PROJECTS_DIR>/.lockfamily), Read reads its up.json with the launcher's trust checks
// (warnings through logf; nil when there is none it trusts) and Mounts lists the volumes a
// container mounts (docker inspect).
type LockWatch struct {
	Dir    string
	Read   func(logf func(string, ...any)) []UpLock
	Mounts func(ctx context.Context, container string) ([]string, error)
}

const (
	// defaultLockDebounce gathers the events of one rewrite (a new file renamed, its mode).
	defaultLockDebounce = 150 * time.Millisecond
	// mountsTTL is how long a container's mounts are kept: the deck and the status lines ask
	// often, and only a new container (lea stop, then lea) changes them.
	mountsTTL = 10 * time.Second
)

type mountsEntry struct {
	at   time.Time
	vols map[string]bool
}

// SetLockWatch sets the up.json the daemon watches (RunLockWatch).
func (d *Daemon) SetLockWatch(w LockWatch) {
	d.mu.Lock()
	d.lockWatch = w
	d.mu.Unlock()
}

// RunLockWatch watches up.json until ctx ends: read at the start, then again after each change.
func (d *Daemon) RunLockWatch(ctx context.Context) error { return d.runLockWatch(ctx, nil) }

// runLockWatch is RunLockWatch closing ready once it watches and read up.json (a test seam).
func (d *Daemon) runLockWatch(ctx context.Context, ready chan<- struct{}) error {
	d.mu.Lock()
	lw, debounce := d.lockWatch, d.lockDebounce
	d.mu.Unlock()
	if debounce == 0 {
		debounce = defaultLockDebounce
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	// The projects directory, for the Lock Family's directory made (lox init) or made again, and
	// the Lock Family's directory itself, where lox up renames its new up.json.
	parent := filepath.Dir(lw.Dir)
	if err := w.Add(parent); err != nil {
		return err
	}
	_ = w.Add(lw.Dir)
	d.readUpLocks(lw, true)
	if ready != nil {
		close(ready)
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			d.lockWarn("lea daemon: watching %s: %v", lw.Dir, err)
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			switch {
			case ev.Name == lw.Dir:
				if ev.Has(fsnotify.Create) {
					_ = w.Add(lw.Dir)
				}
				timer.Reset(debounce)
			case filepath.Dir(ev.Name) == lw.Dir && filepath.Base(ev.Name) == upLockFile:
				timer.Reset(debounce)
			}
		case <-timer.C:
			d.readUpLocks(lw, false)
		}
	}
}

// readUpLocks reads up.json again. A lock that was not in the last trusted state is an event
// (none at the start); an up.json lea cannot trust gives no event and no hint, and its warning
// once, until it changes. The decks are told.
func (d *Daemon) readUpLocks(lw LockWatch, first bool) {
	var warns []string
	locks := lw.Read(func(format string, a ...any) {
		warns = append(warns, strings.TrimSpace(fmt.Sprintf(format, a...)))
	})
	d.mu.Lock()
	warn := strings.Join(warns, "\n")
	say := warn != "" && warn != d.lockLastWarn
	d.lockLastWarn = warn
	var added []string
	if locks != nil || warn == "" {
		for _, l := range locks {
			if !first && !d.upKnown[l.Instance] {
				added = append(added, l.Instance)
			}
		}
		d.upKnown = map[string]bool{}
		for _, l := range locks {
			d.upKnown[l.Instance] = true
		}
	}
	changed := !slices.Equal(d.upLocks, locks)
	d.upLocks = locks
	after := d.afterLockEvent
	d.mu.Unlock()
	if say {
		for _, w := range warns {
			d.lockWarn("lea daemon: %s", w)
		}
	}
	for _, name := range added {
		if after != nil {
			after(LockEvent{Kind: "lock-added", Name: name})
		}
	}
	if changed {
		d.changed()
	}
}

func (d *Daemon) lockWarn(format string, a ...any) {
	d.mu.Lock()
	f := d.lockWarnf
	d.mu.Unlock()
	if f == nil {
		f = log.Printf
	}
	f(format, a...)
}

// upLockNames are the instances of the locks the last trusted up.json names.
func (d *Daemon) upLockNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, l := range d.upLocks {
		out = append(out, l.Instance)
	}
	return out
}

// locksAdded are the locks up.json names that the container does not mount: its real mounts
// (docker inspect), kept mountsTTL. None when they cannot be read.
func (d *Daemon) locksAdded(ctx context.Context, container string) []string {
	d.mu.Lock()
	locks, mounts := d.upLocks, d.lockWatch.Mounts
	e, ok := d.mountsCache[container]
	d.mu.Unlock()
	if len(locks) == 0 || mounts == nil || !containerName.MatchString(container) {
		return nil
	}
	if !ok || time.Since(e.at) > mountsTTL {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		vols, err := mounts(ctx, container)
		if err != nil {
			return nil
		}
		e = mountsEntry{at: time.Now(), vols: map[string]bool{}}
		for _, v := range vols {
			e.vols[v] = true
		}
		d.mu.Lock()
		if d.mountsCache == nil {
			d.mountsCache = map[string]mountsEntry{}
		}
		d.mountsCache[container] = e
		d.mu.Unlock()
	}
	var out []string
	for _, l := range locks {
		if !e.vols[l.Volume] {
			out = append(out, l.Instance)
		}
	}
	return out
}
