package launcher

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// hostMinimum is the line of config/host-minimums for key ("docker-engine", "podman"), "" when absent.
func (l *Launcher) hostMinimum(key string) string {
	f, err := os.Open(l.Root + "/config/host-minimums")
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fs := strings.Fields(s.Text()); len(fs) >= 2 && fs[0] == key {
			return fs[1]
		}
	}
	return ""
}

// hostEngine is the container engine behind docker: "docker" or "podman" with its version, ""
// when unknown. Docker Engine names an Engine component, Podman's Docker API a "Podman Engine"
// one; the podman-docker shim has no components but says "podman version" to docker --version.
func (l *Launcher) hostEngine() (string, string) {
	out, _ := l.output(false, "docker", "version", "--format", `{{range .Server.Components}}{{.Name}}={{.Version}}{{"\n"}}{{end}}`)
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "Engine="); ok {
			return "docker", strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "Podman Engine="); ok {
			return "podman", strings.TrimSpace(v)
		}
	}
	out, _ = l.output(false, "docker", "--version")
	if v, ok := strings.CutPrefix(strings.TrimSpace(out), "podman version "); ok {
		return "podman", strings.TrimSpace(v)
	}
	return "", ""
}

// checkDockerEngine refuses to start a lea container on a Docker Engine older than
// config/host-minimums (#108), unless LEA_ALLOW_OUTDATED_HOST=1, which only warns. Podman below
// its minimum only warns: lea is built for Docker Engine, and the Podman minimum is no seccomp
// fix. An engine whose version is unknown is let through: lea doctor reports it.
func (l *Launcher) checkDockerEngine() error {
	engine, v := l.hostEngine()
	if engine == "podman" {
		if min := l.hostMinimum("podman"); min != "" && !versionAtLeast(v, min) {
			l.logf("warning: Podman %s is older than %s (CVE-2026-57231): update Podman", v, min)
		}
		return nil
	}
	min := l.hostMinimum("docker-engine")
	if engine == "" || min == "" || versionAtLeast(v, min) {
		return nil
	}
	msg := "Docker Engine " + v + " is older than " + min + " (Moby seccomp fixes): update Docker"
	if l.Cfg.Get("LEA_ALLOW_OUTDATED_HOST") == "1" {
		l.logf("warning: %s", msg)
		return nil
	}
	return die("%s, or set LEA_ALLOW_OUTDATED_HOST=1 to start anyway", msg)
}

// versionAtLeast is lea_version_ge: a is b or later, comparing the digit runs field by field
// (v2.1.4 is 2.1.4, 7.0.0-38-generic is 7.0.0.38).
func versionAtLeast(a, b string) bool {
	x, y := versionFields(a), versionFields(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p > q
		}
	}
	return true
}

func versionFields(v string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' }) {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}
