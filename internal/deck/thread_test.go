package deck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type fakeThread struct {
	mu    sync.Mutex
	msgs  []Message
	sent  []string
	err   error
	label string
}

func (f *fakeThread) Thread(ctx context.Context, key string) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.msgs, f.err
}

func (f *fakeThread) Send(ctx context.Context, key, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, key+": "+body)
	return f.err
}

func newThread(api *fakeThread, compose bool) ThreadView {
	return NewThread(api, "lea-a/claude-1", compose, false, func() time.Time { return clock })
}

func tstep(t *testing.T, v ThreadView, msg tea.Msg) (ThreadView, tea.Cmd) {
	t.Helper()
	nv, cmd := v.Update(msg)
	return nv.(ThreadView), cmd
}

func typeText(t *testing.T, v ThreadView, s string) ThreadView {
	t.Helper()
	for _, r := range s {
		v, _ = tstep(t, v, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return v
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func msgFrom(id int64, from, to, body string) Message {
	return Message{ID: id, From: from, To: to, Kind: "note", Body: body, At: clock.Unix()}
}

func TestThreadLoadsThenPolls(t *testing.T) {
	api := &fakeThread{msgs: []Message{msgFrom(1, "user", "lea-a/claude-1", "hi")}}
	v := newThread(api, false)
	loaded, ok := v.Init()().(threadLoaded)
	if !ok || len(loaded.msgs) != 1 {
		t.Fatalf("init=%#v", loaded)
	}
	v, cmd := tstep(t, v, loaded)
	if len(v.msgs) != 1 || cmd == nil {
		t.Fatal("no poll scheduled after a load")
	}
	if _, cmd = tstep(t, v, threadPoll{}); cmd == nil {
		t.Fatal("a poll does not load")
	}
	if _, ok := cmd().(threadLoaded); !ok {
		t.Fatal("a poll does not load the thread")
	}
}

func TestComposeSendsAndAOneShotPopupCloses(t *testing.T) {
	api := &fakeThread{}
	v := newThread(api, true)
	v = typeText(t, v, "hi")
	v, _ = tstep(t, v, tea.KeyMsg{Type: tea.KeyCtrlJ})
	v, _ = tstep(t, v, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	v = typeText(t, v, "there")
	v, cmd := tstep(t, v, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not send")
	}
	sent := cmd()
	if len(api.sent) != 1 || api.sent[0] != "lea-a/claude-1: hi\n there" {
		t.Fatalf("sent=%q", api.sent)
	}
	if _, cmd = tstep(t, v, sent); !quits(cmd) {
		t.Fatal("a popup opened with m stays open after sending")
	}
}

func TestSendErrorKeepsTheDraft(t *testing.T) {
	api := &fakeThread{err: errors.New("rate limit: 10 messages in the last minute (limit 10)")}
	v := typeText(t, newThread(api, true), "hi")
	v, cmd := tstep(t, v, tea.KeyMsg{Type: tea.KeyEnter})
	v, _ = tstep(t, v, cmd())
	if !strings.Contains(v.status, "rate limit") || string(v.draft) != "hi" || !v.composing {
		t.Fatalf("status %q draft %q composing %v", v.status, string(v.draft), v.composing)
	}
}

func TestEscClosesAnEmptyOneShotAndLeavesComposeOtherwise(t *testing.T) {
	if _, cmd := tstep(t, newThread(&fakeThread{}, true), tea.KeyMsg{Type: tea.KeyEsc}); !quits(cmd) {
		t.Fatal("esc on an empty m popup does not close it")
	}
	v, _ := tstep(t, newThread(&fakeThread{}, false), key("m"))
	if !v.composing {
		t.Fatal("m does not start a message")
	}
	v = typeText(t, v, "draft")
	v, cmd := tstep(t, v, tea.KeyMsg{Type: tea.KeyEsc})
	if v.composing || cmd != nil {
		t.Fatal("esc while writing closed the popup")
	}
	if _, cmd = tstep(t, v, key("q")); !quits(cmd) {
		t.Fatal("q does not close the thread")
	}
}

func TestPasteKeepsNewlinesAndDropsControls(t *testing.T) {
	v := newThread(&fakeThread{}, true)
	v, _ = tstep(t, v, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\rb\x1bc\u202ed\te"), Paste: true})
	if string(v.draft) != "a\nbcd\te" {
		t.Fatalf("draft %q", string(v.draft))
	}
}

func TestThreadViewCleansAndFits(t *testing.T) {
	api := &fakeThread{}
	v := newThread(api, false)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 60, Height: 20})
	v, _ = tstep(t, v, threadLoaded{msgs: []Message{
		msgFrom(1, "lea-a/claude-1", "user", "a\x1b[2Jb\u202ec\n"+strings.Repeat("x", 300)),
		msgFrom(2, "user", "lea-a/claude-1", "日本語"+strings.Repeat("語", 80)),
		msgFrom(3, "lea-a/claude-1", "user", strings.Repeat("☺\ufe0f", 80)),
		msgFrom(4, "lea-a/claude-1", "user", strings.Repeat("👩\u200d💻", 80)+"\n"+strings.Repeat("e\u0301", 200)),
	}})
	raw := v.View()
	if strings.Contains(raw, "\u202e") || strings.Contains(sgr.ReplaceAllString(raw, ""), "\x1b") {
		t.Fatalf("control characters reach the terminal: %q", raw)
	}
	ls := strings.Split(sgr.ReplaceAllString(raw, ""), "\n")
	var seen strings.Builder
	for off := 0; off < 400; off += 5 { // every wrapped line, not only the last screen
		v.offset = off
		screen := sgr.ReplaceAllString(v.View(), "")
		seen.WriteString(screen + "\n")
		for i, l := range strings.Split(screen, "\n") {
			if lipgloss.Width(l) > 60 {
				t.Fatalf("offset %d line %d is %d wide: %q", off, i, lipgloss.Width(l), l)
			}
		}
	}
	if len(ls) != 20 {
		t.Fatalf("%d lines", len(ls))
	}
	for i, l := range ls {
		if lipgloss.Width(l) > 60 {
			t.Fatalf("line %d is %d wide: %q", i, lipgloss.Width(l), l)
		}
	}
	s := seen.String()
	if !strings.Contains(s, "#1 claude-1 › you") || !strings.Contains(s, "#2 you › claude-1") {
		t.Fatalf("headers:\n%s", s)
	}
}

func TestScrollKeepsTheNewestAtTheBottom(t *testing.T) {
	var ms []Message
	for i := 1; i <= 30; i++ {
		ms = append(ms, msgFrom(int64(i), "lea-a/claude-1", "user", fmt.Sprintf("body %d", i)))
	}
	v := newThread(&fakeThread{}, false)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 60, Height: 10})
	v, _ = tstep(t, v, threadLoaded{msgs: ms})
	view := func() string { return sgr.ReplaceAllString(v.View(), "") }
	if !strings.Contains(view(), "body 30") || strings.Contains(view(), "body 1\n") {
		t.Fatalf("not at the newest:\n%s", view())
	}
	for i := 0; i < 200; i++ {
		v, _ = tstep(t, v, key("k"))
	}
	if !strings.Contains(view(), "#1 ") {
		t.Fatalf("scrolled to the top:\n%s", view())
	}
	v, _ = tstep(t, v, key("G"))
	if !strings.Contains(view(), "body 30") {
		t.Fatalf("G:\n%s", view())
	}
}

func TestALongDraftRendersFast(t *testing.T) {
	v := newThread(&fakeThread{}, true)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 80, Height: 24})
	v, _ = tstep(t, v, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("☺\ufe0fx", 10000)), Paste: true})
	if len(v.draft) != 30000 {
		t.Fatalf("draft %d runes", len(v.draft))
	}
	began := time.Now()
	out := v.View()
	if d := time.Since(began); d > 50*time.Millisecond {
		t.Fatalf("View took %v", d)
	}
	for i, l := range strings.Split(sgr.ReplaceAllString(out, ""), "\n") {
		if lipgloss.Width(l) > 80 {
			t.Fatalf("line %d is %d wide", i, lipgloss.Width(l))
		}
	}
}

