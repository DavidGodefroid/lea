package launcher

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// lockHostWorld is a host with the separated setup of lockmq and lockfile as their install
// leaves it: root-owned /etc/<tool>/system.toml, the console account and the clients group in
// /etc/passwd and /etc/group, /run/<tool> of the console account and group (mode 2710), and a
// root-owned /usr/local/bin/lockfile of a safe version. The test's own uid stands for root and for
// the console account (it owns everything); the session is uid 4242, gid 4243.
func lockHostWorld(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	// The name the launcher's userName gives this account: id -un, else u<uid>.
	user := "u" + uid
	if me, err := exec.Command("id", "-un").Output(); err == nil {
		user = strings.TrimSpace(string(me))
	}
	mk := func(p string, mode os.FileMode) {
		if err := os.MkdirAll(root+p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root+p, mode); err != nil {
			t.Fatal(err)
		}
	}
	wr := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(root+p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root+p, mode); err != nil {
			t.Fatal(err)
		}
	}
	mk("/usr/local/bin", 0o755)
	var passwd, group string
	for _, n := range []string{"lockmq", "lockfile"} {
		mk("/etc/"+n, 0o755)
		wr("/etc/"+n+"/system.toml", "# "+n+": the console runs as service_user\nservice_user = \""+n+"\"\nclient_group = \""+n+"-clients\" # members reach it\nsocket_dir   = \"/run/"+n+"\"\nx11 = \"refuse\"\nallowed_uids = [10002, 10003]\n", 0o644)
		mk("/run/"+n, 0o2710)
		passwd += n + ":x:" + uid + ":" + uid + "::/nonexistent:/usr/sbin/nologin\n"
		group += n + "-clients:x:" + gid + ":" + user + "\n"
	}
	wr("/etc/passwd", "root:x:0:0::/root:/bin/sh\n"+passwd, 0o644)
	wr("/etc/group", "root:x:0:\n"+group, 0o644)
	wr("/usr/local/bin/lockfile", "#!/bin/sh\necho lockfile v0.2.0\n", 0o755)
	oldRoot, oldOwner, oldSelf, oldGID := lockHostRoot, lockHostOwner, lockHostSelf, lockHostSelfGID
	lockHostRoot, lockHostOwner = root, os.Getuid()
	lockHostSelf, lockHostSelfGID = func() int { return 4242 }, func() int { return 4243 }
	t.Cleanup(func() {
		lockHostRoot, lockHostOwner, lockHostSelf, lockHostSelfGID = oldRoot, oldOwner, oldSelf, oldGID
	})
	return root
}

// imageAccounts answers lea's `docker run --rm … cat IMAGE /etc/passwd /etc/group`.
const imageAccounts = "FAKE_DOCKER_OUT_RUN___RM_AT__ETC_GROUP"

