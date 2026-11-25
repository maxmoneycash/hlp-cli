package glyph

import (
	"os"
	"strings"
)

// Reduced reports whether motion is off: cfg is the `ui.motion` config value
// ("off" turns motion off), and the environment can force it with
// HLP_REDUCED_MOTION=1, any NO_MOTION value, or TERM=dumb (DESIGN §7).
func Reduced(cfg string) bool {
	if strings.EqualFold(strings.TrimSpace(cfg), "off") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HLP_REDUCED_MOTION"))) {
	case "", "0", "false", "no", "off":
	default:
		return true
	}
	if _, ok := os.LookupEnv("NO_MOTION"); ok {
		return true
	}
	return os.Getenv("TERM") == "dumb"
}
