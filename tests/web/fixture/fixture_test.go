package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/web"
)

// TestTheBrowserFlowOverHTTP runs the browser test's scenario (tests/web/e2e.mjs) without a
// browser: lea's real daemon on the fixture's agents, the real lea web server, and an HTTP
// client that does what the page does.
func TestTheBrowserFlowOverHTTP(t *testing.T) {
	dir, err := os.MkdirTemp("", "lwf")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := filepath.Join(dir, "d.sock")
	bus, err := start(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	s, err := web.New(web.SocketCaller(sock), container)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(ctx, l)
	base := "http://" + l.Addr().String()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	resp, err := hc.Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.String() != base+"/" || resp.StatusCode != 200 {
		t.Fatalf("sign-in landed on %s (%d)", resp.Request.URL, resp.StatusCode)
	}
	post := func(method string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+"/api/"+method, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// The stream: its states, as the page reads them.
	sctx, stopStream := context.WithCancel(ctx)
	req, _ := http.NewRequestWithContext(sctx, "GET", base+"/api/events", nil)
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", resp, err)
	}
	states := make(chan web.State, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok && strings.Contains(d, `"snapshot"`) {
				var st web.State
				json.Unmarshal([]byte(d), &st)
				states <- st
			}
		}
	}()
	// until waits (2 s at most) for a state that ok accepts.
	until := func(what string, ok func(web.State, []map[string]any) bool) web.State {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case st := <-states:
				var snap struct{ Agents []map[string]any }
				json.Unmarshal(st.Snapshot, &snap)
				if ok(st, snap.Agents) {
					return st
				}
			case <-deadline:
				t.Fatalf("no state within 2 s: %s", what)
			}
		}
	}
	agent := func(as []map[string]any, name string) map[string]any {
		for _, a := range as {
			if a["name"] == name {
				return a
			}
		}
		return nil
	}
	until("the first state", func(web.State, []map[string]any) bool { return true })

	// An agent appears, then its task.
	if err := call(sock, "", "agent_new", map[string]any{"container": container, "name": "dev-1", "role": "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	until("dev-1 on the board", func(_ web.State, as []map[string]any) bool { return agent(as, "dev-1") != nil })
	if err := call(bus, "dev-1", "task_update", map[string]any{"status_text": "running the tests", "next": "CI"}, nil); err != nil {
		t.Fatal(err)
	}
	until("dev-1's task", func(st web.State, _ []map[string]any) bool {
		for _, raw := range st.Tasks {
			var it map[string]any
			json.Unmarshal(raw, &it)
			if it["agent"] == "dev-1" && it["status"] == "running the tests" && it["next"] == "CI" {
				return true
			}
		}
		return false
	})

	// A note, read in its inbox and in its thread.
	if code, out := post("msg_send", map[string]any{"to": container + "/dev-1", "body": "hello from the browser", "kind": "note"}); code != 200 {
		t.Fatalf("msg_send: %d %v", code, out)
	}
	var inbox any
	if err := call(bus, "dev-1", "msg_inbox", nil, &inbox); err != nil || !strings.Contains(mustJSON(inbox), "hello from the browser") {
		t.Fatalf("inbox %v %v", inbox, err)
	}
	if code, out := post("thread_list", map[string]any{"key": container + "/dev-1"}); code != 200 || !strings.Contains(mustJSON(out), "hello from the browser") {
		t.Fatalf("thread_list: %d %v", code, out)
	}

	// The spawn form, a control tower, rename, pause, hold.
	if code, out := post("agent_new", map[string]any{"role": "reviewer", "label": "web-review", "brief": "review the web UI"}); code != 200 {
		t.Fatalf("agent_new: %d %v", code, out)
	}
	until("web-review", func(_ web.State, as []map[string]any) bool {
		for _, a := range as {
			if a["label"] == "web-review" && a["role"] == "reviewer" {
				return true
			}
		}
		return false
	})
	if code, out := post("agent_new", map[string]any{"tower": true}); code != 200 {
		t.Fatalf("tower: %d %v", code, out)
	}
	until("the tower", func(_ web.State, as []map[string]any) bool {
		for _, a := range as {
			if a["label"] == "Control Tower" && a["role"] == "lead" {
				return true
			}
		}
		return false
	})
	for m, p := range map[string]map[string]any{
		"agent_label": {"key": container + "/dev-1", "label": "builder"},
		"agent_pause": {"key": container + "/dev-1", "paused": true},
		"agent_hold":  {"key": container + "/dev-1", "held": true},
	} {
		if code, out := post(m, p); code != 200 {
			t.Fatalf("%s: %d %v", m, code, out)
		}
	}
	until("dev-1 renamed, paused, held", func(_ web.State, as []map[string]any) bool {
		a := agent(as, "dev-1")
		return a != nil && a["label"] == "builder" && a["paused"] == true && a["held"] == true
	})

	// A permission waits for the browser; Allow answers it.
	verdict := make(chan map[string]any, 1)
	go func() {
		var v map[string]any
		call(bus, "dev-1", "approval_request", map[string]any{"tool": "Bash", "input": map[string]any{"command": "make test"}, "timeout_s": 30, "always": true}, &v)
		verdict <- v
	}()
	st := until("the permission", func(st web.State, _ []map[string]any) bool { return len(st.Approvals) == 1 })
	var p struct{ ID int64 }
	json.Unmarshal(st.Approvals[0], &p)
	if code, out := post("approval_decide", map[string]any{"id": p.ID, "verdict": "allow"}); code != 200 {
		t.Fatalf("approval_decide: %d %v", code, out)
	}
	select {
	case v := <-verdict:
		if v["verdict"] != "allow" {
			t.Fatalf("verdict %v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the permission was not answered")
	}
	if code, _ := post("agent_stop", map[string]any{"key": container + "/dev-1"}); code != 200 {
		t.Fatalf("agent_stop: %d", code)
	}
	until("dev-1 stopped", func(_ web.State, as []map[string]any) bool {
		a := agent(as, "dev-1")
		return a != nil && a["state"] == "done"
	})

	// The browser gone: the next permission is not held.
	stopStream()
	resp.Body.Close()
	time.Sleep(300 * time.Millisecond)
	began := time.Now()
	var v map[string]any
	if err := call(bus, "dev-2", "approval_request", map[string]any{"tool": "Bash", "input": map[string]any{}, "timeout_s": 30}, &v); err != nil {
		t.Fatal(err)
	}
	if v["id"] != float64(0) || time.Since(began) > 2*time.Second {
		t.Fatalf("after the browser left: %v after %s", v, time.Since(began))
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
