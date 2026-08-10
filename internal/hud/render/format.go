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

// Elapsed computes time-in-state from a StateSince timestamp.
//
// It defends a zero timestamp: time.Since(time.Time{}) is about 2562047h
// positive (not negative, so ElapsedString's own clamp does not save us), and
// an older daemon, a replayed snapshot, or a future regression could all
// produce one.
//
// The result is truncated to whole seconds so that repeated calls within the
// same second are equal — both HUD backends rely on this to keep their
// per-tab/surface equality-based skip-checks meaningful: without it, every
// snapshot broadcast (even one carrying no observable change for a given
// tab) would defeat the check and force a redraw regardless of whether
// anything about that tab actually changed.
//
// Hoisted out of the x11 and wlr backends (Task 8 review): the decision was
// byte-for-byte duplicated in both, and neither half touches a backend-
// specific type — exactly the "duplication of a decision" class this branch
// keeps producing.
func Elapsed(since, now time.Time) time.Duration {
	if since.IsZero() {
		return 0
	}
	return now.Sub(since).Truncate(time.Second)
}

// ElapsedChanged is the pure decision behind a backend's "should I re-render
// for a new elapsed string" tick: whether the rendered elapsed string for
// `since` at `now` differs from the last one actually painted (`last`).
// Returns the freshly rendered string either way so the caller can cache it
// without recomputing.
func ElapsedChanged(since, now time.Time, last string) (changed bool, str string) {
	str = ElapsedString(Elapsed(since, now))
	return str != last, str
}

// HaloPhaseStep quantises `now` into one of HaloSteps discrete steps of the
// HaloPeriod pulse cycle starting at `start` (see HaloSteps for why 8 and not
// ~30), returning both the step index and the HaloPhase in [0,1) it
// corresponds to. Callers compare the step against the last-rendered one to
// decide whether a redraw is warranted at all.
func HaloPhaseStep(start, now time.Time) (step int, phase float64) {
	// Integer nanosecond arithmetic, not float seconds: HaloPeriod (1.6) has
	// no exact float64 representation, so a naive
	// elapsed.Seconds()/HaloPeriod computation lands a hair under exact step
	// boundaries (e.g. 0.6/1.6*8 evaluates to ~2.999999999995, not 3) and
	// truncates one step short. Nanoseconds are exact integers, so the mod
	// and scaled division below never misses a boundary.
	periodNS := int64(HaloPeriod * float64(time.Second))
	elapsedNS := now.Sub(start).Nanoseconds() % periodNS
	if elapsedNS < 0 {
		elapsedNS += periodNS
	}
	step = int(elapsedNS * int64(HaloSteps) / periodNS)
	phase = float64(step) / float64(HaloSteps)
	return step, phase
}
