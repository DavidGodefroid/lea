package launcher

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// lea egress allow|deny|list|denied (#345): the egress allowlist (LEA_EGRESS_ALLOW), changed from
// the host, kept in the user configuration and applied to the running proxy at once. The decision
// is the user's: the command is a launcher one, which refuses to run inside lea (cmd/lea:
// runLauncher), and stays on the host with LEA_CTL=1 (ctlHostOnly). No MCP tool, daemon call or
// grant reaches it, and the list the proxy reads lives on the proxy's own tmpfs.

// egressDefault, egressAlways and egressDoH are EGRESS_DEFAULT, EGRESS_ALWAYS and DOH_NAMES of
// proxy/render-squid-conf.sh (TestEgressListsMatchTheProxy keeps them in step).
var (
	egressDefault = strings.Fields(`github.com githubusercontent.com ghcr.io gitlab.com golang.org registry.npmjs.org registry.yarnpkg.com
  pypi.org pythonhosted.org docker.io docker.com quay.io fedoraproject.org alpinelinux.org openai.com chatgpt.com`)
	egressAlways = strings.Fields("anthropic.com claude.ai claude.com")
	egressDoH    = strings.Fields(`dns.google dns.google.com cloudflare-dns.com one.one.one.one quad9.net nextdns.io adguard-dns.com
  dns.adguard.com opendns.com cleanbrowsing.org dns.sb dns.mullvad.net controld.com dns0.eu dns.switch.ch
  doh.libredns.gr dns.alidns.com doh.pub dns.twnic.tw`)
)

// egressApply is the proxy's script that replaces its allowlist and reconfigures squid; egressSrc
// holds the LEA_EGRESS_ALLOW value the running proxy's list comes from.
const (
	egressApply = "/usr/local/bin/egress-apply.sh"
	egressSrc   = "/run/lea-proxy/egress-allow.src"
)

const egressUsage = `usage: lea egress allow DOMAIN... | deny DOMAIN... | list | denied [--since DURATION]`

func (l *Launcher) egress(args []string) error {
	if len(args) == 0 {
		l.logf("error: %s", egressUsage)
		return exitError{2}
	}
	switch args[0] {
	case "allow", "deny":
		if len(args) < 2 {
			l.logf("error: %s", egressUsage)
			return exitError{2}
		}
		return l.egressChange(args[0] == "allow", args[1:])
	case "list":
		if len(args) != 1 {
			l.logf("error: %s", egressUsage)
			return exitError{2}
		}
		return l.egressList()
	case "denied":
		since := "24h"
		switch {
		case len(args) == 3 && args[1] == "--since":
			if _, err := time.ParseDuration(args[2]); err != nil {
				return die("--since takes a duration such as 30m or 24h, not '%s'", args[2])
			}
			since = args[2]
		case len(args) != 1:
			l.logf("error: %s", egressUsage)
			return exitError{2}
		}
		return l.egressDenied(since)
	}
	l.logf("error: %s", egressUsage)
	return exitError{2}
}

// checkEgressDomain is d as an entry of LEA_EGRESS_ALLOW (egressAllow's rules, one name), lower-cased.
// A URL, a wildcard, an IP literal, "default" and a name the proxy refuses whatever the list says (a
// DNS-over-HTTPS resolver) are refused with a message saying so.
func checkEgressDomain(d string) (string, error) {
	switch {
	case strings.Contains(d, "/") || strings.Contains(d, ":"):
		return "", fmt.Errorf("'%s' is not a domain name: give the host alone, as in lea egress allow deb.debian.org (no URL, port or IP address)", d)
	case strings.Contains(d, "*"):
		return "", fmt.Errorf("'%s': no wildcard; a domain already covers its subdomains (opening the whole web is LEA_EGRESS_ALLOW='*' in lea.conf, your call)", d)
	case d == "":
		return "", fmt.Errorf("'' is not a domain name")
	case strings.EqualFold(d, "default"):
		return "", fmt.Errorf("'default' is lea's default list: keep it in LEA_EGRESS_ALLOW in lea.conf")
	}
	n, err := egressAllow(d)
	if err != nil || strings.Contains(n, ",") {
		return "", fmt.Errorf("'%s' is not a domain name (letters, digits, dots and dashes; no IP address)", d)
	}
	if egressIsDoH(n) {
		return "", fmt.Errorf("%s is a public DNS-over-HTTPS resolver: lea-proxy refuses these whatever LEA_EGRESS_ALLOW says", n)
	}
	return n, nil
}

