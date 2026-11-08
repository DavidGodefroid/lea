package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

const questionsUsage = "usage: lea questions [drop ID]"

// runQuestions is lea questions (#95): the questions each control tower has for the user, the
// open one first, then its queue; drop ID removes a queued one, which is then never asked.
func runQuestions(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "lea:", clean(err.Error(), false)); return 1 }
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "drop":
		id, err := strconv.ParseInt(strings.TrimPrefix(args[1], "#"), 10, 64)
		if err != nil || id < 1 {
			fmt.Fprintln(stderr, questionsUsage)
			return 2
		}
		if err := hostCall("question_drop", map[string]any{"id": id}, nil); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "dropped question #%d\n", id)
		return 0
	default:
		fmt.Fprintln(stderr, questionsUsage)
		return 2
	}
	var qs []struct {
		ID                           int64
		Agent, From, Priority, State string
		Body                         string
	}
	if err := hostCall("question_list", nil, &qs); err != nil {
		return fail(err)
	}
	if len(qs) == 0 {
		fmt.Fprintln(stdout, "no question waits for you")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTOWER\tSTATE\tPRIORITY\tFROM\tQUESTION")
	for _, q := range qs {
		body := strings.Join(strings.Fields(q.Body), " ")
		if r := []rune(body); len(r) > 80 {
			body = string(r[:79]) + "…"
		}
		from := q.From
		if from == "" {
			from = "-"
		}
		fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\t%s\t%s\n", q.ID, clean(q.Agent, false), clean(q.State, false), clean(q.Priority, false),
			clean(from, false), clean(body, false))
	}
	tw.Flush()
	return 0
}
