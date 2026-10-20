package launcher

import (
	"os"
	"strings"
	"testing"
	"time"
)

// releaseWorld is a world whose launcher runs from a release install (no .git) of version v0.1.3,
// at a fixed time; its core logs its calls.
func releaseWorld(t *testing.T) (*world, *Launcher, time.Time) {
	t.Helper()
	w := newWorld(t, repoRoot(t))
	// one write per call: the detached updater logs concurrently with the launcher's fakes
	os.WriteFile(w.core, []byte(`#!/bin/sh
l=core; for a in "$@"; do l="$l [$a]"; done; printf '%s\n' "$l" >>"$FAKE_LOG"
exit 0
`), 0o755)
	l := w.launcher()
	l.Root, l.Version = w.dir+"/lea-install/v0.1.3", "v0.1.3"
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	l.Now = func() time.Time { return now }
	return w, l, now
}

// waitCall waits for the detached updater's call to reach the log.
func waitCall(w *world, want string) bool {
	for i := 0; i < 100; i++ {
		if strings.Contains(strings.Join(w.calls(), "\n"), want) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestAutoUpdateStartsTheBackgroundUpdaterAndStampsTheCheck(t *testing.T) {
	w, l, now := releaseWorld(t)
	l.autoUpdate()
	if !waitCall(w, "core [update] [--background]") {
		t.Fatalf("no background update; calls:\n%s", strings.Join(w.calls(), "\n"))
	}
	fi, err := os.Stat(w.hostDir() + "/update-check")
	if err != nil || !fi.ModTime().Equal(now) {
		t.Fatalf("update-check: %v %v", fi, err)
	}
}

func TestAutoUpdateChecksAtMostOncePer24h(t *testing.T) {
	w, l, now := releaseWorld(t)
	stamp := w.hostDir() + "/update-check"
	os.WriteFile(stamp, nil, 0o600)
	os.Chtimes(stamp, now.Add(-23*time.Hour), now.Add(-23*time.Hour))
	l.autoUpdate()
	time.Sleep(200 * time.Millisecond)
	if got := strings.Join(w.calls(), "\n"); strings.Contains(got, "[update]") {
		t.Fatalf("checked again within 24h:\n%s", got)
	}
	// 25 h later: checked again
	os.Chtimes(stamp, now.Add(-25*time.Hour), now.Add(-25*time.Hour))
	l.autoUpdate()
	if !waitCall(w, "core [update] [--background]") {
		t.Fatal("no check after 24h")
	}
}

// A stamp in the future (a clock set back) does not silence the check for ever.
func TestAutoUpdateIgnoresAStampInTheFuture(t *testing.T) {
	w, l, now := releaseWorld(t)
	stamp := w.hostDir() + "/update-check"
	os.WriteFile(stamp, nil, 0o600)
	os.Chtimes(stamp, now.Add(48*time.Hour), now.Add(48*time.Hour))
	l.autoUpdate()
	if !waitCall(w, "core [update] [--background]") {
		t.Fatal("a future stamp blocked the check")
	}
}

func TestAutoUpdateIsOffForLEA_AUTOUPDATE0ACheckoutAndDev(t *testing.T) {
	cases := map[string]func(w *world, l *Launcher){
		"LEA_AUTOUPDATE=0": func(w *world, l *Launcher) { l.Cfg.Export("LEA_AUTOUPDATE", "0") },
		"checkout":         func(w *world, l *Launcher) { os.MkdirAll(l.Root+"/.git", 0o755) },
		"dev":              func(w *world, l *Launcher) { l.Version = "dev" },
		"no version":       func(w *world, l *Launcher) { l.Version = "" },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			w, l, _ := releaseWorld(t)
			set(w, l)
			l.autoUpdate()
			time.Sleep(200 * time.Millisecond)
			if got := strings.Join(w.calls(), "\n"); strings.Contains(got, "[update]") || exists(w.hostDir()+"/update-check") {
				t.Fatalf("checked:\n%s", got)
			}
		})
	}
}

// The launch never waits for the updater, and a core that cannot start is silent.
func TestAutoUpdateNeverBlocksNorFailsTheLaunch(t *testing.T) {
	w, l, _ := releaseWorld(t)
	os.WriteFile(w.core, []byte("#!/bin/sh\nsleep 5\n"), 0o755)
	start := time.Now()
	l.autoUpdate()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the launch waited %v for the updater", d)
	}
	_, l2, _ := releaseWorld(t)
	l2.Core = w.dir + "/no-such-core"
	l2.autoUpdate() // no panic, no output
	if w.errb.Len() != 0 {
		t.Fatalf("stderr: %q", w.errb.String())
	}
}

// The plain lea command checks before the session; other commands do not.
func TestDispatchChecksForUpdatesOnThePlainCommandOnly(t *testing.T) {
	w, l, _ := releaseWorld(t)
	l.Main([]string{"stop"})
	time.Sleep(200 * time.Millisecond)
	if got := strings.Join(w.calls(), "\n"); strings.Contains(got, "[update]") {
		t.Fatalf("lea stop checked for updates:\n%s", got)
	}
	l.Main(nil)
	if !waitCall(w, "core [update] [--background]") {
		t.Fatalf("lea did not check; calls:\n%s", strings.Join(w.calls(), "\n"))
	}
}
