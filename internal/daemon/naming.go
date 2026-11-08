package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DavidGodefroid/lea/internal/agents"
	"github.com/DavidGodefroid/lea/internal/store"
	"github.com/DavidGodefroid/lea/internal/wire"
)

// An agent the user has not labelled is named after its first prompt: the hook sends the prompt
// (name_hint), a short claude run on the host turns it into a few words, and they become the
// agent's label, as if the user had typed them in the deck. The prompt is never stored. One try
// per agent and daemon; a label the user sets meanwhile wins. An agent lea starts with a brief is
// named after the brief instead (#356): its first prompt is lea's own.

// MaxNamePrompt is the part of the first prompt the namer reads, in bytes.
const MaxNamePrompt = 2000

const nameTimeout = 60 * time.Second

// Namer turns an agent's first prompt into a short name.
type Namer func(ctx context.Context, prompt string) (string, error)

// SetNamer turns auto-naming on (nil: off, the default).
func (d *Daemon) SetNamer(n Namer) {
	d.mu.Lock()
	d.namer = n
	d.mu.Unlock()
}

func (d *Daemon) dispatchNameHint(o Origin, me string, req wire.Request) (any, error) {
	if o.Container == "" {
		return nil, fmt.Errorf("%s: agents only", req.Method)
	}
	var p struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, err
	}
	if len(p.Prompt) > MaxNamePrompt {
		return nil, fmt.Errorf("prompt is %d bytes, limit %d", len(p.Prompt), MaxNamePrompt)
	}
	if a, err := d.st.AgentByKey(me); err == nil && leasPrompt(a, p.Prompt) {
		// lea's own first prompt says nothing of the task (#356): an agent started with a brief
		// is named after the brief (nameFromBrief), one started by hand after its user's prompt.
		return struct{}{}, nil
	}
	d.nameFrom(me, p.Prompt)
	return struct{}{}, nil
}

// leasPrompt reports whether prompt is the first prompt lea started a with (the hook may cut it),
// not words of its user.
func leasPrompt(a store.Agent, prompt string) bool {
	return a.Prompt != "" && strings.HasPrefix(a.Prompt, prompt) || strings.HasPrefix(prompt, "You are the lea agent ")
}

// nameFromBrief names an agent lea just started with a brief after the brief, unless it has a
// label (#356): the brief says what the agent does; its first prompt is lea's boilerplate. The
// key may be one an ended agent had: it is tried afresh.
func (d *Daemon) nameFromBrief(key, brief string) {
	if len(brief) > MaxNamePrompt {
		brief = strings.ToValidUTF8(brief[:MaxNamePrompt], "")
	}
	d.mu.Lock()
	delete(d.named, key)
	d.mu.Unlock()
	d.nameFrom(key, brief)
}

// nameFrom starts naming me after prompt, unless naming is off, me was already tried or has a
// label. It does not wait for the namer.
func (d *Daemon) nameFrom(me, prompt string) {
	if strings.TrimSpace(prompt) == "" {
		return
	}
	d.mu.Lock()
	n, life := d.namer, d.life
	if n == nil || d.named[me] {
		d.mu.Unlock()
		return
	}
	d.named[me] = true
	d.mu.Unlock()
	if a, err := d.st.AgentByKey(me); err != nil || a.Label != "" {
		return
	}
	if life == nil {
		life = context.Background()
	}
	after := d.afterName
	go func() {
		if after != nil {
			defer after()
		}
		ctx, cancel := context.WithTimeout(life, nameTimeout)
		defer cancel()
		out, err := n(ctx, prompt)
		if err != nil {
			log.Printf("lea daemon: name %s: %v", me, err)
			return
		}
		// Never a reserved label: the prompt is whatever the agent's hook sent (#356).
		name, err := agents.CheckAgentLabel(autoName(out))
		if err != nil || name == "" {
			return
		}
		d.mu.Lock()
		ag := d.agents
		d.mu.Unlock()
		// A label set meanwhile (the user's) stays; a clash with a live agent's label gets a
		// number (#356).
		if ag != nil {
			_, err = ag.AutoLabel(ctx, me, name)
		} else if _, err = d.st.SetLabelUnique(me, name, agents.MaxLabel, true); err == nil {
			d.changed()
		}
		if err != nil {
			log.Printf("lea daemon: name %s: %v", me, err)
		}
	}()
}

// autoName is the namer's first line, without quotes, end punctuation or control characters,
// its spaces collapsed, cut to agents.MaxLabel characters at a word boundary when it can be.
func autoName(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			s = l
			break
		}
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'`«»“”‘’.,;:!*# ")
	if utf8.RuneCountInString(s) <= agents.MaxLabel {
		return s
	}
	r := []rune(s)[:agents.MaxLabel]
	if i := strings.LastIndexByte(string(r), ' '); i > 0 {
		return strings.TrimSpace(string(r)[:i])
	}
	return string(r)
}

// namePrompt asks for the name; the user's prompt comes on stdin.
const namePrompt = "You name coding-agent sessions. Reply with only a 2-4 word name (at most 24 characters, " +
	"lowercase, words separated by spaces, in the language of the request) that sums up the task " +
	"in the user's message. When the message is about an issue of a repository (lea#350, or #350 in the " +
	"repository lea), start with it as repo#number, then 2-3 words: lea#350 spawn reuse. " +
	"No quotes, no other punctuation, nothing else."

// ClaudeNamer runs `claude -p` with a small model on the host, outside any project and without
// the user's settings, hooks, plugins or MCP servers, and without tools.
func ClaudeNamer(bin, model string) Namer {
	return func(ctx context.Context, prompt string) (string, error) {
		c := exec.CommandContext(ctx, bin, "-p", "--model", model, "--setting-sources", "", "--strict-mcp-config",
			"--tools", "", "--no-session-persistence", "--system-prompt", namePrompt)
		c.Dir = os.TempDir()
		c.Stdin = strings.NewReader(prompt)
		out, err := c.Output()
		if err != nil {
			return "", fmt.Errorf("%s -p: %w", bin, err)
		}
		return string(out), nil
	}
}
