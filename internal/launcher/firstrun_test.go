package launcher

import "testing"

func TestLoxCheckMissing(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["PATH"] = w.dir + "/bin:/usr/bin:/bin"
	err := w.launcher().loxCheck()
	want := "lox not found: lea starts the Lock Family hub with it: brew install davidgodefroid/tap/lox, then run lea again"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckTooOld(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "0.4.0"
	err := w.launcher().loxCheck()
	want := "lox 0.4.0 is older than 0.5.0, which lea needs to start the hub: brew upgrade lox, then run lea again"
	if err == nil || err.Error() != want {
		t.Fatalf("loxCheck() = %v, want %q", err, want)
	}
}

func TestLoxCheckOK(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	if err := w.launcher().loxCheck(); err != nil {
		t.Fatal(err)
	}
	if !w.hasCall("lox [version]") {
		t.Fatalf("calls: %v", w.calls())
	}
}

func TestLoxCheckDevWarns(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	w.env["FAKE_LOX_VERSION"] = "dev"
	if err := w.launcher().loxCheck(); err != nil {
		t.Fatal(err)
	}
	if got := w.errb.String(); got != "lea: warning: lox dev: its version is not checked\n" {
		t.Fatalf("stderr = %q", got)
	}
}
