package render

import (
	"testing"
	"time"
)

func TestStateWords(t *testing.T) {
	cases := []struct {
		activity, attention, waiting, want string
	}{
		{"waiting", "needs", "permission", "blocked on approval"},
		{"waiting", "needs", "user", "waiting for you"},
		{"waiting", "dismissed", "", "dismissed"},
		{"working", "ack", "", "working"},
		{"waiting", "ack", "", "idle"},
		{"unknown", "ack", "", "idle"},
		// permission outranks everything, even while working.
		{"working", "needs", "permission", "blocked on approval"},
		// Divergence case: even with waiting=="permission", if attention is
		// dismissed, the words must match the colour (which For() returns as
		// Dismissed for this input).
		{"waiting", "dismissed", "permission", "dismissed"},
	}
	for _, c := range cases {
		if got := StateWords(c.activity, c.attention, c.waiting); got != c.want {
			t.Errorf("StateWords(%q,%q,%q) = %q, want %q",
				c.activity, c.attention, c.waiting, got, c.want)
		}
	}
}

func TestElapsedString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 00s"},
		{4*time.Minute + 12*time.Second, "4m 12s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 00m"},
		{time.Hour + 4*time.Minute, "1h 04m"},
		{26*time.Hour + 3*time.Minute, "26h 03m"},
		// Negative durations (clock skew between daemon and HUD) must not
		// render as garbage like "-1m -3s".
		{-5 * time.Second, "0s"},
	}
	for _, c := range cases {
		if got := ElapsedString(c.d); got != c.want {
			t.Errorf("ElapsedString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
