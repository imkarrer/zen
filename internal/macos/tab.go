// Package macos opens tabs in the macOS Terminal.app via AppleScript.
package macos

import (
	"fmt"
	"os"
	"os/exec"
)

// OpenTab opens a new Terminal.app tab (or a new window if none exist)
// and runs the given command.
func OpenTab(workDir, command string) error {
	fullCmd := fmt.Sprintf("cd %q && %s", workDir, command)

	// Pass the shell command via env var to avoid AppleScript string escaping.
	// do script without a window creates a new window; do script in front window
	// creates a new tab on modern macOS.
	script := `tell application "Terminal"
	activate
	if (count of windows) is 0 then
		do script (system attribute "ZEN_MACOS_CMD")
	else
		do script (system attribute "ZEN_MACOS_CMD") in front window
	end if
end tell`

	cmd := exec.Command("osascript", "-e", script)
	cmd.Env = append(os.Environ(), "ZEN_MACOS_CMD="+fullCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, string(out))
	}
	return nil
}
