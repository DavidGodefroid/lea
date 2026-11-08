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

	"github.com/charmbracelet/lipgloss"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// Folders group the deck's agents by the user's topics (#30). The layout (the folders, their
// order, their names, whether they are folded and the agents in each, in the user's order) is
// the user's own: the deck keeps it in a file of the host's data dir (LayoutPath), which no
// container mounts, and no agent call reads or writes it. Shells stay out of folders, at the end.
// An agent in no folder is at the root, after the folders, in launch order.

// Layout is what the deck persists of its folders. Saved is when the deck last wrote it (unix
// seconds, 0 for a layout written before #241). Known lists the agents the layout has already
// seen (with a lead or not, since #340), each with the second it was created in, oldest first (#296): an agent with a
// lead that is not in it, or was created later than its entry (a done agent's key respawned), is
// new to the layout, and joins its lead's folder (fileSpawned). Format is layoutKnown once Known is the
// record; a layout written before #296 (0) only has Saved, and an agent started later than
// that is the one new to it.
type Layout struct {
	Folders []Folder `json:"folders"`
	Saved   int64    `json:"saved,omitempty"`
	Format  int      `json:"format,omitempty"`
	Known   []Seen   `json:"known,omitempty"`
}

// Seen is one agent the layout has seen: its key and the second that incarnation was created in.
// store.Register reuses a done agent's key for a new agent; a later Created tells it apart.
type Seen struct {
	Key     string `json:"key"`
	Created int64  `json:"created"`
}

// layoutKnown is the Format of a layout whose Known list decides which agents are new (#296).
const layoutKnown = 1

// Folder is one folder: its stable id, the user's name, whether it is folded, its colour (a
// folderColours name, "" for none), and the keys of its agents in the user's order.
type Folder struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Folded bool     `json:"folded,omitempty"`
	Color  string   `json:"color,omitempty"`
	Agents []string `json:"agents,omitempty"`
}

// folderColours are the colours a folder can take, by name: accents of the current theme
// (agentColors), without the reds and greens a state dot uses. Every theme has all 8 (#337): a
// saved folder keeps a colour after a theme switch.
var folderColours = []struct {
	name   string
	accent int // the colour's index in agentColors
}{
	{"pink", 2}, {"mauve", 3}, {"peach", 6}, {"yellow", 7}, {"teal", 9}, {"sky", 10}, {"blue", 12}, {"lavender", 13},
}

// folderColour is the colour of name; ok is false for "" and for a name the deck does not know.
func folderColour(name string) (lipgloss.AdaptiveColor, bool) {
	for _, c := range folderColours {
		if c.name == name {
			return agentColors[c.accent], true
		}
	}
	return lipgloss.AdaptiveColor{}, false
}

// LayoutStore keeps the layout across deck restarts.
type LayoutStore interface {
	Load() (Layout, error)
	Save(Layout) error
}

