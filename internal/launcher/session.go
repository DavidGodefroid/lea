package launcher

import (
	"os"
	"strconv"
	"syscall"
)

// Host-side session bookkeeping (ported from lib/sessions.sh): one empty file per launcher pid in
// host/sessions/<id>. The launcher's pid is the same whether the bin/lea shim exec'd it or not.

func (l *Launcher) sessionDir(id string) string { return l.Cfg.HostDir() + "/sessions/" + id }

func (l *Launcher) sessionBegin(id string, pid int) error {
	d := l.sessionDir(id)
	if err := Mkdirs(l.Cfg.DataDir(), d); err != nil {
		return die("%v", err)
	}
	if err := WriteFile(l.Cfg.DataDir(), d+"/"+strconv.Itoa(pid), nil); err != nil {
		return die("%v", err)
	}
	return nil
}

func (l *Launcher) sessionEnd(id string, pid int) {
	p := l.sessionDir(id) + "/" + strconv.Itoa(pid)
	if SafePath(l.Cfg.DataDir(), p) == nil {
		os.Remove(p)
	}
}

// sessionCount counts live sessions and removes the rest: a symlink, a name that is not a pid,
// or a pid that kill -0 cannot reach.
func (l *Launcher) sessionCount(id string) (int, error) {
	d := l.sessionDir(id)
	if err := SafePath(l.Cfg.DataDir(), d); err != nil {
		return 0, die("%v", err)
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		return 0, nil
	}
	n := 0
	for _, e := range entries {
		p := d + "/" + e.Name()
		pid, perr := strconv.Atoi(e.Name())
		if !isSymlink(p) && perr == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
			n++
			continue
		}
		os.Remove(p)
	}
	return n, nil
}
