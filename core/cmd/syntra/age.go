package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	v1 "syntra.local/mcp/gen/syntra/v1"
)

// relativeAge renders how long ago then was, in whole units of the largest fitting
// size. A time in the future (clock skew) reads as just now.
func relativeAge(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d/(7*24*time.Hour)))
	case d < 360*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy ago", max(1, int(d/(365*24*time.Hour))))
	}
}

// writeThreadsText prints one aligned line per thread. A thread without a
// published message of its own (for example a pending preparation) shows "-".
func writeThreadsText(w io.Writer, threads []*v1.Thread, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGE\tSTATE\tID\tTITLE")
	for _, t := range threads {
		age := "-"
		if ts := t.GetLastMessagePublishedAt(); ts != nil && ts.CheckValid() == nil {
			age = relativeAge(now, ts.AsTime())
		}
		state := strings.ToLower(strings.TrimPrefix(t.GetState().String(), "THREAD_STATE_"))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", age, state, t.GetId(), plainLine(t.GetTitle()))
	}
	return tw.Flush()
}

// plainLine keeps untrusted thread text to one printable line.
func plainLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
