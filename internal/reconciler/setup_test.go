package reconciler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"chainguard.dev/driftlessaf/workqueue"
	"chainguard.dev/driftlessaf/workqueue/dispatcher"
	"chainguard.dev/driftlessaf/workqueue/inmem"
	"github.com/mgreau/zen/internal/config"
	ghpkg "github.com/mgreau/zen/internal/github"
	wt "github.com/mgreau/zen/internal/worktree"
)

func TestMakePRKey(t *testing.T) {
	tests := []struct {
		repo   string
		number int
		want   string
	}{
		{"mono", 31414, "mono:31414"},
		{"os", 1, "os:1"},
		{"infra-images", 999, "infra-images:999"},
	}
	for _, tt := range tests {
		got := MakePRKey(tt.repo, tt.number)
		if got != tt.want {
			t.Errorf("MakePRKey(%q, %d) = %q, want %q", tt.repo, tt.number, got, tt.want)
		}
	}
}

func TestParsePRKey(t *testing.T) {
	tests := []struct {
		key      string
		wantRepo string
		wantNum  int
		wantErr  bool
	}{
		{"mono:31414", "mono", 31414, false},
		{"os:1", "os", 1, false},
		{"infra-images:999", "infra-images", 999, false},
		{"invalid", "", 0, true},
		{"mono:abc", "", 0, true},
		{"", "", 0, true},
		{":123", "", 123, false},
	}
	for _, tt := range tests {
		repo, num, err := ParsePRKey(tt.key)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParsePRKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if repo != tt.wantRepo || num != tt.wantNum {
			t.Errorf("ParsePRKey(%q) = (%q, %d), want (%q, %d)", tt.key, repo, num, tt.wantRepo, tt.wantNum)
		}
	}
}

func TestReconcile_InvalidKey(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.RepoConfig{
		"mono": {FullName: "chainguard-dev/mono", BasePath: "/tmp/test"},
	}}
	rec := NewSetupReconciler(cfg)

	err := rec.Reconcile(context.Background(), "badkey", workqueue.Options{})
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
	if workqueue.GetNonRetriableDetails(err) == nil {
		t.Error("expected NonRetriableError for invalid key format")
	}
}

func TestReconcile_UnknownRepo(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.RepoConfig{
		"mono": {FullName: "chainguard-dev/mono", BasePath: "/tmp/test"},
	}}
	rec := NewSetupReconciler(cfg)

	// Store PR data so we pass the key parse step but fail on unknown repo
	rec.StorePRData("nonexistent:123", ghpkg.ReviewRequest{
		Number: 123,
		Title:  "Test PR",
		Author: ghpkg.AuthorInfo{Login: "testuser"},
	})

	err := rec.Reconcile(context.Background(), "nonexistent:123", workqueue.Options{})
	if err == nil {
		t.Fatal("expected error for unknown repo")
	}
	if workqueue.GetNonRetriableDetails(err) == nil {
		t.Error("expected NonRetriableError for unknown repo")
	}
}

func TestReconcile_MissingPRData(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.RepoConfig{
		"mono": {FullName: "chainguard-dev/mono", BasePath: "/tmp/test"},
	}}
	rec := NewSetupReconciler(cfg)

	err := rec.Reconcile(context.Background(), "mono:123", workqueue.Options{})
	if err == nil {
		t.Fatal("expected error for missing PR data")
	}
	if workqueue.GetNonRetriableDetails(err) == nil {
		t.Error("expected NonRetriableError for missing PR data")
	}
}

func TestDispatcherIntegration(t *testing.T) {
	queue := inmem.NewWorkQueue(10)
	ctx := context.Background()

	called := false
	callback := func(ctx context.Context, key string, opts workqueue.Options) error {
		if key != "test:1" {
			t.Errorf("unexpected key: %q", key)
		}
		called = true
		return nil
	}

	if err := queue.Queue(ctx, "test:1", workqueue.Options{Priority: 1}); err != nil {
		t.Fatalf("Queue() error: %v", err)
	}

	if err := dispatcher.HandleAsync(ctx, queue, 1, 1, callback, 3)(); err != nil {
		t.Fatalf("HandleAsync() error: %v", err)
	}

	if !called {
		t.Error("callback was not called")
	}
}