// dohLabel is the proxy's doh_re: any doh.* name or subdomain.
var dohLabel = regexp.MustCompile(`(^|\.)doh\.`)

func egressIsDoH(d string) bool {
	return dohLabel.MatchString(d) || egressCovers(egressDoH, d) != ""
}

// egressCovers is the entry of names that covers d (d itself or a parent domain), "" when none.
func egressCovers(names []string, d string) string {
	for _, n := range names {
		if d == n || strings.HasSuffix(d, "."+n) {
			return n
		}
	}
	return ""
}

// egressNames are the entries of a normalized LEA_EGRESS_ALLOW ("" is "default").
func egressNames(v string) []string {
	if v == "" {
		return []string{"default"}
	}
	return strings.Split(v, ",")
}

// egressCoveredBy says what already allows d under v: "lea.conf (<entry>)", "lea's default list"
// or "always (the model API)"; "" when nothing does.
func egressCoveredBy(v, d string) string {
	names := egressNames(v)
	if n := egressCovers(slices.DeleteFunc(slices.Clone(names), func(s string) bool { return s == "default" }), d); n != "" {
		return "LEA_EGRESS_ALLOW (" + n + ")"
	}
	if slices.Contains(names, "default") && egressCovers(egressDefault, d) != "" {
		return "lea's default list"
	}
	if egressCovers(egressAlways, d) != "" {
		return "lea's always-allowed list (the model APIs)"
	}
	return ""
}

// egressWith is the LEA_EGRESS_ALLOW value v with d added (added false when something already
// covers it), checked as LoadConfig checks it.
func egressWith(v, d string) (next string, added bool, err error) {
	if v == "*" || egressCoveredBy(v, d) != "" {
		return v, false, nil
	}
	next, err = egressAllow(strings.Join(append(egressNames(v), d), ","))
	return next, err == nil, err
}

// egressWithout is v without the entry d. Only an entry of the list can go: a domain of lea's
// default list or of the always list is refused with what covers it. The last entry cannot go:
// an empty LEA_EGRESS_ALLOW is lea's default list, wider than what it replaces.
func egressWithout(v, d string) (string, error) {
	if v == "*" {
		return "", fmt.Errorf("LEA_EGRESS_ALLOW='*' allows any domain: set a list in lea.conf to restrict it")
	}
	names := egressNames(v)
	kept := slices.DeleteFunc(slices.Clone(names), func(s string) bool { return s == d })
	if len(kept) == len(names) {
		if by := egressCoveredBy(v, d); by != "" {
			return "", fmt.Errorf("%s is not an entry of LEA_EGRESS_ALLOW: %s allows it", d, by)
		}
		return "", fmt.Errorf("%s is not in the egress allowlist", d)
	}
	if len(kept) == 0 {
		return "", fmt.Errorf("%s is the only entry of LEA_EGRESS_ALLOW, and an empty one means lea's default list: edit LEA_EGRESS_ALLOW in lea.conf instead", d)
	}
	return strings.Join(kept, ","), nil
}

// egressChange is lea egress allow|deny DOMAIN...: the new LEA_EGRESS_ALLOW written to the user
// configuration, then applied to the running proxy.
func (l *Launcher) egressChange(allow bool, domains []string) error {
	cur := l.Cfg.Get("LEA_EGRESS_ALLOW")
	next := cur
	var changed []string
	for _, a := range domains {
		d, err := checkEgressDomain(a)
		if err != nil {
			return die("%v", err)
		}
		if allow {
			n, added, err := egressWith(next, d)
			if err != nil {
				return die("%v", err)
			}
			if !added {
				if next == "*" {
					l.logf("%s: LEA_EGRESS_ALLOW='*' already allows any domain", d)
				} else {
					l.logf("%s is already allowed by %s", d, egressCoveredBy(next, d))
				}
				continue
			}
			next = n
		} else {
			n, err := egressWithout(next, d)
			if err != nil {
				return die("%v", err)
			}
			next = n
		}
		changed = append(changed, d)
	}
	if len(changed) == 0 {
		return nil
	}
	path := l.Cfg.UserConfigFile(l.Cwd)
	if err := WriteUserConfig(path, "LEA_EGRESS_ALLOW", next); err != nil {
		return die("%v", err)
	}
	verb := "allowed"
	if !allow {
		verb = "no longer allowed"
	}
	l.logf("%s %s: LEA_EGRESS_ALLOW='%s' in %s", strings.Join(changed, ", "), verb, next, path)
	return l.egressLive(next)
}

