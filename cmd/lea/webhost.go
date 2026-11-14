package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/DavidGodefroid/lea/internal/grants"
	"github.com/DavidGodefroid/lea/internal/launcher"
	"github.com/DavidGodefroid/lea/internal/web"
)

// webHost is lea web's part on the host (#415): lea allow with the pins read from the projects
// directory's origins, as the core's lea allow reads them, and lea egress through the launcher's
// own code. One call at a time: each writes the user configuration or reads the origins anew.
type webHost struct {
	container, proj string
	mu              sync.Mutex
}

var _ web.Host = (*webHost)(nil)

// allowWords are the words of the lea allow command a asks for, checked as the CLI checks them.
func allowWords(a web.AllowAsk) (allowRequest, error) {
	words := append([]string{}, a.Capabilities...)
	for _, r := range a.Repos {
		words = append(words, "--repo", r)
	}
	for _, f := range [][2]string{{"--branch", a.Branch}, {"--agent", a.Agent}, {"--tag", a.Tag}, {"--for", a.For}} {
		if f[1] != "" {
			words = append(words, f[0], f[1])
		}
	}
	r, ok := parseAllow(words)
	if !ok || len(r.names) != len(a.Capabilities) {
		return r, errors.New("not an allowance: capabilities, then --repo, --branch, --agent, --tag and --for (a duration such as 720h)")
	}
	caps, err := grants.Expand(r.names)
	if err != nil {
		return r, err
	}
	r.caps = caps
	return r, nil
}

// shellWord is w as the user would type it: a glob in single quotes.
func shellWord(w string) string {
	if strings.ContainsAny(w, "*?[ ") {
		return "'" + w + "'"
	}
	return w
}

// plan reads what r would record: the pins of its repositories, those the user has not seen yet
// (none recorded, or another one), and their digest.
func (h *webHost) plan(r allowRequest) (web.AllowPreview, []forgeRepo, error) {
	var out web.AllowPreview
	cmd := []string{"lea", "allow"}
	for _, w := range r.words {
		cmd = append(cmd, shellWord(w))
	}
	out.Command = strings.Join(cmd, " ")
	forge := false
	for _, c := range r.caps {
		forge = forge || c.Forge()
	}
	var cur allowView
	if err := hostCall("allow_list", map[string]any{"container": h.container}, &cur); err != nil {
		return out, nil, err
	}
	for _, pin := range cur.Pins {
		if gl, ok := strings.CutPrefix(pin, "gitlab:"); ok {
			gl, _, _ = strings.Cut(gl, "/")
			r.gitlab[gl] = true
		}
	}
	var repos []forgeRepo
	switch {
	case forge && h.proj == "":
		return out, nil, errors.New("lea web does not know the projects directory: run lea web from it, through the launcher")
	case forge:
		var err error
		if repos, out.Skipped, err = readRepos(h.proj, r); err != nil {
			return out, nil, err
		}
		if len(repos) == 0 {
			return out, nil, fmt.Errorf("no repository of %s has an origin on a forge", oneLine(h.proj, 200))
		}
	case len(r.repos) > 0:
		return out, nil, errors.New(r.caps[0].Name + " covers agents, not a repository: name no repository")
	}
	out.Repos = []web.AllowRepo{}
	for _, x := range repos {
		ar := web.AllowRepo{Repo: x.Repo, Pin: x.Pin}
		switch was, ok := cur.Pins[x.Repo]; {
		case !ok:
			ar.New, out.Unseen = true, true
		case was != x.Pin:
			ar.Was, out.Unseen = was, true
		}
		out.Repos = append(out.Repos, ar)
	}
	if out.Skipped == nil {
		out.Skipped = []string{}
	}
	out.Digest = pinsDigest(repos)
	return out, repos, nil
}

func (h *webHost) AllowPreview(ctx context.Context, a web.AllowAsk) (web.AllowPreview, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, err := allowWords(a)
	if err != nil {
		return web.AllowPreview{}, err
	}
	p, _, err := h.plan(r)
	return p, cleanErr(err)
}

// AllowRecord records a, if its pins still read as the preview the user saw (lea allow --yes).
func (h *webHost) AllowRecord(ctx context.Context, a web.AllowAsk, digest string) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, err := allowWords(a)
	if err != nil {
		return nil, err
	}
	p, repos, err := h.plan(r)
	if err != nil {
		return nil, cleanErr(err)
	}
	if p.Digest != digest {
		return nil, errors.New("nothing recorded: the pins changed since you saw them; check them again")
	}
	params := map[string]any{"container": h.container, "capabilities": r.names, "repos": repos, "all": len(r.repos) == 0,
		"branch": r.branch, "agent": r.agent, "tag": r.tag, "ttl_s": int64(r.ttl.Seconds())}
	if repos == nil {
		params["repos"] = []forgeRepo{}
	}
	var res json.RawMessage
	if err := hostCall("allow_set", params, &res); err != nil {
		return nil, cleanErr(err)
	}
	return res, nil
}

// launcherDo runs f on the launcher of this checkout for the working directory, its configuration
// read anew; it returns what the launcher reported.
func (h *webHost) launcherDo(f func(l *launcher.Launcher) error) (string, error) {
	var out, errb bytes.Buffer
	l, err := newLauncher(nil, &out, &errb)
	if err != nil {
		return "", cleanErr(err)
	}
	err = f(l)
	said := strings.TrimSpace(strings.ReplaceAll(out.String()+errb.String(), "lea: ", ""))
	if err != nil {
		msg := clean(launcher.DieMessage(err), false)
		if msg == "" || strings.HasPrefix(msg, "exit status") {
			msg = said
		}
		return said, errors.New(msg)
	}
	return said, nil
}

func (h *webHost) EgressRefused(ctx context.Context) (json.RawMessage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var rs []launcher.EgressHost
	_, err := h.launcherDo(func(l *launcher.Launcher) (err error) {
		rs, err = l.EgressRefused("24h")
		return err
	})
	if err != nil {
		return nil, err
	}
	if rs == nil {
		rs = []launcher.EgressHost{}
	}
	return json.Marshal(rs)
}

func (h *webHost) EgressChange(ctx context.Context, allow bool, host string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.launcherDo(func(l *launcher.Launcher) error { return l.EgressChange(allow, []string{host}) })
}

// cleanErr is err with its text cleaned for the page (no control characters).
func cleanErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(clean(err.Error(), false))
}