func testSetupCfg(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Repos: map[string]config.RepoConfig{
			"mono": {FullName: "chainguard-dev/mono", BasePath: t.TempDir()},
		},
	}
}

func TestReconcile_closedDoesNotTouchGit(t *testing.T) {
	cfg := testSetupCfg(t)
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		t.Fatal("closed snapshot must not call GitHub")
		return nil, nil
	}
	rec.StorePRData("mono:99", ghpkg.ReviewRequest{
		Number:     99,
		Title:      "closed after queue",
		Closed:     true,
		HeadRefOid: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err := rec.Reconcile(context.Background(), "mono:99", workqueue.Options{}); err != nil {
		t.Fatalf("closed PR must skip, not fail git: %v", err)
	}
}

func TestReconcile_silencedDraftDoesNotTouchGit(t *testing.T) {
	cfg := testSetupCfg(t)
	cfg.IgnoreDrafts = true
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		t.Fatal("silenced draft snapshot must not call GitHub")
		return nil, nil
	}
	rec.StorePRData("mono:7", ghpkg.ReviewRequest{
		Number:     7,
		Title:      "back to draft",
		IsDraft:    true,
		HeadRefOid: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err := rec.Reconcile(context.Background(), "mono:7", workqueue.Options{}); err != nil {
		t.Fatalf("silenced draft must skip, not fail git: %v", err)
	}
}

func TestSkipIfPRInactive_liveDraftRace(t *testing.T) {
	cfg := testSetupCfg(t)
	cfg.IgnoreDrafts = true
	rec := NewSetupReconciler(cfg)
	called := false
	rec.prDetails = func(_ context.Context, fullRepo string, n int) (*ghpkg.PRDetails, error) {
		called = true
		if fullRepo != "chainguard-dev/mono" || n != 7 {
			t.Fatalf("lookup %s #%d", fullRepo, n)
		}
		return &ghpkg.PRDetails{Draft: true, State: "open", HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, nil
	}
	pr := ghpkg.ReviewRequest{
		Number:     7,
		Title:      "queued while ready",
		IsDraft:    false,
		HeadRefOid: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 7, &pr)
	if err != nil {
		t.Fatal(err)
	}
	if !called || !skip {
		t.Fatalf("called=%v skip=%v; want skip after live draft", called, skip)
	}
}

func TestSkipIfPRInactive_liveClosedRace(t *testing.T) {
	cfg := testSetupCfg(t)
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return &ghpkg.PRDetails{State: "closed", HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, nil
	}
	pr := ghpkg.ReviewRequest{Number: 1, Title: "queued then closed"}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 1, &pr)
	if err != nil || !skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
}

func TestSkipIfPRInactive_liveMerged(t *testing.T) {
	cfg := testSetupCfg(t)
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return &ghpkg.PRDetails{State: "MERGED"}, nil
	}
	pr := ghpkg.ReviewRequest{Number: 1, Title: "merged"}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 1, &pr)
	if err != nil || !skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
}

func TestSkipIfPRInactive_deletedPR(t *testing.T) {
	cfg := testSetupCfg(t)
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return nil, fmt.Errorf("fetching PR #1: GET https://api.github.com/repos/o/r/pulls/1: 404 Not Found []")
	}
	pr := ghpkg.ReviewRequest{Number: 1, Title: "deleted"}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 1, &pr)
	if err != nil || !skip {
		t.Fatalf("404 must skip not retry: skip=%v err=%v", skip, err)
	}
}

func TestSkipIfPRInactive_networkRetries(t *testing.T) {
	cfg := testSetupCfg(t)
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return nil, fmt.Errorf("Get \"https://api.github.com/repos/o/r/pulls/1\": connection reset")
	}
	pr := ghpkg.ReviewRequest{Number: 1, Title: "flaky"}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 1, &pr)
	if skip || err == nil {
		t.Fatalf("network error must retry: skip=%v err=%v", skip, err)
	}
}