// egressLive applies the allowlist v to the running proxy, without restarting it; a proxy that is
// not running gets it at its next start.
func (l *Launcher) egressLive(v string) error {
	name := l.prefix() + "-proxy"
	switch {
	case l.Cfg.LockHTTP():
		l.logf("LEA_LOCKHTTP=1: the LockHTTP gate decides the agents' web destinations; the list applies once LockHTTP is off")
		return nil
	case !l.containerRunning(name):
		l.logf("%s is not running: the next lea starts it with this list", name)
		return nil
	}
	if err := l.egressApplyLive(name, v); err != nil {
		return die("%v; the list is saved: lea stop --all, then lea, starts the proxy with it", err)
	}
	l.logf("applied live to %s: no proxy or agent restart", name)
	return nil
}

// egressApplyLive replaces the allowlist of the running proxy name (egress-apply.sh, as root in
// the proxy, through the host's engine).
func (l *Launcher) egressApplyLive(name, v string) error {
	if v == "*" {
		return fmt.Errorf("LEA_EGRESS_ALLOW='*' drops the allowlist from the proxy's configuration: it needs a proxy restart")
	}
	if err := l.run("docker", "exec", "-u", "0", "-e", "LEA_EGRESS_ALLOW="+v, name, egressApply); err != nil {
		return fmt.Errorf("could not change the allowlist of the running %s (a proxy image before lea egress, LEA_EGRESS_ALLOW='*' at its start, or docker's message above)", name)
	}
	return nil
}

// egressLiveValue is the LEA_EGRESS_ALLOW value the running proxy name applies (egressSrc); ok
// is false when it cannot tell (an older proxy, no allowlist).
func (l *Launcher) egressLiveValue(name string) (string, bool) {
	out, err := l.output(false, "docker", "exec", name, "cat", egressSrc)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// egressList is lea egress list: each domain the agents may reach and why.
func (l *Launcher) egressList() error {
	v := l.Cfg.Get("LEA_EGRESS_ALLOW")
	w := l.Stdout
	if l.Cfg.LockHTTP() {
		fmt.Fprintln(w, "LEA_LOCKHTTP=1: the LockHTTP gate decides the agents' web destinations (LEA_EGRESS_ALLOW is not used)")
		return nil
	}
	if v == "*" {
		fmt.Fprintln(w, "LEA_EGRESS_ALLOW='*': any public domain (DNS-over-HTTPS resolvers, IP literals and private addresses stay refused)")
	} else {
		for _, n := range egressNames(v) {
			if n == "default" {
				for _, d := range egressDefault {
					fmt.Fprintf(w, "%-28s lea's default list\n", d)
				}
				continue
			}
			fmt.Fprintf(w, "%-28s LEA_EGRESS_ALLOW\n", n)
		}
		for _, d := range egressAlways {
			fmt.Fprintf(w, "%-28s always (the model APIs)\n", d)
		}
		fmt.Fprintln(w, "(each domain covers its subdomains; lea egress allow DOMAIN adds one, live)")
	}
	name := l.prefix() + "-proxy"
	if l.containerRunning(name) {
		if live, ok := l.egressLiveValue(name); ok && live != v {
			fmt.Fprintf(w, "note: the running %s applies LEA_EGRESS_ALLOW='%s'; the next lea applies this list to it\n", name, live)
		}
	}
	return nil
}

// egressRefusal is one host the proxy refused for the allowlist.
type egressRefusal struct {
	host    string
	count   int
	last    time.Time
	clients []string
}

// parseEgressRefusals reads the proxy's access log (logformat lea of render-squid-conf.sh:
// time client method url status upstream error) and returns the hosts refused with
// ERR_LEA_EGRESS, most recent first.
func parseEgressRefusals(log string) []egressRefusal {
	by := map[string]*egressRefusal{}
	for _, line := range strings.Split(log, "\n") {
		f := strings.Fields(line)
		if len(f) != 7 || f[4] != "403" || f[6] != "ERR_LEA_EGRESS" {
			continue
		}
		host := f[3]
		if f[2] == "CONNECT" {
			host, _, _ = strings.Cut(host, ":")
		} else if u, err := url.Parse(host); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		} else {
			continue
		}
		host = strings.ToLower(host)
		if _, err := checkEgressDomain(host); err != nil {
			continue
		}
		var ts time.Time
		if sec, frac, ok := strings.Cut(f[0], "."); ok {
			var s, ms int64
			if _, err := fmt.Sscanf(sec+" "+frac, "%d %d", &s, &ms); err == nil {
				ts = time.Unix(s, ms*int64(time.Millisecond))
			}
		}
		r := by[host]
		if r == nil {
			r = &egressRefusal{host: host}
			by[host] = r
		}
		r.count++
		if ts.After(r.last) {
			r.last = ts
		}
		if !slices.Contains(r.clients, f[1]) {
			r.clients = append(r.clients, f[1])
		}
	}
	out := make([]egressRefusal, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].last.Equal(out[j].last) {
			return out[i].last.After(out[j].last)
		}
		return out[i].host < out[j].host
	})
	return out
}

