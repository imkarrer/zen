package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"chainguard.dev/driftlessaf/workqueue"
	"chainguard.dev/driftlessaf/workqueue/dispatcher"
	"chainguard.dev/driftlessaf/workqueue/inmem"
	"github.com/chainguard-dev/clog"
	"github.com/mgreau/zen/internal/config"
	ghpkg "github.com/mgreau/zen/internal/github"
	"github.com/mgreau/zen/internal/notify"
	"github.com/mgreau/zen/internal/reconciler"
	"github.com/mgreau/zen/internal/ui"
	wt "github.com/mgreau/zen/internal/worktree"
	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch <action>",
	Short: "Background daemon (start|stop|status)",
	Long: `Background daemon for monitoring GitHub review requests.

Actions:
  start              Start the background daemon
  stop               Stop the background daemon
  status             Show daemon status
  logs               Tail daemon log output
  logs search <term> Search logs for a PR number, worktree, or keyword`,
	Args: cobra.RangeArgs(1, 3),
	RunE: runWatch,
}

func init() {
	rootCmd.AddCommand(watchCmd)
}

func runWatch(cmd *cobra.Command, args []string) error {
	action := args[0]
	switch action {
	case "start":
		return watchStart()
	case "stop":
		return watchStop()
	case "status":
		return watchStatus()
	case "logs":
		if len(args) >= 3 && args[1] == "search" {
			return watchLogSearch(args[2])
		}
		if len(args) >= 2 && args[1] == "search" {
			return fmt.Errorf("usage: zen watch logs search <term>")
		}
		return watchLogs()
	case "daemon":
		return watchDaemon()
	default:
		return fmt.Errorf("unknown action: %s (use start, stop, status, or logs)", action)
	}
}

func pidFile() string {
	return filepath.Join(config.StateDir(), "watch.pid")
}

func logFile() string {
	return filepath.Join(config.StateDir(), "watch.log")
}

func lastCheckFile() string {
	return filepath.Join(config.StateDir(), "last_check.json")
}

func watchIsRunning() (bool, int) {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}
	if err := syscall.Kill(pid, 0); err != nil {
		os.Remove(pidFile())
		return false, 0
	}
	return true, pid
}

func watchStart() error {
	if err := config.EnsureDirs(); err != nil {
		return err
	}

	running, pid := watchIsRunning()
	if running {
		ui.LogWarn(fmt.Sprintf("Watch daemon already running (PID: %d)", pid))
		return nil
	}

	binPath, err := os.Executable()
	if err != nil {
		return err
	}

	logF, err := os.OpenFile(logFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	attr := &os.ProcAttr{
		Dir:   os.Getenv("HOME"),
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, logF, logF},
	}

	proc, err := os.StartProcess(binPath, []string{binPath, "watch", "daemon"}, attr)
	if err != nil {
		logF.Close()
		return fmt.Errorf("starting daemon: %w", err)
	}
	logF.Close()

	if err := os.WriteFile(pidFile(), []byte(strconv.Itoa(proc.Pid)), 0o644); err != nil {
		return err
	}
	proc.Release()

	ui.LogSuccess(fmt.Sprintf("Watch daemon started (PID: %d)", proc.Pid))
	ui.LogInfo("Log file: " + logFile())
	return nil
}

func watchStop() error {
	running, pid := watchIsRunning()
	if !running {
		ui.LogWarn("Watch daemon is not running")
		return nil
	}
	syscall.Kill(pid, syscall.SIGTERM)
	os.Remove(pidFile())
	ui.LogSuccess(fmt.Sprintf("Watch daemon stopped (PID: %d)", pid))
	return nil
}

