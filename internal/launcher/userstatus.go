package launcher

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DavidGodefroid/lea/internal/statusline"
)

// maxStatusFiles bounds how many files of a status line command lea mounts.
const maxStatusFiles = 4

// statusLineFiles are the files below ~/.claude (c) that the user's status line command in the
// Claude settings file at settings names (#374), relative to c: `lea statusline` runs that command
// inside the container, so its script is mounted read-only. Only real files are named, never a
// hidden one (.credentials.json), the transcripts nor what lea already mounts or copies; a path
// outside ~/.claude is never mounted (the command then fails and lea prints its own line). A path
// with a symbolic link on its way is not followed: it is returned in skipped.
func statusLineFiles(c, settings string) (files, skipped []string) {
	cmd := statusline.UserCommand(settings)
	if cmd == "" {
		return nil, nil
	}
	home := filepath.Dir(c)
	for _, w := range strings.Fields(cmd) {
		w = strings.Trim(w, `'";&|()`)
		for _, pre := range []string{"~/", "$HOME/", "${HOME}/"} {
			if strings.HasPrefix(w, pre) {
				w = home + "/" + strings.TrimPrefix(w, pre)
			}
		}
		if !strings.HasPrefix(w, c+"/") || w != filepath.Clean(w) || strings.Contains(w, ",") {
			continue
		}
		rel := strings.TrimPrefix(w, c+"/")
		parts := strings.Split(rel, "/")
		hidden := slices.ContainsFunc(parts, func(p string) bool { return strings.HasPrefix(p, ".") })
		top := parts[0]
		owned := slices.Contains(claudeShared, top) || slices.Contains(claudeLayered, top) || top == "projects" ||
			len(parts) == 1 && strings.HasSuffix(top, ".md")
		if hidden || owned || slices.Contains(files, rel) {
			continue
		}
		if SafePath(c, w) != nil {
			skipped = append(skipped, w)
			continue
		}
		if !isScript(w) {
			continue
		}
		if files = append(files, rel); len(files) == maxStatusFiles {
			break
		}
	}
	return files, skipped
}

// scriptExts are the extensions of a script a command may run without its executable bit.
var scriptExts = []string{".sh", ".bash", ".zsh", ".fish", ".py", ".js", ".mjs", ".cjs", ".ts", ".rb", ".pl", ".php", ".lua"}

// isScript tells a regular file that is executable or named as a script: what a status line
// command runs, not the data (history.jsonl...) it might also name.
func isScript(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0 || slices.Contains(scriptExts, filepath.Ext(p))
}

// statusLineMounts mounts read-only the files of the user's status line command named by the
// container's copy of the settings (priv/settings.json), their mount points made in priv.
func (l *Launcher) statusLineMounts(src, priv, sbase string) ([]string, error) {
	files, skipped := statusLineFiles(src, priv+"/settings.json")
	for _, p := range skipped {
		l.logf("skipped %s for your status line: a symbolic link is on its path (lea mounts only real entries of ~/.claude)", p)
	}
	var a []string
	for _, rel := range files {
		if d := filepath.Dir(rel); d != "." {
			if err := Mkdirs(sbase, priv+"/"+d); err != nil {
				return nil, die("%v", err)
			}
		}
		m, err := l.claudeRO(sbase, src+"/"+rel, priv+"/"+rel, src+"/"+rel)
		if err != nil {
			return nil, err
		}
		a = append(a, m...)
	}
	return a, nil
}