func lockArgs(t *testing.T, w *world) []string {
	t.Helper()
	if _, ok := w.env[imageAccounts]; !ok {
		w.env[imageAccounts] = "root:x:0:0:root:/root:/bin/bash\nlocksql:x:10001:10001::/:/sbin/nologin\nroot:x:0:\nlocksql-clients:x:10000:"
	}
	args, err := w.launcher().runArgs(w.proj, "172.18.0.5")
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// Off by default: no trace of lockmq or lockfile in the container, even with a host setup.
func TestLockHostOffByDefault(t *testing.T) {
	lockHostWorld(t)
	w := newWorld(t, repoRoot(t))
	for _, a := range lockArgs(t, w) {
		if strings.Contains(a, "dst=/etc/lock") && !strings.Contains(a, "locksql") || strings.Contains(a, "dst=/run/lockmq") ||
			strings.Contains(a, "dst=/run/lockfile") || strings.HasPrefix(a, "LEA_LOCKMQ") || strings.HasPrefix(a, "LEA_LOCKFILE") {
			t.Fatalf("lock tool in the run args: %s", a)
		}
	}
}

// On, with a sound host setup: the system file and the socket directory, read-only, the clients
// group and the host ids; nothing else of the tool's account (no home, no config, no audit).
func TestLockHostWired(t *testing.T) {
	root := lockHostWorld(t)
	w := newWorld(t, repoRoot(t))
	writeUserConf(t, w, "LEA_LOCKMQ=1\nLEA_LOCKFILE=1\n")
	args := lockArgs(t, w)
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	for _, n := range []string{"lockmq", "lockfile"} {
		e := "LEA_" + strings.ToUpper(n)
		for _, m := range []string{e + "=1", e + "_UID=" + uid, e + "_GID=" + gid,
			"type=bind,src=" + root + "/etc/" + n + "/system.toml,dst=/etc/" + n + "/system.toml,readonly",
			"type=bind,src=" + root + "/run/" + n + ",dst=/run/" + n + ",readonly"} {
			if !contains(args, m) {
				t.Errorf("missing %q in %v", m, args)
			}
		}
	}
	n := 0
	for i, a := range args {
		if a == "--group-add" && args[i+1] == gid {
			n++
		}
		if strings.Contains(a, "lock") && strings.HasPrefix(a, "type=") && !strings.HasSuffix(a, ",readonly") && !strings.Contains(a, "locksql") {
			t.Errorf("a lock tool mount is writable: %s", a)
		}
		for _, bad := range []string{"/home/lockmq", "/home/lockfile", ".config/lockmq", ".config/lockfile", "/var/lib/lock"} {
			if strings.Contains(a, bad) {
				t.Errorf("the console account's files reach the container: %s", a)
			}
		}
	}
	if n != 2 {
		t.Errorf("--group-add %s: %d times, want 2", gid, n)
	}
	if strings.Contains(w.errb.String(), "warning") {
		t.Errorf("warnings on a sound setup: %s", w.errb.String())
	}
}

// Any doubt on the host setup leaves the tool out, with a warning; lea still starts.
func TestLockHostRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		change func(t *testing.T, root string)
		want   string
	}{
		"no setup": {func(t *testing.T, r string) { must(t, os.RemoveAll(r+"/etc/lockmq")) }, "/etc/lockmq is absent"},
		"system.toml writable by the group": {func(t *testing.T, r string) { must(t, os.Chmod(r+"/etc/lockmq/system.toml", 0o664)) },
			"not owned and writable by root only"},
		"/etc/lockmq writable by others": {func(t *testing.T, r string) { must(t, os.Chmod(r+"/etc/lockmq", 0o757)) }, "not owned and writable by root only"},
		"system.toml a symlink": {func(t *testing.T, r string) {
			must(t, os.Rename(r+"/etc/lockmq/system.toml", r+"/etc/lockmq/real.toml"))
			must(t, os.Symlink("real.toml", r+"/etc/lockmq/system.toml"))
		}, "not owned and writable by root only"},
		"another socket dir": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("socket_dir = \"/tmp/x\"\n"), 0o644))
		}, "socket_dir is '/tmp/x'"},
		"another console account": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("service_user = \"me\"\n"), 0o644))
		}, "service_user is 'me'"},
		"no account": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/passwd", []byte("root:x:0:0::/root:/bin/sh\n"), 0o644))
		}, "no lockmq account"},
		"the console account is the session's": {func(t *testing.T, r string) { lockHostSelf = os.Getuid }, "root or yours"},
		"the clients group is the session's":   {func(t *testing.T, r string) { lockHostSelfGID = os.Getgid }, "root or yours"},
		"no socket dir":                        {func(t *testing.T, r string) { must(t, os.Remove(r+"/run/lockmq")) }, "/run/lockmq is absent"},
		"socket dir open to others":            {func(t *testing.T, r string) { must(t, os.Chmod(r+"/run/lockmq", 0o2711)) }, "closed to others"},
		"socket dir writable by the group":     {func(t *testing.T, r string) { must(t, os.Chmod(r+"/run/lockmq", 0o2770)) }, "not writable by the group"},
		"a table in system.toml": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("[x]\nsocket_dir = \"/run/lockmq\"\n"), 0o644))
		}, "line 1: not a key"},
		"a quoted key in system.toml": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("\"socket_dir\" = \"/tmp/x\"\n"), 0o644))
		}, "line 1: not a key"},
		"an escape in system.toml": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("service_user = \"lock\\u006dmq\"\n"), 0o644))
		}, "line 1: not a key"},
		"a multi-line string in system.toml": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("socket_dir = \"\"\"\n/tmp/x\"\"\"\n"), 0o644))
		}, "line 1: not a key"},
		"a key set twice": {func(t *testing.T, r string) {
			must(t, os.WriteFile(r+"/etc/lockmq/system.toml", []byte("socket_dir = \"/run/lockmq\"\nsocket_dir = \"/tmp/x\"\n"), 0o644))
		}, "set twice"},
		"socket dir a symlink": {func(t *testing.T, r string) {
			must(t, os.Rename(r+"/run/lockmq", r+"/run/real"))
			must(t, os.Symlink("real", r+"/run/lockmq"))
		}, "closed to others"},
	} {
		t.Run(name, func(t *testing.T) {
			root := lockHostWorld(t)
			c.change(t, root)
			w := newWorld(t, repoRoot(t))
			writeUserConf(t, w, "LEA_LOCKMQ=1\n")
			for _, a := range lockArgs(t, w) {
				if strings.Contains(a, "dst=/etc/lockmq") || strings.Contains(a, "dst=/run/lockmq") || strings.HasPrefix(a, "LEA_LOCKMQ") {
					t.Fatalf("lockmq wired despite %s: %s", name, a)
				}
			}
			if e := w.errb.String(); !strings.Contains(e, "warning: lockmq: ") || !strings.Contains(e, c.want) {
				t.Fatalf("stderr %q, want %q", e, c.want)
			}
		})
	}
}