func watchLogs() error {
	lf := logFile()
	if _, err := os.Stat(lf); os.IsNotExist(err) {
		ui.LogWarn("No log file found. Start the daemon with 'zen watch start'.")
		return nil
	}
	cmd := exec.Command("tail", "-f", lf)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func watchLogSearch(term string) error {
	// Search both current and rotated log
	files := []string{logFile(), logFile() + ".1"}

	found := false
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cmd := exec.Command("grep", "-n", "-i", term, f)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			found = true
		}
	}

	if !found {
		fmt.Printf("No matches for %q in daemon logs.\n", term)
	}
	return nil
}

func watchStatus() error {
	fmt.Println()
	fmt.Println(ui.BoldText("Watch Daemon Status"))
	ui.Separator()

	running, pid := watchIsRunning()
	if running {
		fmt.Printf("Status: %s\n", ui.GreenText("Running"))
		fmt.Printf("PID: %d\n", pid)
	} else {
		fmt.Printf("Status: %s\n", ui.DimText("Not running"))
	}
	fmt.Println()

	data, err := os.ReadFile(lastCheckFile())
	if err == nil {
		var state struct {
			Timestamp string `json:"timestamp"`
			PRCount   int    `json:"pr_count"`
		}
		if json.Unmarshal(data, &state) == nil {
			fmt.Println("Last check:")
			fmt.Printf("  Time: %s\n", state.Timestamp)
			fmt.Printf("  PRs found: %d\n", state.PRCount)
		}
	}
	fmt.Println()

	if len(cfg.Authors) > 0 {
		fmt.Printf("Auto-spawn authors: %s\n", strings.Join(cfg.Authors, " "))
	} else {
		fmt.Println("Auto-spawn: disabled (no authors configured)")
	}
	fmt.Println()
	return nil
}

func watchDaemon() error {
	config.EnsureDirs()

	os.WriteFile(pidFile(), []byte(strconv.Itoa(os.Getpid())), 0o644)

	pollInterval := 5 * time.Minute
	if cfg.PollInterval != "" {
		if d, err := time.ParseDuration(cfg.PollInterval); err == nil {
			pollInterval = d
		}
	}

	watchCfg := cfg.Watch
	dispatchInterval := watchCfg.DispatchIntervalDuration()
	cleanupInterval := watchCfg.CleanupIntervalDuration()
	sessionScanInterval := watchCfg.SessionScanIntervalDuration()
	digestInterval, digestEnabled := watchCfg.DigestIntervalDuration()
	concurrency := watchCfg.GetConcurrency()
	maxRetries := watchCfg.GetMaxRetries()

	digestStr := "disabled"
	if digestEnabled {
		digestStr = digestInterval.String()
	}
	fmt.Printf("[%s] Watch daemon started (poll=%s, dispatch=%s, cleanup=%s, session_scan=%s, digest=%s, concurrency=%d, maxRetries=%d)\n",
		time.Now().Format(time.RFC3339), pollInterval, dispatchInterval, cleanupInterval, sessionScanInterval, digestStr, concurrency, maxRetries)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		cancel()
	}()

	// Create tagged contexts so dispatcher logs identify which queue they belong to
	setupCtx := clog.WithLogger(ctx, clog.FromContext(ctx).With("queue", "setup"))
	cleanupCtx := clog.WithLogger(ctx, clog.FromContext(ctx).With("queue", "cleanup"))

	// Create workqueues and reconcilers
	setupQueue := inmem.NewWorkQueue(10)
	cleanupQueue := inmem.NewWorkQueue(10)
	setupRec := reconciler.NewSetupReconciler(cfg)
	cleanupRec := reconciler.NewCleanupReconciler(cfg)

	st := loadCheckState()

	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	dispatchTicker := time.NewTicker(dispatchInterval)
	defer dispatchTicker.Stop()
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()

	// Session scan ticker
	sessionTicker := time.NewTicker(sessionScanInterval)
	defer sessionTicker.Stop()

	// Log rotation ticker — check once per hour
	rotateTicker := time.NewTicker(1 * time.Hour)
	defer rotateTicker.Stop()

	// Digest ticker — only active when digest_interval is configured
	var digestC <-chan time.Time
	if digestEnabled {
		digestTicker := time.NewTicker(digestInterval)
		defer digestTicker.Stop()
		digestC = digestTicker.C
	}

	// Initial poll and session scan
	pollOnce(ctx, st, setupQueue, setupRec)
	reconciler.ScanSessions(cfg, 10*time.Second)

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("[%s] Watch daemon stopping\n", time.Now().Format(time.RFC3339))
			os.Remove(pidFile())
			return nil

		case <-rotateTicker.C:
			rotateLogIfNeeded()

		case <-pollTicker.C:
			reloadConfig(setupRec, cleanupRec, pollTicker)
			pollOnce(ctx, st, setupQueue, setupRec)

		case <-dispatchTicker.C:
			if err := dispatcher.HandleAsync(setupCtx, setupQueue, concurrency, concurrency, setupRec.Reconcile, maxRetries)(); err != nil {
				fmt.Printf("[%s] Setup dispatch error: %v\n", time.Now().Format(time.RFC3339), err)
			}
			if err := dispatcher.HandleAsync(cleanupCtx, cleanupQueue, 1, 1, cleanupRec.Reconcile, 3)(); err != nil {
				fmt.Printf("[%s] Cleanup dispatch error: %v\n", time.Now().Format(time.RFC3339), err)
			}

		case <-sessionTicker.C:
			reconciler.ScanSessions(cfg, 10*time.Second)

		case <-cleanupTicker.C:
			reconciler.ScanMergedPRs(ctx, cfg, cleanupQueue, cfg.Watch.GetCleanupAfterDays())

		case <-digestC:
			reconciler.SendDigest(cfg)
		}
	}
}

