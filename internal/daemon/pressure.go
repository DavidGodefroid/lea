package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pressure (#132, P2): a dozen agents running test suites and pods at once took a 22-core host to
// a load of 58 and close to OOM. The daemon samples the host and each lea container every
// pressureEvery: PSI (cpu, memory, io), the memory in use against the limit, the load. Above a
// threshold it refuses (or queues) new agents and gives fewer heavy slots.

// pressureEvery is how often the daemon samples; a sample older than pressureStale is ignored.
const (
	pressureEvery = 5 * time.Second
	pressureStale = 6 * pressureEvery
)

// Reading is one sample of a scope: the host, or one lea container's cgroup. -1: unknown.
type Reading struct {
	// CPUPSI is cpu "some" avg10, MemPSI memory "full" avg10, IOPSI io "full" avg10, in percent.
	CPUPSI float64 `json:"cpu_psi"`
	MemPSI float64 `json:"mem_psi"`
	IOPSI  float64 `json:"io_psi"`
	// MemUsed is the memory in use, in bytes, without the page cache the kernel can drop at
	// once; MemMax the limit (the host: its memory), 0 when there is none.
	MemUsed int64 `json:"mem_used"`
	MemMax  int64 `json:"mem_max"`
	// CPUs is the scope's CPUs (the container's cpu.max, 0: no limit), CPUUsed the CPUs it used
	// since the last sample.
	CPUs    float64 `json:"cpus"`
	CPUUsed float64 `json:"cpu_used"`
	// Load1 is the host's 1-minute load average (host scope only).
	Load1 float64 `json:"load1"`
}

func unknownReading() Reading {
	return Reading{CPUPSI: -1, MemPSI: -1, IOPSI: -1, MemUsed: -1, CPUUsed: -1, Load1: -1}
}

// MemPct is the memory in use, in percent of the limit; -1 without both.
func (r Reading) MemPct() float64 {
	if r.MemUsed < 0 || r.MemMax <= 0 {
		return -1
	}
	return float64(r.MemUsed) * 100 / float64(r.MemMax)
}

// Thresholds are the admission limits (LEA_ADMIT_*), each 0 when off. MemPct is a percent of the
// container's memory limit; the host's memory is checked at hostMemPct at least, where the
// container has no limit of its own the host's is what counts. Load is per host CPU.
type Thresholds struct {
	CPUPSI float64 `json:"cpu_psi"`
	MemPct float64 `json:"mem_pct"`
	MemPSI float64 `json:"mem_psi"`
	IOPSI  float64 `json:"io_psi"`
	Load   float64 `json:"load"`
}

// DefaultThresholds are the admission limits when none is set.
var DefaultThresholds = Thresholds{CPUPSI: 40, MemPct: 85, MemPSI: 10, IOPSI: 40, Load: 2}

const hostMemPct = 90

// over names each threshold r is above, as "cpu PSI 63% > 40%"; host says which scope r is.
func (t Thresholds) over(r Reading, host bool) []string {
	var out []string
	if t.CPUPSI > 0 && r.CPUPSI >= t.CPUPSI {
		out = append(out, fmt.Sprintf("cpu PSI %.0f%% > %.0f%%", r.CPUPSI, t.CPUPSI))
	}
	memPct := t.MemPct
	if host && memPct > 0 {
		memPct = max(memPct, hostMemPct)
	}
	if p := r.MemPct(); memPct > 0 && p >= memPct {
		out = append(out, fmt.Sprintf("memory %.0f%% of %s > %.0f%%", p, gib(r.MemMax), memPct))
	}
	if t.MemPSI > 0 && r.MemPSI >= t.MemPSI {
		out = append(out, fmt.Sprintf("memory PSI %.0f%% > %.0f%%", r.MemPSI, t.MemPSI))
	}
	if t.IOPSI > 0 && r.IOPSI >= t.IOPSI {
		out = append(out, fmt.Sprintf("io PSI %.0f%% > %.0f%%", r.IOPSI, t.IOPSI))
	}
	if host && t.Load > 0 && r.Load1 >= 0 && r.CPUs > 0 && r.Load1/r.CPUs >= t.Load {
		out = append(out, fmt.Sprintf("load %.1f per CPU > %.1f", r.Load1/r.CPUs, t.Load))
	}
	return out
}

// near is true when r is above warnShare of a threshold: the gauge turns amber.
func (t Thresholds) near(r Reading, host bool) bool {
	const warnShare = 0.8
	w := Thresholds{CPUPSI: t.CPUPSI * warnShare, MemPct: t.MemPct * warnShare, MemPSI: t.MemPSI * warnShare,
		IOPSI: t.IOPSI * warnShare, Load: t.Load * warnShare}
	if host && w.MemPct > 0 {
		w.MemPct = max(w.MemPct, hostMemPct*warnShare)
	}
	return len(w.over(r, host)) > 0
}

func gib(b int64) string {
	return strconv.FormatFloat(float64(b)/(1<<30), 'f', 1, 64) + " GiB"
}

// parsePSI reads one PSI file (/proc/pressure/cpu, a cgroup's cpu.pressure): the avg10 of its
// "some" and "full" lines, -1 when absent.
func parsePSI(b []byte) (some, full float64) {
	some, full = -1, -1
	for _, line := range strings.Split(string(b), "\n") {
		kind, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		for _, f := range strings.Fields(rest) {
			v, ok := strings.CutPrefix(f, "avg10=")
			if !ok {
				continue
			}
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				break
			}
			switch kind {
			case "some":
				some = x
			case "full":
				full = x
			}
		}
	}
	return some, full
}

