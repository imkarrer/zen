//go:build e2e && darwin

package macos

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Not part of `go test ./...` or `go build` (build tag e2e+darwin).
// Run: ZEN_E2E_MACOS=1 go test -tags e2e ./internal/macos -run TestOpenTabE2E
func TestOpenTabE2E(t *testing.T) {
	if os.Getenv("ZEN_E2E_MACOS") == "" {
		t.Skip("set ZEN_E2E_MACOS=1 to open Terminal.app")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "opened")
	if err := OpenTab(dir, "touch opened"); err != nil {
		t.Fatalf("OpenTab: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Terminal.app did not create %s within 15s (Automation permission for osascript → Terminal?)", marker)
}
