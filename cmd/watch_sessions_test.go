package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mgreau/zen/internal/agentdeck"
	"github.com/mgreau/zen/internal/config"
)

// fakeAgentDeck points agentdeck at a script that appends each argv, one
// argument per line and a blank line after, to a log, and answers reply.
func fakeAgentDeck(t *testing.T, reply string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls")
	script := "#!/bin/sh\n{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done; echo; } >> '" + logPath + "'\necho '" + reply + "'\n"
	bin := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := agentdeck.Bin
	agentdeck.Bin = bin
	t.Cleanup(func() { agentdeck.Bin = orig })
	return logPath
}

func withCfg(t *testing.T, c *config.Config) {
	t.Helper()
	orig := cfg
	cfg = c
	t.Cleanup(func() { cfg = orig })
}

func TestRegisterReviewSession(t *testing.T) {
	// ensureReviewPrompt writes under HOME; keep it off the real one.
	t.Setenv("HOME", t.TempDir())
	withCfg(t, &config.Config{Agent: "claude", ClaudeBin: "claude", Terminal: "agent-deck", Watch: config.WatchConfig{ReviewSessions: true}})
	log := fakeAgentDeck(t, `{"success": true, "id": "s-1"}`)

	worktree := filepath.Join(t.TempDir(), "repo-pr-5")
	registerReviewSession(worktree)

	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("agent-deck was not called: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(got) < 1 || got[0] != "add" {
		t.Fatalf("first argument = %q, want add (register without starting)", got)
	}
	if !strings.Contains(string(calls), "\n-cmd\nclaude '/review-pr'\n") {
		t.Errorf("command is not zen's review launch:\n%s", calls)
	}
}

func TestReviewSessionHooksOffByDefault(t *testing.T) {
	withCfg(t, &config.Config{Agent: "claude", ClaudeBin: "claude", Terminal: "agent-deck"})
	log := fakeAgentDeck(t, `{"success": true}`)

	registerReviewSession("/src/repo-pr-5")
	forgetReviewSession("/src/repo-pr-5")

	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("agent-deck was called with watch.review_sessions off")
	}
}

func TestForgetReviewSession(t *testing.T) {
	withCfg(t, &config.Config{Terminal: "agent-deck", Watch: config.WatchConfig{ReviewSessions: true}})
	log := fakeAgentDeck(t, `{"id": "s-1", "status": "error", "path": "/src/repo-pr-5", "success": true}`)

	forgetReviewSession("/src/repo-pr-5")

	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "session\nremove\n-force\n-json\ns-1\n") {
		t.Errorf("dead session was not removed:\n%s", calls)
	}
}