// keyed reads "key value" lines (memory.stat, cpu.stat, /proc/meminfo: "Key: value kB").
func keyed(b []byte) map[string]int64 {
	m := map[string]int64{}
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		f := strings.Fields(s.Text())
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		m[strings.TrimSuffix(f[0], ":")] = v
	}
	return m
}

// cpuMark is a scope's CPU time (in microseconds) at a sample, for the next one's rate.
type cpuMark struct {
	usec int64
	at   time.Time
}

// HostPressure samples the host (/proc) and each lea container's cgroup, found by its container
// ID (docker inspect, either engine) in the host's cgroup tree: never by a host pid, which lea-ctl
// (its own pid namespace) could not tell from one of its own.
type HostPressure struct {
	Proc, Cgroup string // "/proc", "/sys/fs/cgroup" when empty (tests: a fake tree)
	// ID is a container's full ID (nil: Bin inspect).
	ID func(ctx context.Context, container string) (string, error)
	// Bin is the engine's CLI ("docker" when empty; podman answers the same inspect).
	Bin string
	Now func() time.Time

	mu   sync.Mutex
	dirs map[string]string // container -> its cgroup directory, while it exists
	prev map[string]cpuMark
}

func (h *HostPressure) roots() (string, string) {
	proc, cg := h.Proc, h.Cgroup
	if proc == "" {
		proc = "/proc"
	}
	if cg == "" {
		cg = "/sys/fs/cgroup"
	}
	return proc, cg
}

func (h *HostPressure) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Sample reads the host and each container; a container it cannot read is left out.
func (h *HostPressure) Sample(ctx context.Context, containers []string) (Reading, map[string]Reading) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dirs == nil {
		h.dirs, h.prev = map[string]string{}, map[string]cpuMark{}
	}
	proc, _ := h.roots()
	host := h.readHost(proc)
	scopes := map[string]Reading{}
	for _, c := range containers {
		dir, err := h.cgroupOf(ctx, c)
		if err != nil {
			continue
		}
		scopes[c] = h.readCgroup(c, dir)
	}
	// Containers gone: their cgroup and CPU mark go too.
	for c := range h.dirs {
		if _, ok := scopes[c]; !ok {
			delete(h.dirs, c)
			delete(h.prev, c)
		}
	}
	return host, scopes
}

func (h *HostPressure) rate(key string, usec int64) float64 {
	now := h.now()
	p, ok := h.prev[key]
	h.prev[key] = cpuMark{usec: usec, at: now}
	if !ok || !now.After(p.at) || usec < p.usec {
		return -1
	}
	return float64(usec-p.usec) / float64(now.Sub(p.at).Microseconds())
}

