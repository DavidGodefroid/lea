package daemon

import (
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

// Sounder plays a sound of a kind; it must not block.
type Sounder func(kind string)

// SetSounder turns sounds on (nil: off, the default).
func (d *Daemon) SetSounder(s Sounder) {
	d.mu.Lock()
	d.sounder = s
	d.mu.Unlock()
}

func (d *Daemon) sound(kind string) {
	d.mu.Lock()
	s := d.sounder
	now := time.Now()
	if s == nil || now.Sub(d.rang[kind]) < soundGap {
		d.mu.Unlock()
		return
	}
	d.rang[kind] = now
	d.mu.Unlock()
	s(kind)
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