func TestWrappedBodiesAreCached(t *testing.T) {
	var ms []Message
	for i := 1; i <= 200; i++ {
		ms = append(ms, msgFrom(int64(i), "lea-a/claude-1", "user", strings.Repeat("word ", 3000)))
	}
	v := newThread(&fakeThread{}, false)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 80, Height: 24})
	v, _ = tstep(t, v, threadLoaded{msgs: ms})
	v.View()
	began := time.Now()
	for i := 0; i < 20; i++ {
		v, _ = tstep(t, v, key("k"))
		v.View()
	}
	if d := time.Since(began); d > 200*time.Millisecond {
		t.Fatalf("20 scroll steps took %v: the bodies are wrapped again on every key", d)
	}
}

func (f *fakeThread) Label(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.label, nil
}

func TestThreadTitleShowsTheLabelAndTheName(t *testing.T) {
	api := &fakeThread{label: "auth work"}
	v := newThread(api, false)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 60, Height: 10})
	v, _ = tstep(t, v, v.Init()())
	if title := strings.Split(sgr.ReplaceAllString(v.View(), ""), "\n")[0]; title != "messages · auth work (claude-1)" {
		t.Fatalf("title %q", title)
	}
	api.label = ""
	v, _ = tstep(t, v, v.load()())
	if title := strings.Split(sgr.ReplaceAllString(v.View(), ""), "\n")[0]; title != "messages · claude-1" {
		t.Fatalf("title %q", title)
	}
}

// m opens the popup to write one message: its title says so, its footer says where it goes.
func TestTheOneShotPopupSaysItWritesToTheInbox(t *testing.T) {
	v := newThread(&fakeThread{}, true)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 80, Height: 10})
	ls := strings.Split(sgr.ReplaceAllString(v.View(), ""), "\n")
	if ls[0] != "message to · claude-1" || !strings.Contains(ls[len(ls)-1], "enter send to its inbox") {
		t.Fatalf("view:\n%s", strings.Join(ls, "\n"))
	}
}

// An answer names the message it answers: an ask's question and its answer read as a pair (#28).
func TestAnAnswerNamesTheMessageItAnswers(t *testing.T) {
	q := msgFrom(12, "lea-a/dev", "lea-a/ops", "port?")
	q.Kind = "question"
	a := msgFrom(13, "lea-a/ops", "lea-a/dev", "[status: ok] 8080")
	a.Kind, a.ReplyTo = "result", 12
	api := &fakeThread{msgs: []Message{q, a}}
	v := newThread(api, false)
	v, _ = tstep(t, v, tea.WindowSizeMsg{Width: 80, Height: 12})
	v, _ = tstep(t, v, v.Init()())
	s := sgr.ReplaceAllString(v.View(), "")
	for _, w := range []string{"#12 dev › ops · question ·", "#13 ops › dev · result to #12 ·"} {
		if !strings.Contains(s, w) {
			t.Fatalf("view lacks %q:\n%s", w, s)
		}
	}
}