// reloadConfig re-reads ~/.zen/config.yaml and updates the global cfg
// and reconcilers. If the poll interval changed, the ticker is reset.
func reloadConfig(setupRec *reconciler.SetupReconciler, cleanupRec *reconciler.CleanupReconciler, pollTicker *time.Ticker) {
	newCfg, err := config.Load()
	if err != nil {
		fmt.Printf("[%s] Config reload failed: %v\n", time.Now().Format(time.RFC3339), err)
		return
	}

	// Detect poll interval change
	oldInterval := 5 * time.Minute
	if cfg.PollInterval != "" {
		if d, err := time.ParseDuration(cfg.PollInterval); err == nil {
			oldInterval = d
		}
	}
	newInterval := 5 * time.Minute
	if newCfg.PollInterval != "" {
		if d, err := time.ParseDuration(newCfg.PollInterval); err == nil {
			newInterval = d
		}
	}

	if oldInterval != newInterval {
		pollTicker.Reset(newInterval)
		fmt.Printf("[%s] Config reloaded: poll_interval changed %s → %s\n",
			time.Now().Format(time.RFC3339), oldInterval, newInterval)
	}

	cfg = newCfg
	setupRec.SetConfig(newCfg)
	cleanupRec.SetConfig(newCfg)
}

func loadCheckState() *reconciler.PollMemory {
	data, err := os.ReadFile(lastCheckFile())
	if err != nil {
		m := reconciler.DecodePollMemory(nil)
		return &m
	}
	m := reconciler.DecodePollMemory(data)
	return &m
}

func saveState(st *reconciler.PollMemory, prCount int) {
	data, err := reconciler.EncodeCheckFile(*st, time.Now().UTC().Format(time.RFC3339), prCount)
	if err != nil {
		return
	}
	os.WriteFile(lastCheckFile(), data, 0o644)
}

