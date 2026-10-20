package launcher

import (
	"path/filepath"
	"strings"
)

// physDir is lea_abspath of an existing directory: (cd -P dir && pwd -P).
func physDir(d string) string {
	a, err := filepath.Abs(d)
	if err != nil {
		return d
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		return r
	}
	return a
}

// phys is lea__phys: the physical path of p, or of its parent when p does not exist yet.
func phys(p string) string {
	if isDir(p) {
		return physDir(p)
	}
	if i := strings.LastIndex(p, "/"); i >= 0 && isDir(p[:i]) {
		return physDir(p[:i]) + "/" + p[i+1:]
	}
	return p
}

// under is lea__under: b is a or below it.
func under(a, b string) bool { return a == "/" || b == a || strings.HasPrefix(b, a+"/") }

// projectsDir is lea_projects_dir: the physical path of $LEA_PROJECTS_DIR.
func (l *Launcher) projectsDir() (string, error) {
	p := l.Cfg.Get("LEA_PROJECTS_DIR")
	if p == "" {
		return "", die("no projects directory: run 'lea init', or set LEA_PROJECTS_DIR")
	}
	if !isDir(p) {
		return "", die("LEA_PROJECTS_DIR=%s is not a directory", p)
	}
	return physDir(p), nil
}

// guardProjects is lea__guard_projects: refuses /, $HOME and its ancestors, and any directory
// that is, contains or lies inside ~/.claude, the lea data dir or the lea installation.
func (l *Launcher) guardProjects(p string) error {
	home := l.Cfg.Get("HOME")
	if p == "/" || under(p, phys(home)) {
		return die("%s cannot be a projects directory: it is / or contains your home directory", p)
	}
	for _, x := range []string{phys(home + "/.claude"), phys(l.Cfg.DataDir()), l.Root} {
		if under(p, x) || under(x, p) {
			return die("%s cannot be a projects directory: it overlaps %s", p, x)
		}
	}
	return nil
}

// checkPaths is lea__check_paths: before any docker call, a symlink on any path the launcher
// later mounts, creates, writes or removes for this projects dir is refused.
func (l *Launcher) checkPaths(p string) error {
	d, s, h, id := l.Cfg.DataDir(), l.Cfg.StateDir(), l.Cfg.HostDir(), ID(p)
	paths := []string{s + "/claude.json", s + "/claude/" + id + "/projects/" + Slug(p), s + "/ssh",
		s + "/clipboard/copy"}
	for _, g := range gitStateDirs {
		paths = append(paths, s+"/"+g)
	}
	paths = append(paths, s+"/containers", s+"/go", s+"/kube", s+"/codex/"+id,
		h+"/locks/"+id, h+"/locks/global", h+"/sessions/"+id, h+"/scan/"+id+".start")
	for _, x := range paths {
		if err := SafePath(d, x); err != nil {
			return die("%v", err)
		}
	}
	home := l.Cfg.Get("HOME")
	if err := SafePath(home+"/.claude", home+"/.claude/projects/"+Slug(p)); err != nil {
		return die("%v", err)
	}
	return nil
}

// scanStartFile is lea_scan_start_file: the start snapshot, in the host-only dir.
func (l *Launcher) scanStartFile(p string) string {
	return l.Cfg.HostDir() + "/scan/" + ID(p) + ".start"
}

// cwdIn is lea__up's working directory: the launcher's physical working directory when it lies
// in p (`case "$_wd/" in "$p"/*)`), else p.
func (l *Launcher) cwdIn(p string) string {
	if strings.HasPrefix(l.Cwd+"/", p+"/") {
		return l.Cwd
	}
	return p
}