// lockfile below v0.1.1 (plugin sandbox escape), of unknown version, or not a root-owned file in
// /usr/local/bin is never wired.
func TestLockHostLockfileVersion(t *testing.T) {
	for name, bin := range map[string]string{
		"v0.1.0":         "#!/bin/sh\necho lockfile v0.1.0\n",
		"0.0.9":          "#!/bin/sh\necho lockfile 0.0.9\n",
		"unknown":        "#!/bin/sh\nexit 1\n",
		"symlink":        "",
		"group-writable": "#!/bin/sh\necho lockfile v0.2.0\n",
		"dir writable":   "#!/bin/sh\necho lockfile v0.2.0\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := lockHostWorld(t)
			b := root + "/usr/local/bin/lockfile"
			if bin == "" {
				must(t, os.Rename(b, b+".real"))
				must(t, os.Symlink(b+".real", b))
			} else {
				must(t, os.WriteFile(b, []byte(bin), 0o755))
			}
			switch name {
			case "group-writable":
				must(t, os.Chmod(b, 0o775))
			case "dir writable":
				must(t, os.Chmod(root+"/usr/local/bin", 0o777))
			}
			w := newWorld(t, repoRoot(t))
			writeUserConf(t, w, "LEA_LOCKFILE=1\n")
			for _, a := range lockArgs(t, w) {
				if strings.Contains(a, "dst=/run/lockfile") || strings.HasPrefix(a, "LEA_LOCKFILE") {
					t.Fatalf("lockfile %s wired: %s", name, a)
				}
			}
			if !strings.Contains(w.errb.String(), "warning: lockfile: ") {
				t.Fatalf("stderr %q", w.errb.String())
			}
		})
	}
}

func TestLockfileSafe(t *testing.T) {
	for v, want := range map[string]bool{"v0.1.0": false, "v0.0.9": false, "0.1.0": false, "v0.1.1": true, "0.1.1": true,
		"v0.2.0": true, "v1.0.0": true, "v0.10.0": true, "": false, "v0.2": false, "latest": false} {
		if got := lockfileSafe(v); got != want {
			t.Errorf("lockfileSafe(%q) = %v", v, got)
		}
	}
}

