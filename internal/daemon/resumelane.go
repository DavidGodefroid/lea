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

// A suspended agent's resume that fails is tried again by its next wake (a hub sync, every
// hubSyncEvery, while it has an undelivered decision; a new message; a daemon start), but not
// before its backoff: resumeBackoff seconds, doubling at each failure. An agent woken only by
// messages is tried again by the next message or daemon start, not by the hub sync. After
// maxSuspendedResumes failures lea gives up: the agent ends (done), its decisions go to its
// inbox and its lead is told. One resume of an agent runs at a time: a wake while it runs does
// nothing, so one outage counts once. The count lives in the daemon: a restarted daemon starts it
// again.
var (
	resumeBackoff       int64 = 30
	maxSuspendedResumes       = 5
)

// resumeFail is one agent's failed resumes and the time (unix seconds) the next may start.
type resumeFail struct {
	n    int
	next int64
}

// resumeHeldBack reports whether agent's last resume failed and its backoff has not gone by.
func (d *Daemon) resumeHeldBack(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.resumeFails[key]
	return ok && d.now() < f.next
}

// resumeFailed counts a failed resume of agent and arms its backoff; true once lea gives up.
func (d *Daemon) resumeFailed(key string) (giveUp bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.resumeFails[key]
	f.n++
	if f.n >= maxSuspendedResumes {
		delete(d.resumeFails, key)
		return true
	}
	f.next = d.now() + resumeBackoff<<min(f.n-1, 8)
	d.resumeFails[key] = f
	return false
}

// giveUpResume ends a suspended agent whose resumes kept failing: it is done (only if it is still
// suspended_cold), its undelivered decisions are posted to its inbox (once), and its lead is told.
func (d *Daemon) giveUpResume(key string, tries int) {
	ok, err := d.st.EndSuspended(key, d.now())
	if err != nil || !ok {
		if err != nil {
			log.Printf("lea daemon: give up resuming %s: %v", key, err)
		}
		return
	}
	log.Printf("lea daemon: gave up resuming %s after %d tries: its decisions go to its inbox", key, tries)
	if err := d.deliverHubDecisions(); err != nil {
		log.Printf("lea daemon: give up resuming %s: %v", key, err)
	}
	if ag, err := d.st.AgentByKey(key); err == nil && ag.Parent != "" {
		d.post("user", ag.Parent, "note", fmt.Sprintf("lea could not resume %s after %d tries: it is ended, the decisions on its tickets are in its inbox.", ag.Name, tries), 0)
	}
	d.changed()
}

// resumeSuspended resumes a suspended_cold agent with what it has now as its next turn: the
// decisions on its tickets (delivered once the resume started) and the unread notice. One that
// woke or ended meanwhile is left alone. A resume that fails leaves the decisions undelivered:
// the next wake past its backoff tries again, and lea gives up after maxSuspendedResumes tries
// (done, the inbox, its lead told).
func (d *Daemon) resumeSuspended(a Agents, key string) {
	d.mu.Lock()
	if d.resuming[key] {
		d.mu.Unlock()
		return // its resume runs: this wake is that one's
	}
	d.resuming[key] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.resuming, key)
		d.mu.Unlock()
	}()
	ag, err := d.st.AgentByKey(key)
	if err != nil || ag.State != store.StateSuspendedCold {
		d.mu.Lock()
		delete(d.resumeFails, key) // no longer cold: a later suspension starts afresh
		d.mu.Unlock()
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
		if errors.Is(err, agents.ErrNotSuspended) {
			d.mu.Lock()
			delete(d.resumeFails, key)
			d.mu.Unlock()
		} else {
			log.Printf("lea daemon: resume suspended %s: %v", key, err)
			if d.resumeFailed(key) {
				d.giveUpResume(key, maxSuspendedResumes)
			}
		}
	} else {
		d.mu.Lock()
		delete(d.resumeFails, key)
		d.mu.Unlock()
		d.markDelivered(key, ws)
	}
	d.changed()
}

// resumeStopped is the launcher's resume of a container's stopped agents: at once when the
// pressure lets it, else queued in the resume lane, with the notice saying so. The lane lives in
// the daemon: a queued launcher resume is lost when the daemon restarts, and only the launcher's
// next start of that container (lea deck-open --fresh, cmd/lea/host.go) asks for it again, not
// every lea. A suspended agent's queued resume is not lost: the restarted daemon wakes it again
// (suspendedSince).
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
