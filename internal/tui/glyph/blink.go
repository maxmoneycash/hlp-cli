package glyph

import "time"

// Cursor blink timing: a hard step, visible 605 ms then hidden 495 ms, a
// 1.1 s period (the landing's `landing-blink`, steps(1,end)).
const (
	BlinkOn     = 605 * time.Millisecond
	BlinkOff    = 495 * time.Millisecond
	BlinkPeriod = BlinkOn + BlinkOff
)

func blinkPhase(lastKey, now time.Time) time.Duration {
	e := now.Sub(lastKey)
	if e < 0 {
		return 0
	}
	return e % BlinkPeriod
}

// CursorOn reports whether the block cursor is drawn at now. The phase starts
// at lastKey (the last keypress, or when focus came back), so any key shows
// the cursor at once.
func CursorOn(lastKey, now time.Time) bool {
	return blinkPhase(lastKey, now) < BlinkOn
}

// BlinkNext is the time from now until the cursor next changes state. It is
// always positive; the caller decides whether to schedule it at all (only
// while focused, idle and not reduced).
func BlinkNext(lastKey, now time.Time) time.Duration {
	if now.Before(lastKey) {
		return lastKey.Sub(now) + BlinkOn
	}
	p := blinkPhase(lastKey, now)
	if p < BlinkOn {
		return BlinkOn - p
	}
	return BlinkPeriod - p
}
