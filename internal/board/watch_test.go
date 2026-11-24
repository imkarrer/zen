package board

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mgreau/zen/internal/config"
)

func watchedConfig() *config.Config {
	return &config.Config{Repos: map[string]config.RepoConfig{
		"floxhub": {FullName: "flox/floxhub", BasePath: "/src", WatchAuthors: []string{"gilmishal", "nicklewis"}},
	}}
}

func TestSplitHeights_threeTables(t *testing.T) {
	tests := []struct {
		name   string
		avail  int
		counts []int
		want   []int
	}{
		{"all short: minimums, leftover unused", 40, []int{1, 2, 3}, []int{4, 4, 4}},
		{"leftover goes to the one long table", 40, []int{5, 5, 60}, []int{5, 5, 30}},
		{"leftover round-robins between long tables", 40, []int{100, 2, 100}, []int{18, 4, 18}},
		{"wants exceed avail: proportional, last takes remainder", 15, []int{30, 30, 30}, []int{5, 5, 5}},
		{"all empty and squeezed", 9, []int{0, 0, 0}, []int{4, 4, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitHeights(tt.avail, tt.counts)
			if len(got) != len(tt.want) {
				t.Fatalf("splitHeights(%d, %v) = %v, want %v", tt.avail, tt.counts, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("splitHeights(%d, %v) = %v, want %v", tt.avail, tt.counts, got, tt.want)
				}
			}
		})
	}
}

func TestWithoutReviewRequests(t *testing.T) {
	watched := []ReviewRow{{Number: 1, URL: "u1"}, {Number: 2, URL: "u2"}, {Number: 3, URL: "u3"}}
	reviews := []ReviewRow{{Number: 2, URL: "u2"}}

	got := withoutReviewRequests(watched, reviews)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 3 {
		t.Errorf("withoutReviewRequests = %+v, want #1 and #3", got)
	}
	if got := withoutReviewRequests(watched, nil); len(got) != 3 {
		t.Errorf("with no review requests, got %d rows, want all 3", len(got))
	}
}

func TestFetchWatchedPRs_noWatchedReposMakesNoRequest(t *testing.T) {
	// An expired context would make any GitHub request fail.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := FetchWatchedPRs(ctx, &config.Config{Repos: map[string]config.RepoConfig{"r": {FullName: "org/r"}}})
	if rows != nil || err != nil {
		t.Errorf("FetchWatchedPRs = (%v, %v), want (nil, nil)", rows, err)
	}
}

func TestToggleFocus_skipsWatchTableWhenNoneConfigured(t *testing.T) {
	m := NewModel(&config.Config{})
	m.toggleFocus()
	m.toggleFocus()
	if m.focus != FocusMyPRs {
		t.Errorf("after two tabs focus = %v, want My PRs", m.focus)
	}
}

func TestToggleFocus_cyclesThroughWatchTable(t *testing.T) {
	m := NewModel(watchedConfig())
	var seen []Focus
	for range 3 {
		m.toggleFocus()
		seen = append(seen, m.focus)
	}
	want := []Focus{FocusReview, FocusWatch, FocusMyPRs}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("focus sequence = %v, want %v", seen, want)
		}
	}
	if !m.myTable.Focused() || m.watchTable.Focused() || m.reviewTable.Focused() {
		t.Error("only the focused table should be marked focused")
	}
}

func TestView_watchSectionOnlyWhenConfigured(t *testing.T) {
	if v := NewModel(&config.Config{}).View(); strings.Contains(v, "WATCHED AUTHORS") {
		t.Error("Watched Authors section shown with no watch_authors configured")
	}
	if v := NewModel(watchedConfig()).View(); !strings.Contains(v, "WATCHED AUTHORS (0)") {
		t.Error("Watched Authors section missing with watch_authors configured")
	}
}

func TestWatchTable_selectionAndReviewKey(t *testing.T) {
	m := NewModel(watchedConfig())
	m.watchRows = []ReviewRow{{Repo: "floxhub", Number: 42, URL: "https://github.com/flox/floxhub/pull/42", Author: "nicklewis", Draft: true}}
	m.reviewRows = []ReviewRow{{Repo: "floxhub", Number: 7, URL: "https://github.com/flox/floxhub/pull/7"}}
	m.resize()
	m.focus = FocusWatch

	if got := m.selectedURL(); got != "https://github.com/flox/floxhub/pull/42" {
		t.Errorf("selectedURL = %q, want the watched PR", got)
	}
	if repo, n, ok := m.selectedRepoAndNumber(); !ok || repo != "floxhub" || n != 42 {
		t.Errorf("selectedRepoAndNumber = (%q, %d, %v), want (floxhub, 42, true)", repo, n, ok)
	}
	if !strings.Contains(m.View(), "📝 Draft") {
		t.Error("draft watched PR not labelled Draft")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if cmd == nil {
		t.Fatal("v on a watched PR in a configured repo did not start a review")
	}
	if got := next.(Model).statusMsg; got != "Opening review for #42..." {
		t.Errorf("statusMsg = %q, want it to open #42", got)
	}
}

func TestDataMsg_dropsWatchedPRsAlreadyInReview(t *testing.T) {
	m := NewModel(watchedConfig())
	next, _ := m.Update(dataMsg{
		reviewRows: []ReviewRow{{Number: 7, URL: "u7"}},
		watchRows:  []ReviewRow{{Number: 7, URL: "u7"}, {Number: 8, URL: "u8"}},
	})
	got := next.(Model).watchRows
	if len(got) != 1 || got[0].Number != 8 {
		t.Errorf("watchRows = %+v, want only #8", got)
	}
}
