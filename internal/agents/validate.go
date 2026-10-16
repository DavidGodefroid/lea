package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

func controlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// checkWorkdir accepts a clean absolute path without control characters.
func checkWorkdir(p string) error {
	switch {
	case p == "":
		return errors.New("no working directory for the agent: open the deck with lea first")
	case !filepath.IsAbs(p) || filepath.Clean(p) != p:
		return fmt.Errorf("working directory %q must be a clean absolute path", p)
	case len(p) > 4096 || controlChar(p):
		return fmt.Errorf("working directory %q is too long or contains a control character", p)
	}
	return nil
}

// checkArgs applies spec §5.4's argv rules: at most 64 arguments, 8 KiB, no control characters.
func checkArgs(args []string) error {
	if len(args) > MaxArgs {
		return fmt.Errorf("at most %d agent arguments", MaxArgs)
	}
	n := 0
	for _, a := range args {
		n += len(a)
		if controlChar(a) {
			return errors.New("agent arguments must not contain control characters")
		}
	}
	if n > MaxArgBytes {
		return fmt.Errorf("agent arguments exceed %d bytes", MaxArgBytes)
	}
	return nil
}

// MaxBrief caps a brief: it becomes one message (64 KiB limit), with room for a request's
// envelope (spawn requests carry a brief and a reason).
const MaxBrief = 32 << 10

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
)

// CheckRepo accepts "" or a directory name directly under the projects directory: no path
// separator, no leading dot (".lea", ".."), at most 64 characters.
func CheckRepo(r string) error {
	if r == "" || repoRe.MatchString(r) {
		return nil
	}
	return fmt.Errorf("invalid repo %q: the name of a directory directly under the projects directory", r)
}

// CheckBranch accepts "" or a plain git branch name: letters, digits, '.', '_', '-' and '/',
// starting with a letter or digit, without "..", "//", "/." or a trailing '/', '.' or ".lock".
func CheckBranch(b string) error {
	if b == "" {
		return nil
	}
	if !branchRe.MatchString(b) || strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "/.") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") {
		return fmt.Errorf("invalid branch %q", b)
	}
	return nil
}

// repoDir is a repo's directory: it must exist under projects and be a directory, not a symlink.
// Only its metadata is read; nothing in it runs on the host.
func repoDir(projects, repo string) (string, error) {
	p := filepath.Join(projects, repo)
	fi, err := os.Lstat(p)
	if err != nil {
		return "", fmt.Errorf("repo %s: no such directory under the projects directory", repo)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", fmt.Errorf("repo %s is not a directory of the projects directory", repo)
	}
	return p, nil
}

// MaxLabel is the longest display label, in characters.
const MaxLabel = 24

// CheckLabel checks a display label the user gives an agent and returns it trimmed: valid UTF-8,
// at most MaxLabel characters, none of them a control or invisible character (textsafe). An
// empty label (after trimming) clears the agent's label.
func CheckLabel(label string) (string, error) {
	if !utf8.ValidString(label) {
		return "", errors.New("the label is not valid UTF-8")
	}
	for _, r := range label {
		if textsafe.Unsafe(r) {
			return "", fmt.Errorf("the label contains a control or invisible character (U+%04X)", r)
		}
	}
	label = strings.TrimSpace(label)
	if n := utf8.RuneCountInString(label); n > MaxLabel {
		return "", fmt.Errorf("the label is %d characters long, at most %d", n, MaxLabel)
	}
	return label, nil
}
