package main

import (
	"fmt"
	"io"
)

const holdUsage = "usage: lea hold [--off] [AGENT]"

// runHold is lea hold (#95): the user pauses an agent's inbox (by default the only live lead's,
// the control tower): the messages its agents send it wait until lea hold --off, or LEA_HOLD_MAX,
// releases them at once. The deck's H does the same.
func runHold(args []string, stdout, stderr io.Writer) int {
	held, agent := true, ""
	for _, a := range args {
		switch {
		case a == "--off" && held:
			held = false
		case a != "" && a[0] != '-' && agent == "":
			agent = a
		default:
			fmt.Fprintln(stderr, holdUsage)
			return 2
		}
	}
	var r struct {
		Agent string
		Held  bool
		Count int
	}
	if err := hostCall("agent_hold", map[string]any{"agent": agent, "held": held}, &r); err != nil {
		fmt.Fprintln(stderr, "lea:", clean(err.Error(), false))
		return 1
	}
	name := clean(r.Agent, false)
	switch {
	case held:
		fmt.Fprintf(stdout, "holding the messages to %s (%d held): lea hold --off releases them\n", name, r.Count)
	case r.Count > 0:
		fmt.Fprintf(stdout, "released the messages to %s: %d arrive now\n", name, r.Count)
	default:
		fmt.Fprintf(stdout, "released the messages to %s\n", name)
	}
	return 0
}
