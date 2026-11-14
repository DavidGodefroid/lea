package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func (ts *testServer) answer(method string, v any) {
	ts.d.mu.Lock()
	ts.d.answers[method] = v
	ts.d.mu.Unlock()
}

// Spawn and grant requests are answered only when the daemon lists them for this lea, with the
// verdicts lea approve and lea deny send.
func TestRequestsAreDecidedOnlyForThisLea(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	ts.answer("request_list", []map[string]any{{"id": 1, "container": "lea-a"}, {"id": 2, "container": "lea-b"}})
	ts.answer("grant_request_list", []map[string]any{{"id": 5, "container": "lea-a"}, {"id": 6, "container": "lea-b"}})
	refused := []struct {
		m    string
		body map[string]any
	}{
		{"request_decide", map[string]any{"id": 2, "verdict": "accept"}},
		{"request_decide", map[string]any{"id": 9, "verdict": "accept"}},
		{"request_decide", map[string]any{"id": 1, "verdict": "approve"}},
		{"grant_request_decide", map[string]any{"id": 6, "verdict": "approve"}},
		{"grant_request_decide", map[string]any{"id": 5, "verdict": "accept"}},
		{"grant_request_decide", map[string]any{"id": "5", "verdict": "approve"}},
	}
	for _, r := range refused {
		if code, _ := ts.post(t, r.m, r.body, c, nil); code != http.StatusBadRequest {
			t.Fatalf("%s %v: %d", r.m, r.body, code)
		}
	}
	for _, m := range ts.d.methods() {
		if strings.HasSuffix(m, "_decide") {
			t.Fatalf("the daemon got %s", m)
		}
	}
	if code, _ := ts.post(t, "request_decide", map[string]any{"id": 1, "verdict": "decline", "reason": "not now"}, c, nil); code != 200 {
		t.Fatalf("decline: %d", code)
	}
	if p := ts.d.last("request_decide"); p["id"] != float64(1) || p["verdict"] != "decline" || p["reason"] != "not now" {
		t.Fatalf("request_decide %v", p)
	}
	if code, _ := ts.post(t, "grant_request_decide", map[string]any{"id": 5, "verdict": "approve", "ttl_s": 1}, c, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if p := ts.d.last("grant_request_decide"); len(p) != 3 || p["id"] != float64(5) || p["verdict"] != "approve" {
		t.Fatalf("grant_request_decide %v", p)
	}
}

// Only the open question of one of this lea's agents is answered.
func TestQuestionAnswerOnlyTheOpenOneOfThisLea(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	ts.answer("question_list", []map[string]any{{"id": 1, "agent": "lea-a/claude-1", "state": "open"},
		{"id": 2, "agent": "lea-a/claude-1", "state": "queued"}, {"id": 3, "agent": "lea-b/claude-1", "state": "open"}})
	for _, b := range []map[string]any{{"id": 2, "text": "x"}, {"id": 3, "text": "x"}, {"id": 1, "text": " "}} {
		if code, _ := ts.post(t, "question_answer", b, c, nil); code != 400 {
			t.Fatalf("%v: %d", b, code)
		}
	}
	if code, _ := ts.post(t, "question_answer", map[string]any{"id": 1, "text": "yes"}, c, nil); code != 200 {
		t.Fatalf("answer: %d", code)
	}
	if p := ts.d.last("question_answer"); len(p) != 2 || p["id"] != float64(1) || p["text"] != "yes" {
		t.Fatalf("question_answer %v", p)
	}
}

// The allowances are this lea's whatever the page says; the capabilities come from grants.
func TestAllowancesAreThisLeas(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	ts.answer("allow_list", map[string]any{"sets": []any{}})
	code, out := ts.post(t, "allow_list", map[string]any{"container": "lea-b"}, c, nil)
	if code != 200 || ts.d.last("allow_list")["container"] != "lea-a" {
		t.Fatalf("allow_list: %d %v", code, ts.d.last("allow_list"))
	}
	res := out["result"].(map[string]any)
	if caps := res["capabilities"].([]any); len(caps) == 0 || caps[0].(map[string]any)["name"] != "push" {
		t.Fatalf("capabilities %v", res["capabilities"])
	}
	if code, _ := ts.post(t, "allow_off", map[string]any{"container": "lea-b"}, c, nil); code != 400 {
		t.Fatalf("allow_off of everything: %d", code)
	}
	if code, _ := ts.post(t, "allow_off", map[string]any{"set": 4, "container": "lea-b"}, c, nil); code != 200 {
		t.Fatalf("allow_off: %d", code)
	}
	if p := ts.d.last("allow_off"); p["container"] != "lea-a" || p["set"] != float64(4) {
		t.Fatalf("allow_off %v", p)
	}
}

// fakeHost records the host calls.
type fakeHost struct {
	asks    []AllowAsk
	digests []string
	changes []string
	err     error
}

func (h *fakeHost) AllowPreview(ctx context.Context, a AllowAsk) (AllowPreview, error) {
	h.asks = append(h.asks, a)
	return AllowPreview{Command: "lea allow " + strings.Join(a.Capabilities, " "), Digest: "d1",
		Repos: []AllowRepo{{Repo: "lea", Pin: "github.com/o/lea", New: true}}, Unseen: true}, h.err
}

func (h *fakeHost) AllowRecord(ctx context.Context, a AllowAsk, digest string) (json.RawMessage, error) {
	h.asks = append(h.asks, a)
	h.digests = append(h.digests, digest)
	return json.RawMessage(`{"set":{"ID":3}}`), h.err
}

func (h *fakeHost) EgressRefused(ctx context.Context) (json.RawMessage, error) {
	return json.RawMessage(`[{"host":"deb.debian.org","count":2}]`), h.err
}

func (h *fakeHost) EgressChange(ctx context.Context, allow bool, host string) (string, error) {
	verb := "deny "
	if allow {
		verb = "allow "
	}
	h.changes = append(h.changes, verb+host)
	return "applied live", h.err
}

// lea allow and lea egress go to the host, never to the daemon; without one they say so.
func TestAllowAndEgressGoThroughTheHost(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	for _, m := range []string{"allow_preview", "allow_add", "egress_refused", "egress_allow"} {
		code, out := ts.post(t, m, map[string]any{"capabilities": []string{"merge"}, "digest": "d1", "host": "x.example"}, c, nil)
		if code != 400 || !strings.Contains(out["error"].(string), "without the launcher") {
			t.Fatalf("%s without a host: %d %v", m, code, out)
		}
	}
	h := &fakeHost{}
	ts.s.SetHost(h)
	if code, _ := ts.post(t, "allow_preview", map[string]any{"capabilities": []string{"teleport"}}, c, nil); code != 400 {
		t.Fatalf("unknown capability: %d", code)
	}
	code, out := ts.post(t, "allow_preview", map[string]any{"capabilities": []string{"pr", "release"}, "repos": []string{"lea"}, "tag": "v0.15.*"}, c, nil)
	if code != 200 || out["result"].(map[string]any)["digest"] != "d1" || len(h.asks) != 1 || h.asks[0].Tag != "v0.15.*" {
		t.Fatalf("preview: %d %v %+v", code, out, h.asks)
	}
	if code, _ := ts.post(t, "allow_add", map[string]any{"capabilities": []string{"pr"}}, c, nil); code != 400 {
		t.Fatalf("add without a digest: %d", code)
	}
	if code, _ := ts.post(t, "allow_add", map[string]any{"capabilities": []string{"pr"}, "digest": "d1"}, c, nil); code != 200 || h.digests[0] != "d1" {
		t.Fatalf("add: %d %v", code, h.digests)
	}
	ts.post(t, "egress_allow", map[string]any{"host": "deb.debian.org"}, c, nil)
	ts.post(t, "egress_deny", map[string]any{"host": "deb.debian.org"}, c, nil)
	if strings.Join(h.changes, ",") != "allow deb.debian.org,deny deb.debian.org" {
		t.Fatalf("changes %v", h.changes)
	}
	if code, out := ts.post(t, "egress_refused", map[string]any{}, c, nil); code != 200 || len(out["result"].([]any)) != 1 {
		t.Fatalf("refused: %d %v", code, out)
	}
	h.err = errors.New("deb.debian.org is not in the egress allowlist")
	if code, out := ts.post(t, "egress_deny", map[string]any{"host": "deb.debian.org"}, c, nil); code != 400 || out["error"] != h.err.Error() {
		t.Fatalf("host refusal: %d %v", code, out)
	}
	for _, m := range ts.d.methods() {
		if m != "allow_list" && strings.HasPrefix(m, "allow") || strings.HasPrefix(m, "egress") {
			t.Fatalf("the daemon got %s", m)
		}
	}
}

// The stream carries this lea's spawn and grant requests, the grant requests grouped into one
// lea allow advice built by grants.Advise.
func TestTheEventStreamCarriesRequestsAndTheirAdvice(t *testing.T) {
	ts := newTestServer(t)
	c := ts.login(t)
	ts.answer("request_list", []map[string]any{{"id": 1, "container": "lea-a", "role": "dev"}, {"id": 2, "container": "lea-b"}})
	ts.answer("grant_request_list", []map[string]any{
		{"id": 5, "container": "lea-a", "action": "pr-create", "repo": "lea"},
		{"id": 6, "container": "lea-a", "action": "release-create", "repo": "lea", "tag": "v0.15.*"},
		{"id": 7, "container": "lea-a", "action": "close-issue", "repo": "lea"},
		{"id": 8, "container": "lea-b", "action": "merge", "repo": "x"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.url+"/api/events", nil)
	req.AddCookie(c)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, data := readEvent(t, bufio.NewReader(resp.Body))
	var st State
	if err := json.Unmarshal([]byte(data), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Requests) != 1 || len(st.GrantRequests) != 3 || st.GrantRequests[0].Capability != "pr" || st.GrantRequests[2].Capability != "issues" {
		t.Fatalf("requests %s / %+v: %s", st.Requests, st.GrantRequests, data)
	}
	a := st.GrantAdvice
	if a == nil || a.Command != "lea allow pr issues release --repo lea --tag 'v0.15.*'" || !strings.Contains(a.OneOff, "g5 g6 g7") ||
		!strings.Contains(a.NotCovered, "push, merge, delete-branch, release-tag") {
		t.Fatalf("advice %+v", a)
	}
}
