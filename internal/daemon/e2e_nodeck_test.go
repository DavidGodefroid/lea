package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidGodefroid/lea/internal/store"
)

// Without the deck (LEA_DECK=0, #135), a lea agent (the real Claude Code with lea's hooks) asks
// for a permission no rule grants: no deck is attached, yet the request is not handed back to its
// own dialog. The user gets a desktop notification naming the request, sees it in lea requests,
// answers it with lea approve (this build of lea, on the daemon's host socket), and the agent
// gets the answer: the shell call runs.
func TestClaudeWithoutTheDeckAnApprovalIsAnsweredWithLeaApprove(t *testing.T) {
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
	marker := filepath.Join(e.wd, "approved")
	cmd, _ := json.Marshal(map[string]string{"command": "touch " + marker, "description": "touch"})
	e.mu.Lock()
	e.answer = func(body string) (string, string) {
		if strings.Contains(body, `"tool_result"`) {
			return "", ""
		}
		return "Bash", string(cmd)
	}
	e.mu.Unlock()
	// As the agents' panes run it: lea answers permissions (LEA_APPROVALS=deck), long enough here.
	e.env = append(e.env, "LEA_APPROVALS=deck", "LEA_APPROVAL_TIMEOUT=120")
	// The image's managed settings make default the permission mode (no auto mode).
	e.run("--permission-mode", "default", "touch it")

	re := regexp.MustCompile(`^lea needs you: dev asks to use Bash: lea approve p([0-9]+) or lea deny p[0-9]+ \(lea requests\)$`)
	var id string
	for end := time.Now().Add(60 * time.Second); id == "" && time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		mu.Lock()
		for _, n := range notes {
			if m := re.FindStringSubmatch(n); m != nil {
				id = m[1]
			}
		}
		mu.Unlock()
	}
	if id == "" {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("no notification of the permission request: %q\n%s", notes, e.out.String())
	}
	lea := func(args ...string) string {
		c := exec.Command(e.lea, args...)
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.dir, "LEA_DAEMON_SOCK=" + e.api}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("lea %q: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := lea("requests"); !strings.Contains(out, "p"+id+" lea-a/dev wants Bash: ") || !strings.Contains(out, "touch "+marker) {
		t.Fatalf("lea requests:\n%s", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the call ran before it was approved")
	}
	if out := lea("approve", "p"+id); out != "allowed: p"+id+"\n" {
		t.Fatalf("lea approve: %q", out)
	}
	e.toolResult("the tool result after lea approve", 0)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the approved call did not run (%v)\n%s", err, e.out.String())
	}
	if out := lea("requests"); !strings.Contains(out, "nothing waits for you") {
		t.Fatalf("lea requests after the answer:\n%s", out)
	}
}
