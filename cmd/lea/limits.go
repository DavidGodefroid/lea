package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/DavidGodefroid/lea/internal/daemon"
	"github.com/DavidGodefroid/lea/internal/settings"
)

const limitsUsage = "usage: lea limits [get [KEY]] | lea limits set KEY VALUE | lea limits reset KEY"

// limitRow is one live limit as limits_get answers it.
type limitRow struct {
	Key, Env, Value, Default, Source, Help string
	AppliesTo                              string `json:"applies_to"`
	InUse                                  *int   `json:"in_use"`
}

// runLimits is lea limits (#420): the live limits of the running daemon (value, default,
// source), and set or reset one. The daemon applies a change at once, without a restart, and
// writes it to the user configuration, so the next lea keeps it; running agents are never
// stopped by a change.
func runLimits(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0 || (args[0] == "get" && len(args) <= 2):
		var r struct{ Limits []limitRow }
		if err := hostCall("limits_get", nil, &r); err != nil {
			fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
			return 1
		}
		key := ""
		if len(args) == 2 {
			key = args[1]
		}
		found := false
		for _, l := range r.Limits {
			if key != "" && key != l.Key && key != l.Env {
				continue
			}
			found = true
			printLimit(stdout, l)
		}
		if key != "" && !found {
			fmt.Fprintf(stderr, "lea: %s is not a live limit (lea limits lists them)\n", clean(key, false))
			return 1
		}
		return 0
	case args[0] == "set" && len(args) == 3:
		return setLimit(map[string]any{"key": args[1], "value": args[2]}, stdout, stderr)
	case args[0] == "reset" && len(args) == 2:
		return setLimit(map[string]any{"key": args[1], "reset": true}, stdout, stderr)
	}
	fmt.Fprintln(stderr, limitsUsage)
	return 2
}

func setLimit(params map[string]any, stdout, stderr io.Writer) int {
	var l limitRow
	if err := hostCall("limits_set", params, &l); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	printLimit(stdout, l)
	fmt.Fprintln(stdout, "applies now:", clean(l.AppliesTo, false))
	return 0
}

// printLimit prints "max_running  3  (default 3, from your file; 2 in use)".
func printLimit(w io.Writer, l limitRow) {
	src := map[string]string{"default": "lea's default", "file": "your file", "environment": "the environment"}[l.Source]
	if src == "" {
		src = clean(l.Source, false)
	}
	extra := ""
	if l.InUse != nil {
		extra = "; " + strconv.Itoa(*l.InUse) + " in use"
	}
	fmt.Fprintf(w, "%-12s %-6s (default %s, from %s%s)\n", clean(l.Key, false), clean(l.Value, false), clean(l.Default, false), src, extra)
}

// limitStore is the daemon's daemon.LimitStore: the live limits in the settings of the lea
// installed next to the daemon's binary, the deck's settings screen's own store.
type limitStore struct{ st settings.Store }

func (s limitStore) Load() (map[string]daemon.LimitValue, error) {
	list, err := s.st.List()
	if err != nil {
		return nil, err
	}
	out := map[string]daemon.LimitValue{}
	for _, l := range list {
		if l.Limit != "" {
			out[l.Key] = daemon.LimitValue{Value: l.Value, Source: l.Source}
		}
	}
	return out, nil
}

func (s limitStore) Save(key, value string) error { return s.st.Set(key, value) }
func (s limitStore) Reset(key string) error       { return s.st.Unset(key) }

// daemonLimits gives the daemon its live limits from the configuration of the lea at exe, read
// from the daemon's directory; without one, lea's defaults apply and limits_set is refused.
func daemonLimits(d *daemon.Daemon, exe string, stderr io.Writer) {
	cwd, _ := os.Getwd()
	var st daemon.LimitStore
	// The deck's settings screen reads the same files (deckSettings); the daemon always runs on
	// the host.
	root := filepath.Dir(filepath.Dir(exe))
	if exe != "" && fileExists(filepath.Join(root, "config", "lea.conf")) {
		st = limitStore{settings.Store{Root: root, Cwd: cwd, Getenv: os.Getenv}}
	} else {
		fmt.Fprintln(stderr, "lea daemon: live limits at lea's defaults: no config/lea.conf next to", exe)
	}
	if err := d.SetLimits(st); err != nil {
		fmt.Fprintln(stderr, "lea daemon: live limits:", clean(err.Error(), false))
	}
}
