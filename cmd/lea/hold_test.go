package main

import (
	"strings"
	"testing"

	"github.com/DavidGodefroid/lea/internal/store"
)

// lea hold pauses the only live lead's inbox, or the agent named; --off releases it (#95).
func TestHoldCommand(t *testing.T) {
	api, _, st := startDaemonWithStore(t)
	env := map[string]string{"LEA_DAEMON_SOCK": api}
	tw, err := st.Register(store.NewAgent{Container: "lea-a", Name: "tower", Profile: "claude", Workdir: "/p", Role: "lead"}, store.Caps{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rc, out, e := runEnv(t, env, "hold"); rc != 0 || !strings.Contains(out, "holding the messages to lea-a/tower (0 held)") {
		t.Fatalf("hold: rc=%d out=%q err=%q", rc, out, e)
	}
	if a, _ := st.AgentByKey(tw.Key); a.HoldSince == 0 {
		t.Fatal("not held")
	}
	if rc, out, e := runEnv(t, env, "hold", "--off", "tower"); rc != 0 || !strings.Contains(out, "released the messages to lea-a/tower") {
		t.Fatalf("release: rc=%d out=%q err=%q", rc, out, e)
	}
	if a, _ := st.AgentByKey(tw.Key); a.HoldSince != 0 {
		t.Fatal("still held")
	}
	if rc, _, e := runEnv(t, env, "hold", "nobody"); rc != 1 || e == "" {
		t.Fatalf("unknown agent: rc=%d err=%q", rc, e)
	}
	for _, bad := range [][]string{{"hold", "--on"}, {"hold", "a", "b"}, {"hold", "--off", "--off"}} {
		if rc, _, _ := runEnv(t, env, bad...); rc != 2 {
			t.Fatalf("%v: rc=%d", bad, rc)
		}
	}
}
