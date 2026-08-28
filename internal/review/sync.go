package review

import (
	"context"
	"fmt"
	"os"

	"github.com/mgreau/zen/internal/agent"
	wt "github.com/mgreau/zen/internal/worktree"
)

// SyncOutcome is the result of trying to fast-forward an existing PR worktree.
type SyncOutcome int

const (
	// SyncMissing means the worktree directory does not exist.
	SyncMissing SyncOutcome = iota
	// SyncUpToDate means HEAD already matches the fetched PR head (or wantSHA).
	SyncUpToDate
	// SyncUpdated means the worktree was fast-forwarded to a new commit.
	SyncUpdated
	// SyncSkippedDirty means tracked files have local changes; nothing was moved.
	SyncSkippedDirty
	// SyncSkippedAgent means an agent process is running in the worktree.
	SyncSkippedAgent
	// SyncSkippedReset means GitHub rewrote history and reset --hard was not
	// confirmed (daemon/MCP never reset; CLI declined or --json).
	SyncSkippedReset
)

func (o SyncOutcome) String() string {
	switch o {
	case SyncMissing:
		return "missing"
	case SyncUpToDate:
		return "up-to-date"
	case SyncUpdated:
		return "updated"
	case SyncSkippedDirty:
		return "skipped-dirty"
	case SyncSkippedAgent:
		return "skipped-agent"
	case SyncSkippedReset:
		return "skipped-reset"
	default:
		return fmt.Sprintf("SyncOutcome(%d)", o)
	}
}

// ResetRequest is passed to ConfirmReset when a clean idle worktree cannot
// fast-forward onto GitHub's head (typically a force-push).
type ResetRequest struct {
	PRNumber      int
	UniqueCommits int // commits on HEAD that are not in origin/pr-N
}

// ConfirmReset asks whether to git reset --hard onto the rewritten GitHub head.
// Nil means never reset (daemon, MCP, non-interactive --json).
type ConfirmReset func(ResetRequest) bool

// runningIn is swapped in tests.
var runningIn = agent.RunningIn

// SyncExisting fetches pull/N/head into a remote-tracking ref and moves the
// worktree to that commit. A linear update is git merge --ff-only. If the
// author rewrote history, reset --hard runs only when confirmReset returns
// true (CLI prompt). Nil confirmReset never resets — the daemon and MCP skip.
// Dirty worktrees and live agent sessions are left alone.
//
// wantSHA is the GitHub head OID. When it matches worktree HEAD, fetch is skipped.
// An empty wantSHA always fetches.
func SyncExisting(ctx context.Context, originPath, worktreePath string, prNumber int, wantSHA string, log Logger, confirmReset ConfirmReset) (SyncOutcome, error) {
	if log == nil {
		log = noop
	}
	if _, err := os.Stat(worktreePath); err != nil {
		return SyncMissing, nil
	}

	if runningIn(worktreePath) {
		log(fmt.Sprintf("skipping fetch for PR #%d: agent session is running", prNumber))
		return SyncSkippedAgent, nil
	}

	dirty, err := wt.TrackedDirty(worktreePath)
	if err != nil {
		return SyncUpToDate, err
	}
	if dirty {
		log(fmt.Sprintf("skipping fetch for PR #%d: worktree has local changes", prNumber))
		return SyncSkippedDirty, nil
	}

	if wantSHA != "" {
		head, herr := wt.HEAD(worktreePath)
		if herr == nil && wt.SHAEqual(head, wantSHA) {
			return SyncUpToDate, nil
		}
	}

	wt.GitMu.Lock()
	defer wt.GitMu.Unlock()

	// Re-check after lock: another poll may have updated, or the user dirtied it.
	if runningIn(worktreePath) {
		log(fmt.Sprintf("skipping fetch for PR #%d: agent session is running", prNumber))
		return SyncSkippedAgent, nil
	}
	dirty, err = wt.TrackedDirty(worktreePath)
	if err != nil {
		return SyncUpToDate, err
	}
	if dirty {
		log(fmt.Sprintf("skipping fetch for PR #%d: worktree has local changes", prNumber))
		return SyncSkippedDirty, nil
	}

	gitCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	if err := wt.FetchPRHead(gitCtx, originPath, prNumber); err != nil {
		return SyncUpToDate, err
	}

	head, herr := wt.HEAD(worktreePath)
	remote, rerr := wt.RevParse(worktreePath, wt.RemotePRRef(prNumber))
	if herr == nil && rerr == nil && wt.SHAEqual(head, remote) {
		return SyncUpToDate, nil
	}

	if runningIn(worktreePath) {
		log(fmt.Sprintf("skipping merge for PR #%d: agent session is running", prNumber))
		return SyncSkippedAgent, nil
	}

	if err := wt.FastForward(gitCtx, worktreePath, prNumber); err != nil {
		if !wt.IsNonFastForward(err) {
			return SyncUpToDate, err
		}
		return maybeReset(gitCtx, worktreePath, prNumber, log, confirmReset)
	}
	log(fmt.Sprintf("fast-forwarded PR #%d worktree", prNumber))
	return SyncUpdated, nil
}

func maybeReset(ctx context.Context, worktreePath string, prNumber int, log Logger, confirmReset ConfirmReset) (SyncOutcome, error) {
	if runningIn(worktreePath) {
		log(fmt.Sprintf("skipping reset for PR #%d: agent session is running", prNumber))
		return SyncSkippedAgent, nil
	}
	dirty, err := wt.TrackedDirty(worktreePath)
	if err != nil {
		return SyncUpToDate, err
	}
	if dirty {
		log(fmt.Sprintf("skipping reset for PR #%d: worktree has local changes", prNumber))
		return SyncSkippedDirty, nil
	}

	unique, _ := wt.UniqueCommitCount(worktreePath, prNumber)
	req := ResetRequest{PRNumber: prNumber, UniqueCommits: unique}
	if confirmReset == nil || !confirmReset(req) {
		log(fmt.Sprintf("skipping reset for PR #%d: rewritten GitHub head (run zen review %d to reset)", prNumber, prNumber))
		return SyncSkippedReset, nil
	}
	if err := wt.ResetToRemotePR(ctx, worktreePath, prNumber); err != nil {
		return SyncUpToDate, err
	}
	log(fmt.Sprintf("reset PR #%d worktree to rewritten GitHub head", prNumber))
	return SyncUpdated, nil
}
