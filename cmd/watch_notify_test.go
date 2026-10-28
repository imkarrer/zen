package cmd

import (
	"testing"

	"github.com/mgreau/zen/internal/config"
	ghpkg "github.com/mgreau/zen/internal/github"
	"github.com/mgreau/zen/internal/reconciler"
)

func reviewIn(full string, n int) ghpkg.ReviewRequest {
	return ghpkg.ReviewRequest{Number: n, Repository: ghpkg.RepoInfo{NameWithOwner: full}}
}

func TestUnconfiguredToNotify_skipsConfiguredRepos(t *testing.T) {
	c := &config.Config{Repos: map[string]config.RepoConfig{"flox": {FullName: "flox/flox"}}}
	st := reconciler.DecodePollMemory(nil)

	got := unconfiguredToNotify(c, []ghpkg.ReviewRequest{reviewIn("flox/flox", 1), reviewIn("flox/other", 2)}, &st)

	if len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("notified %v, want only flox/other#2", got)
	}
}

func TestUnconfiguredToNotify_notifiesOnce(t *testing.T) {
	c := &config.Config{}
	st := reconciler.DecodePollMemory(nil)
	reqs := []ghpkg.ReviewRequest{reviewIn("flox/other", 2)}

	if got := unconfiguredToNotify(c, reqs, &st); len(got) != 1 {
		t.Fatalf("first poll notified %d, want 1", len(got))
	}
	// The memory survives a daemon restart through the state file.
	data, err := reconciler.EncodeCheckFile(st, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := reconciler.DecodePollMemory(data)
	if got := unconfiguredToNotify(c, reqs, &reloaded); len(got) != 0 {
		t.Errorf("second poll notified %d, want 0", len(got))
	}
}

func TestUnconfiguredToNotify_legacyStateAbsorbsWithoutNotifying(t *testing.T) {
	st := reconciler.DecodePollMemory(nil)
	st.LegacySeen = true

	if got := unconfiguredToNotify(&config.Config{}, []ghpkg.ReviewRequest{reviewIn("flox/other", 2)}, &st); len(got) != 0 {
		t.Errorf("legacy state notified %d, want 0", len(got))
	}
	if !st.NotifiedNew[reconciler.MakePRKey("flox/other", 2)] {
		t.Error("legacy state did not record the request, so the next poll would notify it")
	}
}
