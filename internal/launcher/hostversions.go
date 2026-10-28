package launcher

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// dockerEngineMin is the docker-engine line of config/host-minimums ("" when absent).
func (l *Launcher) dockerEngineMin() string {
	f, err := os.Open(l.Root + "/config/host-minimums")
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fs := strings.Fields(s.Text()); len(fs) >= 2 && fs[0] == "docker-engine" {
			return fs[1]
		}
	}
	return ""
}

// checkDockerEngine refuses to start a lea container on a Docker Engine older than
// config/host-minimums (#108), unless LEA_ALLOW_OUTDATED_HOST=1, which only warns. An engine
// whose version is unknown (Podman, a remote API without components) is let through: lea doctor
// reports it.
func (l *Launcher) checkDockerEngine() error {
	min := l.dockerEngineMin()
	if min == "" {
		return nil
	}
	out, _ := l.output(false, "docker", "version", "--format", `{{range .Server.Components}}{{.Name}}={{.Version}}{{"\n"}}{{end}}`)
	engine := ""
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "Engine="); ok {
			engine = strings.TrimSpace(v)
			break
		}
	}
	if engine == "" || versionAtLeast(engine, min) {
		return nil
	}
	msg := "Docker Engine " + engine + " is older than " + min + " (Moby seccomp fixes): update Docker"
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
