package mlxlinkexporter

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if a test leaves a goroutine behind, such as an
// HTTP server or client connection that was never closed.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
