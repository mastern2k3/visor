package render

import (
	"fmt"
	"time"
)

// StateWords is the human-readable state shown on the expanded panel's second
// line. Precedence matches Palette.For so the words and the colour can never
// disagree.
func StateWords(activity, attention, waiting string) string {
	switch {
	case attention == "needs" && waiting == "permission":
		return "blocked on approval"
	case attention == "needs":
		return "waiting for you"
	case attention == "dismissed":
		return "dismissed"
	case activity == "working":
		return "working"
	default:
		return "idle"
	}
}

// ElapsedString renders time-in-state compactly. Two-digit zero padding on the
// trailing unit keeps the string a stable width so the tabular-figure counter
// does not jitter as it ticks.
func ElapsedString(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%dh %02dm", h, m)
	}
}