func pollOnce(ctx context.Context, st *reconciler.PollMemory, queue workqueue.Interface, rec *reconciler.SetupReconciler) {
	reviews, err := ghpkg.GetReviewRequests(ctx, "", cfg.IgnoreDrafts)
	if err != nil {
		fmt.Printf("[%s] Error fetching reviews: %v\n", time.Now().Format(time.RFC3339), err)
		return
	}

	kept := 0
	for _, pr := range reviews {
		short, ok := cfg.ConfiguredRepo(pr.Repository.NameWithOwner)
		if !ok {
			continue
		}
		kept++
		key := reconciler.MakePRKey(short, pr.Number)
		if st.LegacySeen {
			// Absorb pre-#18 seen_prs: do not re-notify "new" for the current
			// inbox. SHA refresh still runs.
			st.NotifiedNew[key] = true
		}
		basePath := cfg.RepoBasePath(short)
		worktreePath := filepath.Join(basePath, fmt.Sprintf("%s-pr-%d", short, pr.Number))
		exists := false
		head := ""
		if _, err := os.Stat(worktreePath); err == nil {
			exists = true
			if h, herr := wt.HEAD(worktreePath); herr == nil {
				head = h
				st.AppliedSHAs[key] = h
			}
		}

		if reconciler.ShouldNotifyNew(st.NotifiedNew[key], exists) {
			fmt.Printf("[%s] New PR review request: #%d - %s (by %s)\n",
				time.Now().Format(time.RFC3339), pr.Number, pr.Title, pr.Author.Login)
			notify.PRReview(pr.Number, pr.Title, pr.Author.Login, short)
		}
		if !st.NotifiedNew[key] {
			st.NotifiedNew[key] = true
		}

		action := reconciler.DecideDaemon(reconciler.PollInput{
			InReviewSearch: true,
			IgnoreDrafts:   cfg.IgnoreDrafts,
			IsDraft:        pr.IsDraft,
			Closed:         pr.Closed,
			WorktreeExists: exists,
			AuthorInList:   cfg.IsAuthor(pr.Author.Login),
			WorktreeHEAD:   head,
			HeadOID:        pr.HeadRefOid,
		})
		switch action {
		case reconciler.PollSkip:
			continue
		case reconciler.PollRefresh:
			fmt.Printf("[%s] PR #%d head moved (%s → %s), queuing refresh\n",
				time.Now().Format(time.RFC3339), pr.Number, shortSHA(head), shortSHA(pr.HeadRefOid))
		}

		rec.StorePRData(key, pr)
		if err := queue.Queue(ctx, key, workqueue.Options{Priority: 1}); err != nil {
			fmt.Printf("[%s] Error queuing PR #%d: %v\n", time.Now().Format(time.RFC3339), pr.Number, err)
		} else {
			fmt.Printf("[%s] Queued PR #%d for %s (author: %s)\n",
				time.Now().Format(time.RFC3339), pr.Number, action, pr.Author.Login)
		}
	}

	st.LegacySeen = false
	saveState(st, kept)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	if sha == "" {
		return "?"
	}
	return sha
}

const maxLogSize = 10 * 1024 * 1024 // 10 MB

// rotateLogIfNeeded checks the log file size and rotates if it exceeds maxLogSize.
// Keeps one previous log as watch.log.1. Since the daemon's stdout/stderr point
// to the log file, we reopen and replace them after rotation.
func rotateLogIfNeeded() {
	lf := logFile()
	info, err := os.Stat(lf)
	if err != nil || info.Size() < maxLogSize {
		return
	}

	// Rotate: watch.log → watch.log.1 (overwrite previous backup)
	backup := lf + ".1"
	os.Remove(backup)
	if err := os.Rename(lf, backup); err != nil {
		fmt.Printf("[%s] Log rotation: rename failed: %v\n", time.Now().Format(time.RFC3339), err)
		return
	}

	// Reopen a fresh log file and redirect stdout/stderr
	f, err := os.OpenFile(lf, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Printf("[%s] Log rotation: reopen failed: %v\n", time.Now().Format(time.RFC3339), err)
		return
	}

	// Redirect stdout and stderr to the new log file
	os.Stdout = f
	os.Stderr = f

	fmt.Printf("[%s] Log rotated (previous log saved as watch.log.1)\n", time.Now().Format(time.RFC3339))
}
