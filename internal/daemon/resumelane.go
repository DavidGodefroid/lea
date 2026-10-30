package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
)

// The resume lane of the admission (#162). A suspended agent woken by a decision or a message,
// and the launcher's resume of a container's stopped agents, start at once when the pressure lets
// them, else wait in the resume lane. A resume is never dropped: no TTL, no cap per lead (those
// are the spawns'). It starts before any queued spawn, a suspended wake before the launcher's
// resume, and a spawn does not start in a container where resumes wait.

// queuedResume is a resume waiting for the pressure to fall: a suspended agent's (key), or the
// stopped agents of a container (manual).
type queuedResume struct {
	key       string
	container string
	manual    bool
	at        time.Time
}

// resumesQueuedLocked is how many resumes wait in container. Callers hold d.mu.
func (d *Daemon) resumesQueuedLocked(container string) int {
	n := 0
	for _, r := range d.resumeQueue {
		if r.container == container {
			n++
		}
	}
	return n
}

// queueResumeLocked lets the resume start now (true) when the pressure and the resumes already
// waiting in its container let it, else queues it once. Callers hold d.mu.
func (d *Daemon) queueResumeLocked(r *queuedResume) (now bool) {
	if d.overLocked(r.container) == "" && d.resumesQueuedLocked(r.container) == 0 {
		return true
	}
	if !slices.ContainsFunc(d.resumeQueue, func(q *queuedResume) bool {
		return q.manual == r.manual && q.key == r.key && q.container == r.container
	}) {
		r.at = time.Now()
		d.resumeQueue = append(d.resumeQueue, r)
	}
	return false
}

// takeResumeLocked takes the resume the pressure now lets through, nil when none: the suspended
// wakes first, then the manual resumes, each in the order they came. Callers hold d.mu.
func (d *Daemon) takeResumeLocked() *queuedResume {
	for _, manual := range []bool{false, true} {
		for i, r := range d.resumeQueue {
			if r.manual == manual && d.overLocked(r.container) == "" {
				d.resumeQueue = slices.Delete(d.resumeQueue, i, i+1)
				return r
			}
		}
	}
	return nil
}

// startResume runs a resume the lane lets through.
func (d *Daemon) startResume(a Agents, r *queuedResume) {
	if a == nil {
		return
	}
	if r.manual {
		ctx, cancel := context.WithTimeout(context.Background(), suspendTimeout)
		defer cancel()
		if err := a.ResumeStopped(ctx, r.container); err != nil {
			log.Printf("lea daemon: queued resume of %s: %v", r.container, err)
		}
		d.pruneForgotten()
		d.changed()
		return
	}
	d.resumeSuspended(a, r.key)
}

// resumeSuspended resumes a suspended_cold agent with what it has now as its next turn: the
// decisions on its tickets (delivered once the resume started) and the unread notice. One that
// woke or ended meanwhile is left alone. A resume that fails leaves the decisions undelivered:
// the next hub sync wakes the agent again, or tells its inbox once it is no longer cold.
func (d *Daemon) resumeSuspended(a Agents, key string) {
	ag, err := d.st.AgentByKey(key)
	if err != nil || ag.State != store.StateSuspendedCold {
		return
	}
	n, err := d.unreadNotice(key)
	if err != nil {
		return
	}
	text, _ := n["text"].(string)
	ws := d.decisionsOf(key)
	prompt := resumePrompt(ws, text)
	if prompt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), suspendTimeout)
	defer cancel()
	if err := a.ResumeSuspended(ctx, key, prompt); err != nil {
		if !errors.Is(err, agents.ErrNotSuspended) {
			log.Printf("lea daemon: resume suspended %s: %v", key, err)
		}
	} else {
		d.markDelivered(key, ws)
	}
	d.changed()
}

// resumeStopped is the launcher's resume of a container's stopped agents: at once when the
// pressure lets it, else queued in the resume lane, with the notice saying so.
func (d *Daemon) resumeStopped(ctx context.Context, a Agents, container string) (string, error) {
	d.mu.Lock()
	why := d.overLocked(container)
	now := d.queueResumeLocked(&queuedResume{container: container, manual: true})
	d.mu.Unlock()
	if !now {
		d.changed()
		if why == "" {
			why = "resumes wait before it"
		}
		return fmt.Sprintf("queued: the stopped agents of %s resume once the pressure falls (%s)", container, why), nil
	}
	err := a.ResumeStopped(ctx, container)
	d.pruneForgotten()
	return "", err
}
