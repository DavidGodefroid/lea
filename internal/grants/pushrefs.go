package grants

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// A bare "git push origin <branch>" lets git choose the source: with no local branch of that
// name and a tag of it, git pushes the tag, as refs/tags/<branch>. parsePush cannot tell (it
// reads words, not the repository): RawPush reads the refs of the repository the push runs in.
// It answers a push no grant rewrote (a granted one pushes refs/heads/<branch>^{commit} to the
// full ref, pushScript). Against a hostile agent it is a check, not a lock: the agent runs as
// the repository's owner and could add the tag between the check and the push; branch
// protection on the remote remains the control that holds.
const (
	pushShadowed = "a tag is named like the agent's branch: git would push the tag (refs/tags/<branch>); delete the tag or push refs/heads/<branch>"
	pushNoBranch = "the agent's branch is not a local branch of the repository the push runs in: git would push whatever else has its name; push refs/heads/<branch>"
	pushNoRepo   = "lea cannot tell which repository the push runs in (cd or -C computed, cd -, a substitution): push from the worktree, plainly"
	pushAfter    = "a bare git push origin <branch> after commands that may change the refs (anything but cd, git add|commit|status|log|diff|show, echo): lea reads them before the command runs; push refs/heads/<branch>, or alone"
	pushRefsOdd  = "lea cannot read the refs of the repository the push runs in (reftable, a symlink or an odd file): push refs/heads/<branch>"
)

// maxPackedRefs bounds the packed-refs file RawPush reads.
const maxPackedRefs = 64 << 20

// RawPush returns why a Bash call's bare push of the agent's own branch is denied, or "": the
// repository it runs in must hold refs/heads/<branch> and no refs/tags/<branch>. Calls lea's
// hard denies let through only: any other refspec of the agent's branch is qualified
// (parsePush).
func RawPush(tool string, input json.RawMessage, c Caller) string {
	if tool != "Bash" || c.Branch == "" {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	cmd := in.Command
	if lit, ok := literalHeredocs(cmd); ok {
		cmd = lit
	}
	segs, inners, ok := segments(cmd)
	if !ok {
		return "" // hardGit denies a push it cannot read
	}
	for _, x := range inners {
		if holdsPush(x) {
			return pushNoRepo
		}
	}
	dir, known := "", false
	if strings.HasPrefix(c.Cwd, "/") {
		dir, known = path.Clean(c.Cwd), true
	}
	// moved: a command before the push may have changed the refs lea reads before any runs.
	moved := false
	for _, w := range segs {
		p := 0
		for p < len(w) && isAssignment(w[p]) {
			p++
		}
		if p == len(w) {
			continue
		}
		switch path.Base(w[p]) {
		case "cd", "pushd":
			dir, known = pushDir(dir, known, w[p+1:])
			continue
		case "popd":
			known = false
			continue
		case "echo", "printf", "true", "pwd":
			continue
		}
		if !isGit(w[p]) {
			moved = true
			continue
		}
		gdir, gknown, names := gitPushNames(w[p:], dir, known, c.Branch)
		if s := gitSub(w[p:]); s > 0 && refsKept[w[p+s]] {
			continue
		}
		for _, b := range names {
			if moved {
				return pushAfter
			}
			if !gknown {
				return pushNoRepo
			}
			if why := branchRefs(gdir, b); why != "" {
				return why
			}
		}
		moved = true // the push itself, a git call that may change the refs
	}
	return ""
}

// refsKept are the git subcommands that leave every ref but the branch's own as it is.
var refsKept = map[string]bool{"add": true, "commit": true, "status": true, "log": true, "diff": true, "show": true}

// pushDir is the directory cd args leaves the shell in: known false when lea cannot tell.
func pushDir(dir string, known bool, args []string) (string, bool) {
	if len(args) != 1 || !known {
		return dir, false
	}
	a := args[0]
	if a == "" || a == "-" || strings.HasPrefix(a, "~") || strings.HasPrefix(a, "-") || strings.ContainsAny(a, expanded+wild) {
		return dir, false
	}
	if strings.HasPrefix(a, "/") {
		return path.Clean(a), true
	}
	return path.Join(dir, a), true
}

// gitPushNames reads one git call (w[0] git) run in dir: when it is a push, the directory git
// runs in (-C) and the refspecs that leave the source to git, those naming the branch alone.
func gitPushNames(w []string, dir string, known bool, branch string) (string, bool, []string) {
	j := 1
	for ; j < len(w) && strings.HasPrefix(w[j], "-"); j++ {
		switch w[j] {
		case "-C":
			if j+1 < len(w) {
				dir, known = pushDir(dir, known, w[j+1:j+2])
				j++
			}
		case "-c":
			j++
		}
	}
	if j >= len(w) || w[j] != "push" {
		return dir, known, nil
	}
	var pos []string
	opts := true
	for i := j + 1; i < len(w); i++ {
		a := w[i]
		switch {
		case !opts || a == "-" || !strings.HasPrefix(a, "-"):
			pos = append(pos, a)
		case a == "--":
			opts = false
		case a == "--delete" || (!strings.HasPrefix(a, "--") && strings.Contains(a, "d")):
			return dir, known, nil // a deletion names the remote's ref, not a source
		case a == "-o" || a == "--push-option" || (!strings.HasPrefix(a, "--") && strings.HasSuffix(a, "o")):
			i++
		}
	}
	var names []string
	for _, r := range pos[min(1, len(pos)):] {
		if r == branch {
			names = append(names, r)
		}
	}
	return dir, known, names
}

// branchRefs checks the refs of the repository dir is in: refs/heads/b must be there,
// refs/tags/b must not. Whatever it cannot read is a deny.
func branchRefs(dir, b string) string {
	gitdir, ok := findGitDir(dir)
	if !ok {
		return pushNoBranch
	}
	common := gitdir
	if s, err := readSmall(filepath.Join(gitdir, "commondir"), 4096); err == nil {
		common = strings.TrimSpace(s)
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return pushRefsOdd
	}
	if _, err := os.Lstat(filepath.Join(common, "reftable")); !errors.Is(err, fs.ErrNotExist) {
		return pushRefsOdd
	}
	tag, branch := false, false
	switch _, err := os.Lstat(filepath.Join(common, "refs", "tags", b)); {
	case err == nil:
		tag = true
	case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
		return pushRefsOdd
	}
	switch fi, err := os.Lstat(filepath.Join(common, "refs", "heads", b)); {
	case err == nil && fi.Mode().IsRegular():
		branch = true
	case err == nil:
		return pushRefsOdd
	case !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR):
		return pushRefsOdd
	}
	pt, pb, err := packedRefs(filepath.Join(common, "packed-refs"), b)
	if err != nil {
		return pushRefsOdd
	}
	switch {
	case tag || pt:
		return pushShadowed
	case !branch && !pb:
		return pushNoBranch
	}
	return ""
}

