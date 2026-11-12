package reconciler

import (
	"testing"

	"github.com/mgreau/zen/internal/config"
	"github.com/mgreau/zen/internal/worktree"
)

func TestAlertWaiting(t *testing.T) {
	review := worktree.Worktree{Name: "repo-pr-5", Type: worktree.TypePRReview, PRNumber: 5}
	feature := worktree.Worktree{Name: "repo-my-feature", Type: worktree.TypeFeature}

	tests := []struct {
		mode string
		wt   worktree.Worktree
		want bool
	}{
		{config.WaitingAlertsAll, review, true},
		{config.WaitingAlertsAll, feature, true},
		{"", feature, true}, // unset in a hand-built config behaves as all
		{config.WaitingAlertsReviews, review, true},
		{config.WaitingAlertsReviews, feature, false},
		{config.WaitingAlertsOff, review, false},
		{config.WaitingAlertsOff, feature, false},
	}
	for _, tt := range tests {
		if got := alertWaiting(tt.mode, tt.wt); got != tt.want {
			t.Errorf("alertWaiting(%q, %s) = %v, want %v", tt.mode, tt.wt.Type, got, tt.want)
		}
	}
}
