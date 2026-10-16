package launcher

import (
	"os"
	"strconv"
	"time"
)

// lock is lea_lock: mkdir host/locks/<name>, retried every second up to LEA_LOCK_TIMEOUT tries
// (default 120). A timeout that is not a number fails at the first contention. After Interrupt no
// lock is taken: a lock waited for ends the wait, and one won as the signal came is released at once.
func (l *Launcher) lock(name string) error {
	dir := l.Cfg.HostDir() + "/locks"
	if err := SafePath(l.Cfg.DataDir(), dir+"/"+name); err != nil {
		return die("%v", err)
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return die("%v", err)
	}
	timeout, terr := strconv.Atoi(nonEmpty(l.Cfg.Get("LEA_LOCK_TIMEOUT"), "120"))
	for i := 0; ; {
		if l.refused() {
			return errInterrupted
		}
		if os.Mkdir(dir+"/"+name, 0o777) == nil {
			break
		}
		i++
		if terr != nil || i >= timeout {
			return die("another lea holds the '%s' lock (remove %s/%s if it is stale)", name, dir, name)
		}
		l.Sleep(time.Second)
	}
	release := func() { os.Remove(dir + "/" + name) }
	r := l.root()
	if l.drainOf != nil {
		// An exit function's lock: Interrupt runs no pending function after this one, the exit
		// function releases its lock itself.
		r.mu.Lock()
		defer r.mu.Unlock()
		if !l.admittedLocked() {
			release()
			return errInterrupted
		}
		r.setLock(name, release)
		return nil
	}
	finish := l.atExit(func(*Launcher) { release() })
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.interrupted {
		// atExit already ran the release, or Interrupt is about to.
		return errInterrupted
	}
	r.setLock(name, finish)
	return nil
}

// setLock records a held lock's release; mu is held.
func (l *Launcher) setLock(name string, release func()) {
	if l.locks == nil {
		l.locks = map[string]func(){}
	}
	l.locks[name] = release
}

// unlock is lea_unlock: best effort.
func (l *Launcher) unlock(name string) {
	r := l.root()
	r.mu.Lock()
	finish := r.locks[name]
	delete(r.locks, name)
	r.mu.Unlock()
	if finish != nil {
		finish()
		return
	}
	os.Remove(l.Cfg.HostDir() + "/locks/" + name)
}
