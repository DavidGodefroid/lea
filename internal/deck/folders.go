package deck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// Folders group the deck's agents by the user's topics (#30). The layout (the folders, their
// order, their names, whether they are folded and the agents in each, in the user's order) is
// the user's own: the deck keeps it in a file of the host's data dir (LayoutPath), which no
// container mounts, and no agent call reads or writes it. Shells stay out of folders, at the end.
// An agent in no folder is at the root, after the folders, in launch order.

// Layout is what the deck persists of its folders.
type Layout struct {
	Folders []Folder `json:"folders"`
}

// Folder is one folder: its stable id, the user's name, whether it is folded, and the keys of
// its agents in the user's order.
type Folder struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Folded bool     `json:"folded,omitempty"`
	Agents []string `json:"agents,omitempty"`
}

// LayoutStore keeps the layout across deck restarts.
type LayoutStore interface {
	Load() (Layout, error)
	Save(Layout) error
}

const (
	maxFolders      = 32
	maxFolderAgents = 256
	maxLayoutBytes  = 64 << 10
	// folderPrefix starts a folder row's key; no agent key holds a NUL (deckMenu).
	folderPrefix = "\x00folder:"
)

var (
	folderID      = regexp.MustCompile(`^f[1-9][0-9]{0,5}$`)
	agentKey      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}/[A-Za-z0-9:._-]{1,64}$`)
	layoutName    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	errBadLayout  = errors.New("deck layout refused")
	errLayoutType = errors.New("deck layout is not a regular file")
)

// validFolderName reports whether s is a folder name the deck accepts: 1 to maxLabel
// characters, not only spaces, no control or invisible character (textsafe).
func validFolderName(s string) bool {
	n := 0
	for _, r := range s {
		if textsafe.Unsafe(r) {
			return false
		}
		n++
	}
	return n >= 1 && n <= maxLabel && strings.TrimSpace(s) != ""
}

// check refuses a layout the deck would never write: too many folders or agents, a bad id or
// name, an agent key that is no agent's, or an agent in two folders.
func (l Layout) check() error {
	if len(l.Folders) > maxFolders {
		return fmt.Errorf("%w: more than %d folders", errBadLayout, maxFolders)
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, f := range l.Folders {
		if !folderID.MatchString(f.ID) || ids[f.ID] {
			return fmt.Errorf("%w: folder id %q", errBadLayout, f.ID)
		}
		if !validFolderName(f.Name) {
			return fmt.Errorf("%w: a folder name", errBadLayout)
		}
		if len(f.Agents) > maxFolderAgents {
			return fmt.Errorf("%w: more than %d agents in a folder", errBadLayout, maxFolderAgents)
		}
		ids[f.ID] = true
		for _, k := range f.Agents {
			if !agentKey.MatchString(k) || keys[k] {
				return fmt.Errorf("%w: agent key %q", errBadLayout, k)
			}
			keys[k] = true
		}
	}
	return nil
}

// LayoutPath is where the deck of container keeps its layout: the host's data dir (0700, never
// mounted in a container), deck/<container>.json. ok is false for a container name lea never
// makes: no path is derived from it.
func LayoutPath(hostDir, container string) (string, bool) {
	if hostDir == "" || !layoutName.MatchString(container) {
		return "", false
	}
	return filepath.Join(hostDir, "deck", container+".json"), true
}

// FileStore keeps the layout in one file, written whole and atomically (a temporary file in the
// same directory renamed over it), mode 0600 in a 0700 directory; a symlink is refused at both.
type FileStore struct{ Path string }

func (s FileStore) Load() (Layout, error) {
	f, err := os.OpenFile(s.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return Layout{}, nil
	}
	if err != nil {
		return Layout{}, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return Layout{}, errLayoutType
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLayoutBytes+1))
	if err != nil {
		return Layout{}, err
	}
	if len(b) > maxLayoutBytes {
		return Layout{}, fmt.Errorf("%w: larger than %d bytes", errBadLayout, maxLayoutBytes)
	}
	var l Layout
	if err := json.Unmarshal(b, &l); err != nil {
		return Layout{}, fmt.Errorf("%w: %v", errBadLayout, err)
	}
	if err := l.check(); err != nil {
		return Layout{}, err
	}
	return l, nil
}

func (s FileStore) Save(l Layout) error {
	if err := l.check(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is a symlink or not a directory, refused", dir)
	} else if fi.Mode().Perm()&^0o700 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	if fi, err := os.Lstat(s.Path); err == nil && !fi.Mode().IsRegular() {
		return errLayoutType
	}
	tmp, err := os.CreateTemp(dir, ".layout-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

// folderKey is the row key of folder id.
func folderKey(id string) string { return folderPrefix + id }

// isFolderKey reports whether a row key is a folder's.
func isFolderKey(k string) bool { return strings.HasPrefix(k, folderPrefix) }

// folderAt is the index of the folder whose row key is k, -1 when none.
func (m Model) folderAt(k string) int {
	if !isFolderKey(k) {
		return -1
	}
	id := strings.TrimPrefix(k, folderPrefix)
	for i, f := range m.layout.Folders {
		if f.ID == id {
			return i
		}
	}
	return -1
}

// folderOf is the index of the folder holding agent key, -1 at the root.
func (m Model) folderOf(key string) int {
	for i, f := range m.layout.Folders {
		for _, k := range f.Agents {
			if k == key {
				return i
			}
		}
	}
	return -1
}

// entry is one row of the list: a folder (agent -1) or an agent; in is the folder it lies in
// (-1 at the root), its own index for a folder.
type entry struct {
	key       string
	agent, in int
}

// entries are the rows in display order: each folder, then, unless folded, its agents in the
// user's order; then the agents at the root in launch order, shells last. all lists the agents
// of folded folders too.
func (m Model) entries(all bool) []entry {
	at := make(map[string]int, len(m.agents))
	for i, a := range m.agents {
		at[a.Key] = i
	}
	inFolder := map[string]bool{}
	var out []entry
	for fi, f := range m.layout.Folders {
		out = append(out, entry{folderKey(f.ID), -1, fi})
		for _, k := range f.Agents {
			i, ok := at[k]
			if !ok || isShell(m.agents[i]) || inFolder[k] {
				continue
			}
			inFolder[k] = true
			if all || !f.Folded {
				out = append(out, entry{k, i, fi})
			}
		}
	}
	for i, a := range m.agents {
		if !inFolder[a.Key] {
			out = append(out, entry{a.Key, i, -1})
		}
	}
	return out
}

// pos is the display position of row key, -1 when it is not shown.
func (m Model) pos(key string) int {
	for i, e := range m.entries(false) {
		if e.key == key {
			return i
		}
	}
	return -1
}

// exists reports whether key is a listed agent or a folder.
func (m Model) exists(key string) bool {
	if isFolderKey(key) {
		return m.folderAt(key) >= 0
	}
	return m.index(key) >= 0
}

// folderMembers are the listed agents of folder fi, in its order.
func (m Model) folderMembers(fi int) []Agent {
	var out []Agent
	for _, e := range m.entries(true) {
		if e.agent >= 0 && e.in == fi {
			out = append(out, m.agents[e.agent])
		}
	}
	return out
}

// folderState is the state a folder's dot shows, the worst of its agents' as a row's dot shows
// it: blocked when one needs the user, working while one works (a paused agent does not), else
// idle with its latest agent's time (green, then empty once all have been quiet a while), ""
// when no agent inside has a state to show.
func (m Model) folderState(fi int) (string, int64) {
	state, updated := "", int64(0)
	for _, a := range m.folderMembers(fi) {
		s := shownState(a)
		switch {
		case a.Paused:
			continue
		case s == "blocked":
			return "blocked", a.Updated
		case busy(a): // working or waiting (#72)
			state = "working"
		case s == "limited":
			if state == "" || state == "idle" {
				state = "limited"
			}
			updated = max(updated, a.Updated)
		case s == "idle" || s == "done":
			if state == "" {
				state = "idle"
			}
			updated = max(updated, a.Updated)
		}
	}
	return state, updated
}

// nextFolderID is an id no folder has.
func (m Model) nextFolderID() string {
	n := 0
	for _, f := range m.layout.Folders {
		if v, err := strconv.Atoi(strings.TrimPrefix(f.ID, "f")); err == nil {
			n = max(n, v)
		}
	}
	return "f" + strconv.Itoa(n+1)
}

// cloneLayout copies l, so that a change never shows through an older model.
func cloneLayout(l Layout) Layout {
	out := Layout{Folders: make([]Folder, len(l.Folders))}
	for i, f := range l.Folders {
		f.Agents = append([]string(nil), f.Agents...)
		out.Folders[i] = f
	}
	return out
}

// save writes the layout, without the agents no longer listed (an agent removed for good
// leaves its folder). A failure shows in the status; the deck goes on with the layout in hand.
func (m *Model) save() {
	if m.version > 0 {
		for i := range m.layout.Folders {
			f := &m.layout.Folders[i]
			kept := f.Agents[:0:0]
			for _, k := range f.Agents {
				if m.index(k) >= 0 {
					kept = append(kept, k)
				}
			}
			f.Agents = kept
		}
	}
	if m.store == nil {
		return
	}
	if err := m.store.Save(m.layout); err != nil {
		m.status = "layout not saved: " + oneLine(err.Error())
	}
}

// newFolder adds a folder after the others, holding agent key unless it is "", selects it and
// opens its name field.
func (m Model) newFolder(key string) (Model, bool) {
	if len(m.layout.Folders) >= maxFolders {
		m.status = fmt.Sprintf("at most %d folders", maxFolders)
		return m, false
	}
	m.layout = cloneLayout(m.layout)
	id := m.nextFolderID()
	m.layout.Folders = append(m.layout.Folders, Folder{ID: id, Name: "folder " + strings.TrimPrefix(id, "f")})
	if key != "" {
		m.layout = withAgentIn(m.layout, key, len(m.layout.Folders)-1)
	}
	m.save()
	fk := folderKey(id)
	m.selected, m.editFor, m.draft = fk, fk, []rune(m.layout.Folders[len(m.layout.Folders)-1].Name)
	m.menuFor, m.menuPicker = "", false
	return m, true
}

// withAgentIn moves agent key into folder fi (at its end), or to the root when fi is -1.
func withAgentIn(l Layout, key string, fi int) Layout {
	for i := range l.Folders {
		f := &l.Folders[i]
		for j, k := range f.Agents {
			if k == key {
				f.Agents = append(f.Agents[:j:j], f.Agents[j+1:]...)
				break
			}
		}
	}
	if fi >= 0 && fi < len(l.Folders) && len(l.Folders[fi].Agents) < maxFolderAgents {
		l.Folders[fi].Agents = append(l.Folders[fi].Agents, key)
	}
	return l
}

// moveAgent puts agent key in folder fi (-1: the root) and keeps it selected and in view.
func (m Model) moveAgent(key string, fi int) Model {
	a := m.index(key)
	if a < 0 || isShell(m.agents[a]) {
		return m
	}
	m.layout = withAgentIn(cloneLayout(m.layout), key, fi)
	if fi >= 0 {
		m.layout.Folders[fi].Folded = false
	}
	m.selected = key
	m.save()
	return m
}

// renameFolder gives folder key the name s; an invalid or empty name keeps the old one.
func (m Model) renameFolder(key, s string) Model {
	fi := m.folderAt(key)
	s = strings.TrimSpace(s)
	if fi < 0 || !validFolderName(s) {
		return m
	}
	m.layout = cloneLayout(m.layout)
	m.layout.Folders[fi].Name = s
	m.save()
	return m
}

// deleteFolder removes folder key; its agents go back to the root, never stopped.
func (m Model) deleteFolder(key string) Model {
	fi := m.folderAt(key)
	if fi < 0 {
		return m
	}
	members := m.folderMembers(fi)
	m.layout = cloneLayout(m.layout)
	m.layout.Folders = append(m.layout.Folders[:fi], m.layout.Folders[fi+1:]...)
	m.selected = ""
	if len(members) > 0 {
		m.selected = members[0].Key
	} else if fi < len(m.layout.Folders) {
		m.selected = folderKey(m.layout.Folders[fi].ID)
	} else if es := m.entries(false); len(es) > 0 {
		m.selected = es[0].key
	}
	m.save()
	return m
}

// toggleFold folds or unfolds folder key.
func (m Model) toggleFold(key string) Model {
	fi := m.folderAt(key)
	if fi < 0 {
		return m
	}
	m.layout = cloneLayout(m.layout)
	m.layout.Folders[fi].Folded = !m.layout.Folders[fi].Folded
	m.save()
	return m
}

// unfoldFor unfolds the folder holding agent key, so that it can be selected.
func (m *Model) unfoldFor(key string) {
	if fi := m.folderOf(key); fi >= 0 && m.layout.Folders[fi].Folded {
		m.layout = cloneLayout(m.layout)
		m.layout.Folders[fi].Folded = false
		m.save()
	}
}

// shift moves the selected row one place up (-1) or down (+1): a folder among the folders, an
// agent among its folder's agents. An agent at the root keeps its launch order.
func (m Model) shift(d int) Model {
	if fi := m.folderAt(m.selected); fi >= 0 {
		j := fi + d
		if j < 0 || j >= len(m.layout.Folders) {
			return m
		}
		m.layout = cloneLayout(m.layout)
		fs := m.layout.Folders
		fs[fi], fs[j] = fs[j], fs[fi]
		m.save()
		return m
	}
	fi := m.folderOf(m.selected)
	if fi < 0 {
		return m
	}
	// Among the agents still listed: a stale key between two rows takes no move.
	var listed []int
	at := -1
	for j, k := range m.layout.Folders[fi].Agents {
		if m.index(k) >= 0 {
			if k == m.selected {
				at = len(listed)
			}
			listed = append(listed, j)
		}
	}
	if at < 0 || at+d < 0 || at+d >= len(listed) {
		return m
	}
	m.layout = cloneLayout(m.layout)
	ks := m.layout.Folders[fi].Agents
	a, b := listed[at], listed[at+d]
	ks[a], ks[b] = ks[b], ks[a]
	m.save()
	return m
}