func TestSkipIfPRInactive_refreshesHeadSHA(t *testing.T) {
	cfg := testSetupCfg(t)
	rec := NewSetupReconciler(cfg)
	want := "cccccccccccccccccccccccccccccccccccccccc"
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return &ghpkg.PRDetails{State: "open", HeadSHA: want}, nil
	}
	pr := ghpkg.ReviewRequest{Number: 3, Title: "moved", HeadRefOid: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 3, &pr)
	if err != nil || skip {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
	if pr.HeadRefOid != want {
		t.Fatalf("HeadRefOid=%s want live SHA", pr.HeadRefOid)
	}
	stored, ok := rec.getPRData("mono:3")
	if !ok || stored.HeadRefOid != want {
		t.Fatalf("stored SHA not updated: %+v", stored)
	}
}

func TestSkipIfPRInactive_draftStillSyncedWhenShown(t *testing.T) {
	cfg := testSetupCfg(t)
	cfg.IgnoreDrafts = false
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return &ghpkg.PRDetails{Draft: true, State: "open", HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, nil
	}
	pr := ghpkg.ReviewRequest{Number: 4, Title: "visible draft", IsDraft: true}
	skip, err := rec.skipIfPRInactive(context.Background(), "chainguard-dev/mono", "mono", 4, &pr)
	if err != nil || skip {
		t.Fatalf("shown drafts must still sync: skip=%v err=%v", skip, err)
	}
}

func TestReconcile_liveDraftRaceDoesNotTouchGit(t *testing.T) {
	cfg := testSetupCfg(t)
	cfg.IgnoreDrafts = true
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return &ghpkg.PRDetails{Draft: true, State: "open"}, nil
	}
	rec.StorePRData("mono:8", ghpkg.ReviewRequest{
		Number:     8,
		Title:      "ready when queued",
		IsDraft:    false,
		HeadRefOid: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err := rec.Reconcile(context.Background(), "mono:8", workqueue.Options{}); err != nil {
		t.Fatalf("open→draft race must skip, not fail git: %v", err)
	}
}

// prOriginFixture builds a clone at basePath/testrepo whose origin carries
// refs/pull/5/head, so Reconcile can create a review worktree offline.
// PATH is narrowed to git alone so the "worktree ready" notification fails
// quietly instead of reaching the desktop.
func prOriginFixture(t *testing.T) (cfg *config.Config, head string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	upstream := t.TempDir()
	run(upstream, "init", "-b", "main")
	run(upstream, "config", "user.email", "test@example.com")
	run(upstream, "config", "user.name", "Test")
	run(upstream, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(upstream, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(upstream, "add", "f")
	run(upstream, "commit", "-m", "init")
	head = run(upstream, "rev-parse", "HEAD")
	run(upstream, "update-ref", "refs/pull/5/head", head)

	basePath := t.TempDir()
	run(basePath, "clone", "-q", upstream, filepath.Join(basePath, "testrepo"))

	binDir := t.TempDir()
	if err := os.Symlink(gitBin, filepath.Join(binDir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	cfg = &config.Config{Repos: map[string]config.RepoConfig{
		"testrepo": {FullName: "test/testrepo", BasePath: basePath},
	}}
	return cfg, head
}

func TestReconcile_runsOnCreatedOnlyForNewWorktrees(t *testing.T) {
	cfg, head := prOriginFixture(t)
	rec := NewSetupReconciler(cfg)
	rec.prDetails = func(context.Context, string, int) (*ghpkg.PRDetails, error) {
		return &ghpkg.PRDetails{State: "open", HeadSHA: head}, nil
	}
	rec.renderContext = func(context.Context, string, int) (string, error) { return "context", nil }
	var got []string
	rec.OnCreated(func(p string) {
		if _, err := os.Stat(filepath.Join(p, "f")); err != nil {
			t.Errorf("OnCreated ran before the worktree was checked out: %v", err)
		}
		got = append(got, p)
	})
	rec.StorePRData("testrepo:5", ghpkg.ReviewRequest{Number: 5, Title: "t", HeadRefOid: head})

	for i := 0; i < 2; i++ {
		if err := rec.Reconcile(context.Background(), "testrepo:5", workqueue.Options{}); err != nil {
			t.Fatalf("Reconcile #%d: %v", i+1, err)
		}
	}
	want := wt.PRPath(cfg.RepoBasePath("testrepo"), "testrepo", 5)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("OnCreated calls = %q, want one for %q", got, want)
	}
}
