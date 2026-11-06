package daemon

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/DavidGodefroid/lea/internal/textsafe"
)

// Without the deck (LEA_DECK=0, #135) nobody watches an agent column: the host CLI answers lea's
// own approvals (lea requests, lea approve, lea deny) and a desktop notification says when one
// waits. Lock requests are not lea's: lox run asks for them, deck or not (#300).

// Notifier shows one desktop notification; it must not block.
type Notifier func(title, body string)

// notifyGap is the shortest time between two notifications about one thing (an agent, or one of
// its approval cards).
const notifyGap = 5 * time.Second

// maxNotice caps a notification's text (agent names and tool names come from the agents).
const maxNotice = 300

// SetNoDeck turns the no-deck mode on: approvals wait for the host CLI instead of a deck, sounds
// stay off, and n (nil: none, the daemon logs nothing more) notifies the user of what waits.
func (d *Daemon) SetNoDeck(n Notifier) {
	d.mu.Lock()
	d.noDeck, d.notifier = true, n
	d.mu.Unlock()
}

func (d *Daemon) noDeckMode() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.noDeck
}

// attention is lea needing the user now (#294): the sound while a deck shows the container, or,
// without the deck, a desktop notification that says what waits and how to answer it. about is
// the agent it concerns (one notification per agent per notifyGap).
func (d *Daemon) attention(ctx context.Context, container, about, text string) {
	if !d.noDeckMode() {
		d.sound(ctx, container, SoundAttention)
		return
	}
	d.desktop(about, text)
}

// waitsDelay is how long, without the deck, an agent that starts waiting (a permission dialog,
// a question in its terminal) waits before the user is told: the approval card that usually
// follows names the request better (a variable: tests shorten it).
var waitsDelay = 2 * time.Second

// waitsForUser is an agent that starts waiting on the user (#294): the sound with the deck;
// without it, a notification, unless an approval card of the agent says it already.
func (d *Daemon) waitsForUser(ctx context.Context, container, key string) {
	if !d.noDeckMode() {
		d.sound(ctx, container, SoundAttention)
		return
	}
	go func() {
		time.Sleep(waitsDelay)
		if as, err := d.st.PendingApprovals(); err == nil {
			for _, a := range as {
				if a.Agent == key {
					return
				}
			}
		}
		if a, err := d.st.AgentByKey(key); err != nil || a.State != "blocked" {
			return
		}
		d.desktop(key, shortName(key)+" waits for you in its terminal: lea attach "+shortName(key))
	}()
}

// desktop shows text without the deck only (the deck shows it otherwise).
func (d *Daemon) desktop(about, text string) {
	d.mu.Lock()
	n := d.notifier
	now := time.Now()
	if !d.noDeck || n == nil || now.Sub(d.notified[about]) < notifyGap {
		d.mu.Unlock()
		return
	}
	d.notified[about] = now
	d.mu.Unlock()
	n("lea needs you", noticeText(text))
}

// noticeText is text as a notification server shows it safely: no control or invisible
// character, none of the markup characters notify-send's servers read, at most maxNotice runes.
func noticeText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == maxNotice {
			b.WriteString("…")
			break
		}
		switch {
		case r == '<':
			r = '‹'
		case r == '>':
			r = '›'
		case r == '&':
			r = '+'
		case textsafe.Unsafe(r):
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// HostNotifier shows notifications on the host's desktop: notify-send (Linux), else osascript
// (macOS). nil when neither is installed.
func HostNotifier() Notifier {
	var argv func(title, body string) []string
	if bin, err := exec.LookPath("notify-send"); err == nil {
		argv = func(title, body string) []string { return []string{bin, "-a", "lea", "--", title, body} }
	} else if bin, err := exec.LookPath("osascript"); err == nil {
		// The texts are arguments, never part of the script.
		argv = func(title, body string) []string {
			return []string{bin, "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)",
				"-e", "end run", title, body}
		}
	} else {
		return nil
	}
	return func(title, body string) {
		a := argv(title, body)
		c := exec.Command(a[0], a[1:]...)
		if c.Start() == nil {
			go c.Wait()
		}
	}
}
