package deck

import (
	"errors"
	"testing"
)

// lea web edits the deck's layout file (#416): a watching deck takes the folders it finds there.
func TestTheDeckTakesTheFoldersLeaWebSaved(t *testing.T) {
	st := &memStore{}
	m := withFolders(t, st, ag("a", "idle", 1), ag("b", "idle", 1))
	if m.readLayout() != nil {
		t.Fatal("a deck that does not watch its layout reads it again")
	}
	m = m.WithLayoutWatch()
	if m.readLayout() == nil {
		t.Fatal("no read of the layout")
	}
	m.selected = "lea-a/b"
	web := Layout{Folders: []Folder{{ID: "f1", Name: "web", Folded: true, Agents: []string{"lea-a/b"}}}}
	m, cmd := m.takeLayout(layoutMsg{layout: web})
	if cmd == nil || order(m) != "[web] a" {
		t.Fatalf("order %q", order(m))
	}
	if m.selected != folderKey("f1") {
		t.Fatalf("selected %q: b is folded away", m.selected)
	}
	if st.saves != 0 {
		t.Fatal("taking the layout wrote it back")
	}
	// A layout that cannot be read, or one the user is editing here, changes nothing.
	m2, _ := m.takeLayout(layoutMsg{err: errors.New("refused")})
	if order(m2) != "[web] a" {
		t.Fatalf("after an error %q", order(m2))
	}
	m.editFor = folderKey("f1")
	m3, _ := m.takeLayout(layoutMsg{layout: Layout{}})
	if order(m3) != "[web] a" {
		t.Fatalf("while renaming %q", order(m3))
	}
	m.editFor = ""
	m4, _ := m.takeLayout(layoutMsg{layout: Layout{}})
	if order(m4) != "a b" {
		t.Fatalf("folders removed in the browser: %q", order(m4))
	}
}
