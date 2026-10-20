package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

// doneDelay is how long a turn must stay over before the done sound: Claude runs the Stop hooks
// in parallel, and another one (an agent-type hook of a plugin, say) can work for a while, then
// block the stop.
const doneDelay = 10 * time.Second

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

// armDone plays the done sound of an agent after doneDelay, unless cancelDone comes first;
// arming again restarts the wait. container is the agent's: its deck must still show it then.
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
	var t *time.Timer
	t = time.AfterFunc(d.doneDelay, func() {
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
