package wrapper

import (
	"fmt"
	"os"
	"strings"
	"time"
)

var traceStart = time.Now()

func traceEnabled() bool {
	v := strings.TrimSpace(os.Getenv("TRACE"))
	if v == "" {
		return false
	}
	v = strings.ToLower(v)
	return v == "1" || v == "true" || v == "yes" || v == "y"
}

func tracef(format string, args ...any) {
	if !traceEnabled() {
		return
	}
	now := time.Now()
	// Include wall-clock and monotonic delta for easier correlation.
	prefix := fmt.Sprintf("[TRACE %s +%dms] ", now.Format("15:04:05.000"), now.Sub(traceStart).Milliseconds())
	fmt.Printf(prefix+format+"\n", args...)
}

// Tracef is a public tracing hook for APP code.
// It respects the same TRACE env var and time base as internal wrapper traces.
func Tracef(format string, args ...any) {
	tracef(format, args...)
}
