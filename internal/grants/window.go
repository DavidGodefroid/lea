package grants

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	// The deploy windows name IANA time zones: the binary carries the database, so a host
	// without /usr/share/zoneinfo reads them too.
	_ "time/tzdata"
)

// Window is a lead's deploy window (#110, C1): a recurring time range on some days of the week,
// in a time zone, e.g. "mon-sun 21:30-23:59 Europe/Brussels" or "mon-fri,sun 22:00-02:00 UTC". A
// range whose end is before its start runs past midnight: it belongs to the day it starts on.
type Window struct {
	Days       [7]bool // by time.Weekday
	Start, End int     // minutes after midnight; End is exclusive
	Loc        *time.Location
	Text       string
}

var (
	windowRe = regexp.MustCompile(`^([a-z,-]{3,64}) ([0-9]{2}):([0-9]{2})-([0-9]{2}):([0-9]{2}) ([A-Za-z][A-Za-z0-9_+/-]{0,63})$`)
	weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
)

// MaxWindowLen bounds a window's text.
const MaxWindowLen = 128

// ParseWindow reads a window "DAYS HH:MM-HH:MM ZONE": DAYS is a comma-separated list of days
// (mon) and ranges (mon-fri, fri-mon wraps), ZONE an IANA time zone (Europe/Brussels, UTC).
func ParseWindow(s string) (Window, error) {
	if len(s) > MaxWindowLen {
		return Window{}, fmt.Errorf("window is %d bytes, limit %d", len(s), MaxWindowLen)
	}
	m := windowRe.FindStringSubmatch(s)
	if m == nil {
		return Window{}, errors.New(`window: "DAYS HH:MM-HH:MM ZONE", e.g. "mon-sun 21:30-23:59 Europe/Brussels"`)
	}
	w := Window{Text: s}
	for _, part := range strings.Split(m[1], ",") {
		a, b, isRange := strings.Cut(part, "-")
		da, ok := weekdays[a]
		if !ok {
			return Window{}, fmt.Errorf("window: unknown day %q (mon, tue, wed, thu, fri, sat, sun)", a)
		}
		db := da
		if isRange {
			if db, ok = weekdays[b]; !ok {
				return Window{}, fmt.Errorf("window: unknown day %q (mon, tue, wed, thu, fri, sat, sun)", b)
			}
		}
		for d := da; ; d = (d + 1) % 7 {
			w.Days[d] = true
			if d == db {
				break
			}
		}
	}
	hm := func(h, mi string) (int, bool) {
		hh, _ := strconv.Atoi(h)
		mm, _ := strconv.Atoi(mi)
		return hh*60 + mm, hh <= 23 && mm <= 59
	}
	var ok1, ok2 bool
	w.Start, ok1 = hm(m[2], m[3])
	w.End, ok2 = hm(m[4], m[5])
	if !ok1 || !ok2 {
		return Window{}, errors.New("window: times are HH:MM, 00:00 to 23:59")
	}
	if w.Start == w.End {
		return Window{}, errors.New("window: an empty range (start = end)")
	}
	if m[6] == "Local" {
		return Window{}, errors.New("window: name a time zone (Europe/Brussels, UTC), not Local")
	}
	loc, err := time.LoadLocation(m[6])
	if err != nil {
		return Window{}, fmt.Errorf("window: unknown time zone %q", m[6])
	}
	w.Loc = loc
	return w, nil
}

// Open says whether t falls inside the window. 23:59 as an end counts the whole last minute.
func (w Window) Open(t time.Time) bool {
	if w.Loc == nil {
		return false
	}
	t = t.In(w.Loc)
	min := t.Hour()*60 + t.Minute()
	end := w.End
	if end == 23*60+59 {
		end = 24 * 60
	}
	if w.Start < end {
		return w.Days[t.Weekday()] && min >= w.Start && min < end
	}
	// Past midnight: from Start on its day, or before End on the next day.
	if min >= w.Start {
		return w.Days[t.Weekday()]
	}
	return min < end && w.Days[(t.Weekday()+6)%7]
}
