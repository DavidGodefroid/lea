package deck

import (
	"regexp"
	"strconv"
	"strings"
)

// releaseRe is a release version, vMAJOR.MINOR.PATCH.
var releaseRe = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

func release(v string) ([3]int, bool) {
	var r [3]int
	m := releaseRe.FindStringSubmatch(v)
	if m == nil {
		return r, false
	}
	for i := range r {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return r, false
		}
		r[i] = n
	}
	return r, true
}

// UpdateNotice is the deck's line (short: the deck is Width cells wide) for the background update's notice file (<host>/update-notice,
// written by libexec/lib/host.sh lea__notice: "vX.Y.Z installed", "vX.Y.Z major" or
// "vX.Y.Z available"), or "" when running (the deck's own build) is that release or a later one,
// is no release, or the file holds anything else.
func UpdateNotice(file, running string) string {
	f := strings.Fields(file)
	if len(f) != 2 {
		return ""
	}
	nv, ok1 := release(f[0])
	rv, ok2 := release(running)
	if !ok1 || !ok2 {
		return ""
	}
	newer := false
	for i := range nv {
		if nv[i] != rv[i] {
			newer = nv[i] > rv[i]
			break
		}
	}
	if !newer {
		return ""
	}
	switch f[1] {
	case "installed":
		return "lea " + f[0] + " installed: relaunch"
	case "major":
		return "lea " + f[0] + " (major): lea update"
	case "available":
		return "lea " + f[0] + ": run lea update"
	}
	return ""
}

// WithNotice gives the deck the update notice, read again on each snapshot ("" for none).
func (m Model) WithNotice(notice func() string) Model {
	m.notice = notice
	return m
}