func (h *HostPressure) readHost(proc string) Reading {
	r := unknownReading()
	r.CPUs = float64(runtime.NumCPU())
	if b, err := os.ReadFile(filepath.Join(proc, "pressure/cpu")); err == nil {
		r.CPUPSI, _ = parsePSI(b)
	}
	if b, err := os.ReadFile(filepath.Join(proc, "pressure/memory")); err == nil {
		_, r.MemPSI = parsePSI(b)
	}
	if b, err := os.ReadFile(filepath.Join(proc, "pressure/io")); err == nil {
		_, r.IOPSI = parsePSI(b)
	}
	if b, err := os.ReadFile(filepath.Join(proc, "meminfo")); err == nil {
		m := keyed(b)
		if t, ok := m["MemTotal"]; ok {
			r.MemMax = t << 10
			if a, ok := m["MemAvailable"]; ok {
				r.MemUsed = (t - a) << 10
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(proc, "loadavg")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if x, err := strconv.ParseFloat(f[0], 64); err == nil {
				r.Load1 = x
			}
		}
	}
	// /proc/stat's first line: user nice system idle iowait irq softirq steal, in clock ticks
	// (USER_HZ, 100 on Linux).
	if b, err := os.ReadFile(filepath.Join(proc, "stat")); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		if f := strings.Fields(line); len(f) >= 9 && f[0] == "cpu" {
			var busy int64
			for i, v := range f[1:9] {
				x, _ := strconv.ParseInt(v, 10, 64)
				if i != 3 && i != 4 { // idle, iowait
					busy += x
				}
			}
			r.CPUUsed = h.rate("", busy*10000)
		}
	}
	return r
}

func (h *HostPressure) readCgroup(c, dir string) Reading {
	r := unknownReading()
	read := func(f string) []byte { b, _ := os.ReadFile(filepath.Join(dir, f)); return b }
	if b := read("cpu.pressure"); b != nil {
		r.CPUPSI, _ = parsePSI(b)
	}
	if b := read("memory.pressure"); b != nil {
		_, r.MemPSI = parsePSI(b)
	}
	if b := read("io.pressure"); b != nil {
		_, r.IOPSI = parsePSI(b)
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(string(read("memory.current"))), 10, 64); err == nil {
		r.MemUsed = v
		// The page cache the kernel drops first is no pressure (a git checkout fills it).
		if in, ok := keyed(read("memory.stat"))["inactive_file"]; ok && in <= v {
			r.MemUsed = v - in
		}
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(string(read("memory.max"))), 10, 64); err == nil {
		r.MemMax = v
	}
	if f := strings.Fields(string(read("cpu.max"))); len(f) == 2 {
		q, e1 := strconv.ParseFloat(f[0], 64)
		p, e2 := strconv.ParseFloat(f[1], 64)
		if e1 == nil && e2 == nil && p > 0 {
			r.CPUs = q / p
		}
	}
	if u, ok := keyed(read("cpu.stat"))["usage_usec"]; ok {
		r.CPUUsed = h.rate("c/"+c, u)
	}
	return r
}

// cgroupOf is the cgroup directory of container c, from its ID (cached while that directory
// exists: a container re-created under the same name has a new ID).
func (h *HostPressure) cgroupOf(ctx context.Context, c string) (string, error) {
	if d, ok := h.dirs[c]; ok {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d, nil
		}
		delete(h.dirs, c)
	}
	id, err := h.id(ctx, c)
	if err != nil {
		return "", err
	}
	_, cg := h.roots()
	d, err := cgroupByID(cg, id)
	if err != nil {
		return "", fmt.Errorf("%s: %w", c, err)
	}
	h.dirs[c] = d
	return d, nil
}

// cgroupByID finds the cgroup v2 directory of the container with full ID id below root, under
// each engine's naming: docker-<id>.scope and libpod-<id>.scope (systemd driver, rootless or
// not), <id> (cgroupfs driver). Podman's libpod-conmon-<id>.scope is its monitor, not the
// container. Not followed: symbolic links; not searched: below maxCgroupDepth.
func cgroupByID(root, id string) (string, error) {
	if !fullID(id) {
		return "", fmt.Errorf("not a container ID: %q", id)
	}
	root = filepath.Clean(root)
	found := ""
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return fs.SkipDir
		}
		if !e.IsDir() {
			return nil
		}
		name := strings.TrimSuffix(e.Name(), ".scope")
		if p != root && (name == id || name == "docker-"+id || name == "libpod-"+id) {
			found = p
			return fs.SkipAll
		}
		if strings.Count(strings.TrimPrefix(p, root), "/") >= maxCgroupDepth {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no cgroup v2 for container %.12s", id)
	}
	return found, nil
}

// maxCgroupDepth bounds the search: rootless Podman's scopes are six levels down
// (user.slice/user-N.slice/user@N.service/user.slice/libpod-ID.scope).
const maxCgroupDepth = 8

// fullID says whether id is a full container ID: 64 lowercase hex digits (either engine).
func fullID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (h *HostPressure) id(ctx context.Context, c string) (string, error) {
	if h.ID != nil {
		return h.ID(ctx, c)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	bin := h.Bin
	if bin == "" {
		bin = "docker"
	}
	out, err := exec.CommandContext(ctx, bin, "inspect", "-f", "{{.Id}}", c).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