const (
	maxFolders      = 32
	maxFolderAgents = 256
	// maxKnown bounds Known: past it, the oldest keys go first.
	maxKnown = 1024
	// maxLayoutBytes leaves room for a full Known of the longest keys (about 160 KiB).
	maxLayoutBytes = 256 << 10
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
	if l.Saved < 0 {
		return fmt.Errorf("%w: saved time", errBadLayout)
	}
	if l.Format != 0 && l.Format != layoutKnown {
		return fmt.Errorf("%w: format %d", errBadLayout, l.Format)
	}
	if len(l.Known) > maxKnown {
		return fmt.Errorf("%w: more than %d known agents", errBadLayout, maxKnown)
	}
	known := map[string]bool{}
	for _, k := range l.Known {
		if !agentKey.MatchString(k.Key) || known[k.Key] || k.Created < 0 {
			return fmt.Errorf("%w: known agent key %q", errBadLayout, k.Key)
		}
		known[k.Key] = true
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for _, f := range l.Folders {
		if !folderID.MatchString(f.ID) || ids[f.ID] {
			return fmt.Errorf("%w: folder id %q", errBadLayout, f.ID)
		}
		if !validFolderName(f.Name) {
			return fmt.Errorf("%w: a folder name", errBadLayout)
		}
		if _, ok := folderColour(f.Color); f.Color != "" && !ok {
			return fmt.Errorf("%w: folder colour %q", errBadLayout, f.Color)
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
	if isAllowKey(key) {
		return m.hasAllowance(allowKeyID(key))
	}
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
	out := Layout{Folders: make([]Folder, len(l.Folders)), Saved: l.Saved, Format: l.Format, Known: append([]Seen(nil), l.Known...)}
	for i, f := range l.Folders {
		f.Agents = append([]string(nil), f.Agents...)
		out.Folders[i] = f
	}
	return out
}

// save writes the layout, without the agents no longer listed (an agent removed for good
// leaves its folder and Known). A failure shows in the status; the deck goes on with the layout
// in hand.
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
		kept := m.layout.Known[:0:0]
		for _, k := range m.layout.Known {
			if m.index(k.Key) >= 0 {
				kept = append(kept, k)
			}
		}
		m.layout.Known = kept
	}
	m.layout.Saved = m.now().Unix()
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
	m.editTitle = "New folder"
	m.closeMenu()
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

// moveOut puts agent key back at the root (u); an agent at the root or a shell stays as it is.
func (m Model) moveOut(key string) Model {
	if m.folderOf(key) < 0 {
		return m
	}
	return m.moveAgent(key, -1)
}

// foldFrom folds the folder holding agent key (h on an agent inside it) and selects its row.
func (m Model) foldFrom(key string) Model {
	fi := m.folderOf(key)
	if fi < 0 {
		return m
	}
	fk := folderKey(m.layout.Folders[fi].ID)
	m.selected = fk
	m.seq++ // drops a pending delayed show
	if !m.layout.Folders[fi].Folded {
		m = m.toggleFold(fk)
	}
	return m
}

// setColour gives folder key the colour name ("" for none); an unknown name changes nothing.
func (m Model) setColour(key, name string) Model {
	fi := m.folderAt(key)
	if _, ok := folderColour(name); fi < 0 || name != "" && !ok {
		return m
	}
	m.layout = cloneLayout(m.layout)
	m.layout.Folders[fi].Color = name
	m.save()
	return m
}

// fileSpawned puts each agent with a lead (Parent) that is new to the layout (not in Known, or
// created later than its Known entry: a done agent's key store.Register gave a new agent) and
// in no folder into the folder of its lead, at its end. It runs on every snapshot, and
// records every agent it lists in Known, filed or not, so it files an agent once: an agent the
// user moves out is never moved back, even after a deck restart (the move saves Known). An
// agent started while no deck was attached is new too, and is filed on the next attach. Known
// is a record per agent, not a save time: an agent created in the same second as a save
// (another child filed, a fold, a move) is still new (#296).
//
// A renewed key (in Known, created later) first leaves the folder its previous agent was in,
// lead or not: the new agent follows its own lead, or stays at the root without one, never the
// folder of the agent that had its name before (#340). Agent names are per container, so the
// previous agent may have had another lead.
//
// A layout written before #296 (Format 0) first takes as known the agents it lists that started
// no later than its last save, all of them for a layout that never recorded one (written before
// #241): the agents already listed keep their place.
func (m *Model) fileSpawned() {
	if m.layout.Format != layoutKnown {
		m.layout = cloneLayout(m.layout)
		m.layout.Format = layoutKnown
		for _, a := range m.agents {
			if !isShell(a) && (m.layout.Saved == 0 || a.Created <= m.layout.Saved) {
				m.layout.Known = append(m.layout.Known, Seen{Key: a.Key, Created: a.Created})
			}
		}
	}
	known := make(map[string]int64, len(m.layout.Known))
	for _, k := range m.layout.Known {
		known[k.Key] = k.Created
	}
	var fresh []Agent
	renewed := map[string]bool{}
	for _, a := range m.agents {
		if isShell(a) || !agentKey.MatchString(a.Key) || a.Created < 0 {
			continue
		}
		c, ok := known[a.Key]
		if ok && a.Created <= c {
			continue
		}
		fresh = append(fresh, a)
		renewed[a.Key] = ok
	}
	if len(fresh) == 0 {
		return
	}
	m.layout = cloneLayout(m.layout)
	kept := m.layout.Known[:0]
	for _, k := range m.layout.Known {
		if !renewed[k.Key] {
			kept = append(kept, k)
		}
	}
	m.layout.Known = kept
	changed := false
	for _, a := range fresh {
		m.layout.Known = append(m.layout.Known, Seen{Key: a.Key, Created: a.Created})
		if renewed[a.Key] && m.folderOf(a.Key) >= 0 {
			m.layout, changed = withAgentIn(m.layout, a.Key, -1), true
		}
		if a.Parent == "" || m.folderOf(a.Key) >= 0 {
			continue
		}
		fi := m.folderOf(a.Parent)
		if fi < 0 || len(m.layout.Folders[fi].Agents) >= maxFolderAgents {
			continue
		}
		m.layout, changed = withAgentIn(m.layout, a.Key, fi), true
	}
	if n := len(m.layout.Known) - maxKnown; n > 0 {
		m.layout.Known = append([]Seen(nil), m.layout.Known[n:]...)
	}
	if changed {
		m.save()
	}
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
