package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// Issue #484, end to end: a lea agent (the real Claude Code with lea's hooks) reported a blocker
// with task_update; the user types to it, and its row turns back to working without the agent
// calling task_update again. It then asks for a permission (red again), the user approves it
// with lea approve, and the approved command fails: Claude reports PostToolUseFailure, which
// lea's hooks forward, and the agent works on, then ends its turn idle, its blocker still clear.
func TestClaudeUnblockedAgentWorksAgain(t *testing.T) {
	e := newClaudeE2E(t)
	e.setLimited(false)
	if _, err := e.st.Register(store.NewAgent{Container: "lea-a", Name: "dev", Profile: "claude", Workdir: e.wd, Role: "dev"}, store.Caps{}, 1); err != nil {
		t.Fatal(err)
	}
	e.d.SetAgents(&fakeAgents{unattached: true})
	var mu sync.Mutex
	var notes []string
	e.d.SetNoDeck(func(title, body string) {
		mu.Lock()
		notes = append(notes, title+": "+body)
		mu.Unlock()
	})
	if err := call(t, e.bus, "dev", "task_update", map[string]any{"status_text": "migrating", "blocked_on": "David must approve the migration"}, nil); err != nil {
		t.Fatal(err)
	}
	blocker := func() string {
		tk, err := e.st.TaskOf("lea-a/dev")
		if err != nil {
			t.Fatal(err)
		}
		return tk.BlockedOn
	}
	cmd, _ := json.Marshal(map[string]string{"command": "exit 3", "description": "fail"})
	e.mu.Lock()
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", string(cmd)
	}
	e.mu.Unlock()
	e.env = append(e.env, "LEA_APPROVALS=deck", "LEA_APPROVAL_TIMEOUT=120")
	e.run("--permission-mode", "default", "approved, go on")

	// The prompt is the user's answer: the blocker is gone, the agent asks for its permission.
	e.until("the permission request", 60, func(a store.Agent) bool { return a.State == "blocked" })
	if b := blocker(); b != "" {
		t.Fatalf("after the prompt: blocker=%q", b)
	}
	re := regexp.MustCompile(`lea approve p([0-9]+) \(once\)`)
	var id string
	for end := time.Now().Add(30 * time.Second); id == "" && time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		mu.Lock()
		for _, n := range notes {
			if m := re.FindStringSubmatch(n); m != nil {
				id = m[1]
			}
		}
		mu.Unlock()
	}
	if id == "" {
		t.Fatalf("no notification of the permission request\n%s", e.out.String())
	}
	c := exec.Command(e.lea, "approve", "p"+id)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.dir, "LEA_DAEMON_SOCK=" + e.api}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("lea approve: %v\n%s", err, out)
	}
	for end := time.Now().Add(45 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if ps, _ := e.st.EventPayloads("lea-a/dev", "PostToolUseFailure"); len(ps) > 0 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("no PostToolUseFailure for the failed command\n%s", e.out.String())
		}
	}
	e.until("the turn's end", 45, func(a store.Agent) bool { return a.State == "idle" })
	if b := blocker(); b != "" {
		t.Fatalf("at the turn's end: blocker=%q", b)
	}
}