// egressDenied is lea egress denied: the hosts the proxy refused for the allowlist lately, with
// the containers that asked and the command that allows each.
func (l *Launcher) egressDenied(since string) error {
	name := l.prefix() + "-proxy"
	if !l.containerRunning(name) {
		return die("%s is not running", name)
	}
	log, err := l.output(false, "docker", "logs", "--since", since, name)
	if err != nil {
		return die("could not read the logs of %s", name)
	}
	rs := parseEgressRefusals(log)
	if len(rs) == 0 {
		fmt.Fprintf(l.Stdout, "no host refused for the egress allowlist in the last %s\n", since)
		return nil
	}
	who := l.containersByIP()
	for _, r := range rs {
		var from []string
		for _, c := range r.clients {
			from = append(from, nonEmpty(who[c], c))
		}
		fmt.Fprintf(l.Stdout, "%s refused %d time(s), last at %s, from %s: lea egress allow %s\n",
			r.host, r.count, r.last.Local().Format("15:04:05"), strings.Join(from, ", "), r.host)
	}
	return nil
}

// containersByIP maps the addresses of lea's containers to their names, best effort.
func (l *Launcher) containersByIP() map[string]string {
	m := map[string]string{}
	ids, err := l.output(false, "docker", "ps", "-q", "--filter", "label=lea.prefix="+l.prefix())
	if err != nil || ids == "" {
		return m
	}
	out, err := l.output(false, "docker", append([]string{"inspect", "-f",
		"{{.Name}}{{range .NetworkSettings.Networks}} {{.IPAddress}}{{end}}"}, strings.Fields(ids)...)...)
	if err != nil {
		return m
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		for _, ip := range f[min(1, len(f)):] {
			m[ip] = strings.TrimPrefix(f[0], "/")
		}
	}
	return m
}

// proxyEgressLive is ensureProxy's case of a running proxy that differs from what lea wants in its
// egress allowlist alone (its lea.egress-allow label: the list it started with): it reports true
// when that proxy applies allow, already (lea egress allow changed it live: egressSrc says so) or
// once egressApplyLive gives it. A change to or from '*' changes squid.conf itself: false, the
// proxy is replaced.
func (l *Launcher) proxyEgressLive(name, allow, got string, wantFor func(string) string) bool {
	label, err := l.output(false, "docker", "container", "inspect", "-f", `{{index .Config.Labels "lea.egress-allow"}}`, name)
	if err != nil || label == allow || label == "*" || allow == "*" || got != wantFor(label) {
		return false
	}
	if live, ok := l.egressLiveValue(name); !ok {
		return false
	} else if live == allow {
		return true
	}
	if l.egressApplyLive(name, allow) != nil {
		return false
	}
	l.logf("applied the egress allowlist '%s' to the running %s, live", allow, name)
	return true
}
