package launcher

import (
	"strings"
	"testing"
)

// fakeRuntimes makes the fake docker info list these runtimes (docker info {{json .Runtimes}}).
func fakeRuntimes(w *world, json string) {
	w.env["FAKE_DOCKER_OUT_INFO___FORMAT_AT_"+fakeKey("{{json .Runtimes}}")] = json
}

func TestRuntimeIsValidated(t *testing.T) {
	for _, line := range []string{"LEA_RUNTIME='run sc'", "LEA_RUNTIME=--privileged", "LEA_RUNTIME=-x", "LEA_RUNTIME=a/b",
		"LEA_RUNTIME='runsc;id'", "LEA_RUNTIME=" + strings.Repeat("r", 65)} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, line+"\n")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err == nil || !strings.Contains(err.Error(), "LEA_RUNTIME must be") {
			t.Errorf("%s: err = %v", line, err)
		}
	}
	for _, line := range []string{"LEA_RUNTIME=", "LEA_RUNTIME=runsc", "LEA_RUNTIME=kata", "LEA_RUNTIME=io.containerd.kata.v2", "LEA_RUNTIME=runsc-debug_1"} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, line+"\n")
		if _, err := LoadConfig(w.getenv, w.root, w.proj); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
}

// runtimeOf is the value after --runtime in args, "" without one; n is the number of --runtime.
func runtimeOf(args []string) (rt string, n int) {
	for i, a := range args {
		if a == "--runtime" && i+1 < len(args) {
			rt, n = args[i+1], n+1
		}
		if strings.HasPrefix(a, "--runtime=") {
			n++
		}
	}
	return rt, n
}

func TestRunArgsRuntime(t *testing.T) {
	// Empty (the default): no --runtime, the engine's default runtime, and docker info is not asked.
	w := newWorld(t, repoRoot(t))
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if _, n := runtimeOf(args); n != 0 {
		t.Errorf("--runtime without LEA_RUNTIME: %q", args)
	}
	if w.hasCall("{{json .Runtimes}}") {
		t.Error("the engine's runtimes are read without LEA_RUNTIME")
	}
	// A runtime the engine knows: --runtime, once, before the image.
	for _, rt := range []string{"runsc", "kata"} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, "LEA_RUNTIME="+rt+"\n")
		fakeRuntimes(w, `{"io.containerd.runc.v2":{"path":"runc"},"runc":{"path":"runc"},"runsc":{"path":"/usr/local/bin/runsc","runtimeArgs":["--host-uds=open"]},"kata":{"runtimeType":"io.containerd.kata.v2"}}`)
		args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
		if err != nil {
			t.Fatalf("%s: %v", rt, err)
		}
		if got, n := runtimeOf(args); got != rt || n != 1 {
			t.Errorf("%s: --runtime %q (%d times)", rt, got, n)
		}
		if args[len(args)-1] != "lea:dev" {
			t.Errorf("%s: image last, got %q", rt, args[len(args)-1])
		}
		// The rest of the containment stays: seccomp, the dropped capabilities.
		for _, m := range []string{"seccomp=" + repoRoot(t) + "/image/seccomp.json", "NET_RAW", "MKNOD"} {
			if !contains(args, m) {
				t.Errorf("%s: missing %q", rt, m)
			}
		}
	}
	// A runtime the engine does not know refuses the start, naming the ones it knows.
	w = newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_RUNTIME=runsc\n")
	fakeRuntimes(w, `{"runc":{"path":"runc"},"io.containerd.runc.v2":{}}`)
	_, err = w.launcher().runArgs(w.proj, "172.18.0.5")
	if err == nil || !strings.Contains(err.Error(), "LEA_RUNTIME=runsc: the container engine has no such runtime (it knows io.containerd.runc.v2, runc)") {
		t.Errorf("err = %v", err)
	}
	// An engine that does not list its runtimes (Podman): --runtime is passed, the engine judges.
	w = newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_RUNTIME=runsc\n")
	args, err = w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := runtimeOf(args); got != "runsc" {
		t.Errorf("--runtime %q under an engine without .Runtimes", got)
	}
}

// The proxy is never started on LEA_RUNTIME: only the agent container runs agent code.
func TestProxyKeepsTheDefaultRuntime(t *testing.T) {
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_RUNTIME=runsc\n")
	fakeRuntimes(w, `{"runc":{},"runsc":{}}`)
	if err := w.launcher().ensureProxy(w.proj); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.calls() {
		if strings.HasPrefix(c, "docker [run]") && strings.Contains(c, "[--runtime]") {
			t.Errorf("the proxy gets a runtime: %s", c)
		}
	}
}

func TestRunningContainerOnAnotherRuntimeIsReported(t *testing.T) {
	up := func(conf, runtime string) *world {
		t.Helper()
		w := newWorld(t, repoRoot(t))
		running(w)
		w.env["FAKE_DOCKER_OUT_CONTAINER_INSPECT_AT_"+fakeKey(w.name())] = runtime
		writeUserConf(t, w, conf)
		l := w.launcher()
		u, err := l.up(w.proj)
		if err != nil {
			t.Fatal(err)
		}
		l.unlock(u.sid)
		return w
	}
	w := up("LEA_RUNTIME=runsc\n", "runc")
	if want := "warning: the running " + w.name() + " runs on the runc runtime, not runsc (LEA_RUNTIME): run 'lea stop', then lea, to restart it on runsc"; !strings.Contains(w.errb.String(), want) {
		t.Errorf("stderr %q, want %q", w.errb.String(), want)
	}
	for _, c := range w.calls() {
		if strings.HasPrefix(c, "docker [rm]") || strings.HasPrefix(c, "docker [run]") {
			t.Errorf("the running container is replaced: %s", c)
		}
	}
	if w := up("LEA_RUNTIME=runsc\n", "runsc"); strings.Contains(w.errb.String(), "runtime") {
		t.Errorf("stderr %q", w.errb.String())
	}
	// Empty LEA_RUNTIME: nothing is asked.
	if w := up("", "runsc"); strings.Contains(w.errb.String(), "runtime") || w.hasCall("{{.HostConfig.Runtime}}") {
		t.Errorf("stderr %q", w.errb.String())
	}
}