// findGitDir is the git directory of the repository dir is in: the first .git up from dir, a
// directory or a "gitdir:" file (a linked worktree). A symlinked .git is not read.
func findGitDir(dir string) (string, bool) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		g := filepath.Join(d, ".git")
		fi, err := os.Lstat(g)
		switch {
		case err == nil && fi.IsDir():
			return g, true
		case err == nil && fi.Mode().IsRegular():
			s, err := readSmall(g, 4096)
			p, ok := strings.CutPrefix(strings.TrimSpace(s), "gitdir: ")
			if err != nil || !ok || p == "" {
				return "", false
			}
			if !filepath.IsAbs(p) {
				p = filepath.Join(d, p)
			}
			return filepath.Clean(p), true
		case err == nil, !errors.Is(err, fs.ErrNotExist):
			return "", false
		}
		if d == "/" || d == "." {
			return "", false
		}
	}
}

// openPlain opens a regular file, never through a symlink at its last component.
func openPlain(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, errors.New(p + ": not a regular file")
	}
	return f, nil
}

func readSmall(p string, n int64) (string, error) {
	f, err := openPlain(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil || int64(len(b)) > n {
		return "", errors.New(p + ": unreadable")
	}
	return string(b), nil
}

// packedRefs says whether packed-refs holds refs/tags/b and refs/heads/b; no file holds none.
func packedRefs(p, b string) (tag, branch bool, err error) {
	f, err := openPlain(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi.Size() > maxPackedRefs {
		return false, false, errors.New(p + ": too big")
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		_, name, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		tag = tag || name == "refs/tags/"+b
		branch = branch || name == "refs/heads/"+b
	}
	return tag, branch, sc.Err()
}
