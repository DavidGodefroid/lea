// Package transcript repairs a Claude Code session transcript before lea resumes it.
//
// A transcript is a JSONL file, one record per line, that Claude Code appends to. A crash or a
// killed container can leave its last line cut mid-write. Claude Code 2.1.296 resumes such a
// session, but it appends its next record to the cut line without a newline first: the two make
// one invalid line, and that record is lost to every reader (#219, spike SP2 b). lea therefore
// cuts the file after its last valid line before `claude --resume`, and keeps the file as it was.
package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// session is a Claude Code session id: a UUID, as lea passes to --resume.
var session = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// BackupSuffix is the start of the suffix of a repaired transcript's backup: <file>.lea-cut-<time>.
// It does not end in .jsonl, so Claude Code never lists it as a session.
const BackupSuffix = ".lea-cut-"

// Repaired is one repaired transcript: the file, the copy of it as it was, and the bytes dropped.
type Repaired struct {
	File, Backup string
	Dropped      int64
}

// RepairSession repairs the transcripts of session under configDir (Claude Code's configuration
// directory: CLAUDE_CONFIG_DIR, else ~/.claude), one per project directory: configDir/projects/
// <project>/<session>.jsonl. An id that is not a UUID repairs nothing.
func RepairSession(configDir, id string) ([]Repaired, error) {
	if !session.MatchString(id) {
		return nil, nil
	}
	fs, err := filepath.Glob(filepath.Join(configDir, "projects", "*", id+".jsonl"))
	if err != nil {
		return nil, err
	}
	var out []Repaired
	var errs []error
	for _, f := range fs {
		r, err := Repair(f)
		if err != nil {
			errs = append(errs, err)
		} else if r != nil {
			out = append(out, *r)
		}
	}
	return out, errors.Join(errs...)
}

// Repair makes transcript f safe to resume: when its last line is incomplete (no newline after it)
// or not valid JSON, it copies f to a backup next to it, then cuts f after its last valid line (a
// last line that is valid JSON but lacks its newline gets one instead; nothing is dropped). Earlier
// invalid lines stay: they are history, which Claude Code skips. nil when f needed nothing. f must
// be a regular file (a link is left alone).
func Repair(f string) (*Repaired, error) {
	st, err := os.Lstat(f)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", f)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}
	keep, newline := tail(b)
	if keep == len(b) && !newline {
		return nil, nil
	}
	backup, err := writeBackup(f, b, st.Mode().Perm())
	if err != nil {
		return nil, err
	}
	w, err := os.OpenFile(f, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	err = w.Truncate(int64(keep))
	if err == nil && newline {
		_, err = w.WriteAt([]byte{'\n'}, int64(keep))
	}
	if err == nil {
		err = w.Sync()
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("repair %s (its copy is %s): %w", f, backup, err)
	}
	return &Repaired{File: f, Backup: backup, Dropped: int64(len(b) - keep)}, nil
}

// tail is how much of transcript b to keep: up to the end of its last valid line, its newline
// included, and whether that line still needs its newline (a valid last line without one).
func tail(b []byte) (keep int, newline bool) {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end-- // b[:end] ends where the last line ends
	}
	for {
		start := bytes.LastIndexByte(b[:end], '\n') + 1
		if line := b[start:end]; len(bytes.TrimSpace(line)) > 0 && json.Valid(line) {
			if end == len(b) {
				return end, true
			}
			return end + 1, false
		}
		if start == 0 {
			return 0, false
		}
		end = start - 1
	}
}

// writeBackup writes b to a new file next to f, f's name with BackupSuffix and the time, mode perm.
func writeBackup(f string, b []byte, perm os.FileMode) (string, error) {
	base := f + BackupSuffix + time.Now().UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		p := base
		if i > 0 {
			p = fmt.Sprintf("%s-%d", base, i)
		}
		w, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) && i < 100 {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = w.Write(b)
		if err == nil {
			err = w.Sync()
		}
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p)
			return "", err
		}
		return p, nil
	}
}