// The switches are 0 or 1, the pins releases, and lockfile's pin never below v0.1.1.
func TestLockHostConfig(t *testing.T) {
	for conf, want := range map[string]string{
		"LEA_LOCKMQ=yes\n":          "LEA_LOCKMQ must be 1",
		"LEA_LOCKFILE=2\n":          "LEA_LOCKFILE must be 1",
		"LEA_LOCKMQ_REF=latest\n":   "LEA_LOCKMQ_REF must be a lockmq release",
		"LEA_LOCKFILE_REF=main\n":   "LEA_LOCKFILE_REF must be a lockfile release",
		"LEA_LOCKFILE_REF=v0.1.0\n": "sandbox",
	} {
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, conf)
		_, err := LoadConfig(w.getenv, w.root, w.proj)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", conf, err, want)
		}
	}
	w := newWorld(t, repoRoot(t))
	cfg, err := LoadConfig(w.getenv, w.root, w.proj)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LockHost("LEA_LOCKMQ") || cfg.LockHost("LEA_LOCKFILE") {
		t.Fatal("lockmq and lockfile must be off by default")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The console account and the clients group are named in lea's image with the host ids: an id
// the image already uses (or locksql's) leaves the tool out, never half-wired; so does an image
// whose accounts cannot be read.
func TestLockHostImageIDs(t *testing.T) {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	for name, c := range map[string]struct{ accounts, want string }{
		"uid taken":  {"root:x:0:0:root:/root:/bin/bash\nsvc:x:" + uid + ":1::/:/bin/sh\nroot:x:0:", "uid " + uid + " of the lockmq account is already used in lea's image"},
		"gid taken":  {"root:x:0:0:root:/root:/bin/bash\nroot:x:0:\nsystemd-journal:x:" + gid + ":", "gid " + gid + " of the lockmq-clients group is already used in lea's image"},
		"unreadable": {"", "cannot read the accounts of lea's image"},
	} {
		t.Run(name, func(t *testing.T) {
			lockHostWorld(t)
			w := newWorld(t, repoRoot(t))
			w.env[imageAccounts] = c.accounts
			writeUserConf(t, w, "LEA_LOCKMQ=1\n")
			args := lockArgs(t, w)
			for i, a := range args {
				if a == "LEA_LOCKMQ=1" || a == "--group-add" && args[i+1] == gid {
					t.Fatalf("lockmq wired: %v", args)
				}
			}
			if !strings.Contains(w.errb.String(), c.want) {
				t.Fatalf("stderr %q, want %q", w.errb.String(), c.want)
			}
		})
	}
	t.Run("locksql's ids", func(t *testing.T) {
		root := lockHostWorld(t)
		must(t, os.WriteFile(root+"/etc/group", []byte("lockmq-clients:x:10000:\n"), 0o644))
		w := newWorld(t, repoRoot(t))
		writeUserConf(t, w, "LEA_LOCKMQ=1\n")
		if contains(lockArgs(t, w), "LEA_LOCKMQ=1") {
			t.Fatal("lockmq wired on gid 10000")
		}
		if !strings.Contains(w.errb.String(), "is locksql's in lea's image") {
			t.Fatalf("stderr %q", w.errb.String())
		}
	})
}

// The image's accounts are read once per image id, then from the host dir.
func TestLockHostImageIDsCached(t *testing.T) {
	lockHostWorld(t)
	w := newWorld(t, repoRoot(t))
	id := strings.Repeat("ab", 32)
	w.env["FAKE_DOCKER_OUT_IMAGE_INSPECT"] = "sha256:" + id
	writeUserConf(t, w, "LEA_LOCKMQ=1\n")
	for i := 0; i < 2; i++ {
		if !contains(lockArgs(t, w), "LEA_LOCKMQ=1") {
			t.Fatalf("run %d: lockmq not wired: %s", i, w.errb.String())
		}
	}
	n := 0
	for _, c := range w.calls() {
		if strings.Contains(c, "[--entrypoint] [cat]") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the image's accounts read %d times, want 1", n)
	}
	if _, err := os.Stat(w.data + "/lea/host/image-ids/" + id); err != nil {
		t.Fatal(err)
	}
}
