package cliproxy

import (
	"testing"
	"time"
)

// The window bounds how long a terminating process may spend finishing requests
// that are already streaming. Five minutes is the floor for an LLM proxy; the
// previous fixed 30s cut long answers off mid-stream on every deploy.
func TestShutdownGracePeriodDefaultsToFiveMinutes(t *testing.T) {
	t.Setenv("CLIRELAY_SHUTDOWN_GRACE", "")
	if got := shutdownGracePeriod(); got != 5*time.Minute {
		t.Fatalf("grace period = %s, want 5m", got)
	}
}

func TestShutdownGracePeriodHonoursOverride(t *testing.T) {
	t.Setenv("CLIRELAY_SHUTDOWN_GRACE", "90s")
	if got := shutdownGracePeriod(); got != 90*time.Second {
		t.Fatalf("grace period = %s, want 90s", got)
	}
}

// A malformed or non-positive override must not silently disable the wait —
// falling back to zero would make every deploy sever in-flight requests.
func TestShutdownGracePeriodRejectsUnusableValues(t *testing.T) {
	for _, raw := range []string{"nonsense", "0", "-30s"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("CLIRELAY_SHUTDOWN_GRACE", raw)
			if got := shutdownGracePeriod(); got != 5*time.Minute {
				t.Fatalf("grace period for %q = %s, want the 5m default", raw, got)
			}
		})
	}
}
