package launcher

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// hostMinimum is the line of config/host-minimums for key ("podman"), "" when absent.
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

// checkHostEngine warns when the engine is Podman older than config/host-minimums (#108). No
// engine is refused: lea runs its own seccomp profile (image/seccomp.json), so no Docker Engine
// release fixes something lea relies on; lea doctor shows the versions.
func (l *Launcher) checkHostEngine() {
	engine, v := l.hostEngine()
	if engine != "podman" {
		return
	}
	if min := l.hostMinimum("podman"); min != "" && !versionAtLeast(v, min) {
		l.logf("warning: Podman %s is older than %s (CVE-2026-57231): update Podman", v, min)
	}
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
