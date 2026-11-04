package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Sounds tell the user without a look at the deck: SoundAttention when an agent starts waiting
// for them (a permission), SoundDone when an agent's turn ends.
const (
	SoundAttention = "attention"
	SoundDone      = "done"
)

// soundGap is the shortest time between two sounds of one kind: agents finishing together ring
// once.
const soundGap = time.Second

// doneDelay is how long an agent must stay quiet after its turn ends before the done sound:
// Claude runs the Stop hooks in parallel, and an agent-type one (a plugin's) shows itself only at
// its first tool call, a second or so after the Stop (#274).
const doneDelay = 3 * time.Second

// doneHookWait holds the done sound while an agent-type Stop hook has not answered: it may think a
// while between two tools, and one killed at its timeout never answers.
const doneHookWait = time.Minute

// An agent-type hook runs as a subagent whose agent_id starts with hookAgentPrefix, and its last
// tool call, hookVerdictTool, carries its verdict (Claude Code).
const (
	hookAgentPrefix = "hook-agent-"
	hookVerdictTool = "StructuredOutput"
)

// doneTimer is a pending done sound: a *time.Timer, or a test's.
type doneTimer interface{ Stop() bool }

func afterFunc(d time.Duration, f func()) doneTimer { return time.AfterFunc(d, f) }

// Sounder plays a sound of a kind; it must not block.
type Sounder func(kind string)

// SetSounder turns sounds on (nil: off, the default).
func (d *Daemon) SetSounder(s Sounder) {
	d.mu.Lock()
	d.sounder = s
	d.mu.Unlock()
}

// SetSoundOn is the user's switch (on at start): off, the sounder stays aside and nothing rings.
func (d *Daemon) SetSoundOn(on bool) {
	d.mu.Lock()
	d.soundOff = !on
	d.mu.Unlock()
}

// soundState is what the user's switch shows: "on", "off", or "" when no sounder plays at all.
func (d *Daemon) soundState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case d.sounder == nil:
		return ""
	case d.soundOff:
		return "off"
	}
	return "on"
}

// switchSound is the sound method (host only): with on, it flips the switch and tells the decks;
// either way it answers the state.
func (d *Daemon) switchSound(params json.RawMessage) (any, error) {
	var p struct{ On *bool }
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
	}
	if p.On != nil {
		d.SetSoundOn(*p.On)
		d.changed()
	}
	return map[string]string{"sound": d.soundState()}, nil
}

// sound plays a sound of a kind for an agent of the container, only while a deck shows the
// container: the daemon and the agents outlive the deck, and nobody listens then.
func (d *Daemon) sound(ctx context.Context, container, kind string) {
	d.mu.Lock()
	on := d.sounder != nil && !d.soundOff
	d.mu.Unlock()
	if !on || !d.deckAttached(ctx, container) {
		return
	}
	d.mu.Lock()
	s := d.sounder
	now := time.Now()
	if s == nil || d.soundOff || now.Sub(d.rang[kind]) < soundGap {
		d.mu.Unlock()
		return
	}
	d.rang[kind] = now
	d.mu.Unlock()
	s(kind)
}

// armDone plays the done sound of an agent after doneDelay (doneHookWait while a Stop hook has not
// answered), unless cancelDone comes first; arming again restarts the wait. container is the
// agent's: its deck must still show it then.
func (d *Daemon) armDone(key, container string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armDoneLocked(key, container)
}

// restartDoneLocked restarts the wait of a pending done sound; none pending, it does nothing.
func (d *Daemon) restartDoneLocked(key, container string) {
	if d.doneTimers[key] != nil {
		d.armDoneLocked(key, container)
	}
}

func (d *Daemon) armDoneLocked(key, container string) {
	if t := d.doneTimers[key]; t != nil {
		t.Stop()
	}
	delay := d.doneDelay
	if len(d.stopHooks[key]) > 0 {
		delay = doneHookWait
	}
	var t doneTimer
	t = d.doneAfter(delay, func() {
		d.mu.Lock()
		// A timer stopped too late still runs: only the agent's current one rings.
		mine := d.doneTimers[key] == t
		if mine {
			delete(d.doneTimers, key)
		}
		d.mu.Unlock()
		if mine {
			d.sound(context.Background(), container, SoundDone)
		}
	})
	d.doneTimers[key] = t
}

// cancelDone drops the pending done sound of an agent: its turn goes on, or it is gone.
func (d *Daemon) cancelDone(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelDoneLocked(key)
}

func (d *Daemon) cancelDoneLocked(key string) {
	if t := d.doneTimers[key]; t != nil {
		t.Stop()
		delete(d.doneTimers, key)
	}
}

// toolAfterStopLocked sorts a tool event that comes after the agent's Stop by who runs the tool
// (d.mu held), and tells whether the agent itself works again:
//   - an agent-type Stop hook: it is pending until its verdict, and the done sound waits for it;
//   - the agent itself (an empty id): a Stop hook blocked the stop. The done sound goes, the
//     agent is no longer stopped, and its next Stop arms another;
//   - another subagent (a background task): nothing changes;
//   - no id at all (an older lea hook): the wait starts over, as it did before #274.
func (d *Daemon) toolAfterStopLocked(key, container, event string, agentID *string, tool string) bool {
	switch {
	case agentID == nil:
	case strings.HasPrefix(*agentID, hookAgentPrefix):
		hooks := d.stopHooks[key]
		if event == "PostToolUse" && tool == hookVerdictTool {
			delete(hooks, *agentID)
		} else if len(hooks) < maxSubagents {
			if hooks == nil {
				hooks = map[string]bool{}
				d.stopHooks[key] = hooks
			}
			hooks[*agentID] = true
		}
	case *agentID != "":
		return false
	default:
		d.cancelDoneLocked(key)
		delete(d.stopped, key)
		delete(d.stopHooks, key)
		return true
	}
	// Cancelling would leave a stop the hook lets through without its sound: no stop_check follows.
	d.restartDoneLocked(key, container)
	return false
}

// HostSounder plays files on the host with the first player found (pw-play, paplay,
// canberra-gtk-play); files maps a kind to a sound file. nil when no player is installed.
func HostSounder(files map[string]string) Sounder {
	var player []string
	for _, p := range [][]string{{"pw-play"}, {"paplay"}, {"canberra-gtk-play", "-f"}} {
		if bin, err := exec.LookPath(p[0]); err == nil {
			player = append([]string{bin}, p[1:]...)
			break
		}
	}
	if player == nil {
		return nil
	}
	var mu sync.Mutex
	return func(kind string) {
		f := files[kind]
		if f == "" {
			return
		}
		if _, err := os.Stat(f); err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		c := exec.Command(player[0], append(player[1:], f)...)
		if c.Start() == nil {
			go c.Wait()
		}
	}
}

// BellSounder rings the terminal bell of each terminal ttys lists (lea-ctl, #165: no sound player
// runs there; the user's terminal sounds its bell). Every kind of sound is the bell.
func BellSounder(ttys func() []string) Sounder {
	return func(kind string) {
		for _, tty := range ttys() {
			f, err := os.OpenFile(tty, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				continue
			}
			f.Write([]byte("\a"))
			f.Close()
		}
	}
}
