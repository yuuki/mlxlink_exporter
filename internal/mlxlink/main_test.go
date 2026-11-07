package mlxlink

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if a test leaves a goroutine behind: the poller
// loop, the sweep it drives and the os/exec plumbing behind every mlxlink
// invocation must all be gone once their context is cancelled or the run
// returns.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
