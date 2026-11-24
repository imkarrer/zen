package github

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBuildAuthorPRsQuery(t *testing.T) {
	tests := []struct {
		name         string
		repo         string
		authors      []string
		ignoreDrafts bool
		want         string
	}{
		{
			name:    "one author",
			repo:    "flox/floxhub",
			authors: []string{"nicklewis"},
			want:    "is:pr is:open repo:flox/floxhub author:nicklewis",
		},
		{
			name:    "several authors are ORed by repeating author:",
			repo:    "flox/floxhub",
			authors: []string{"gilmishal", "nicklewis"},
			want:    "is:pr is:open repo:flox/floxhub author:gilmishal author:nicklewis",
		},
		{
			name:         "drafts excluded",
			repo:         "flox/floxhub",
			authors:      []string{"gilmishal"},
			ignoreDrafts: true,
			want:         "is:pr is:open repo:flox/floxhub author:gilmishal draft:false",
		},
		{
			name:    "blank entries dropped",
			repo:    "org/r",
			authors: []string{" a ", "", "  "},
			want:    "is:pr is:open repo:org/r author:a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildAuthorPRsQuery(tt.repo, tt.authors, tt.ignoreDrafts); got != tt.want {
				t.Errorf("buildAuthorPRsQuery() = %q, want %q", got, tt.want)
			}
		})
	}
}

// With no named author the search would be every open PR in the repo, so
// GetOpenPRsByAuthors must return nothing without querying GitHub. An
// expired context proves no query ran: one would fail with a timeout.
func TestGetOpenPRsByAuthors_noAuthorsSkipsTheSearch(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	for _, authors := range [][]string{nil, {}, {"", "  "}} {
		prs, err := GetOpenPRsByAuthors(ctx, "org/r", authors, false)
		if err != nil || prs != nil {
			t.Errorf("GetOpenPRsByAuthors(%q) = (%v, %v), want (nil, nil)", authors, prs, err)
		}
	}
	if prs, err := GetOpenPRsByAuthors(ctx, "", []string{"a"}, false); err != nil || prs != nil {
		t.Errorf("GetOpenPRsByAuthors with no repo = (%v, %v), want (nil, nil)", prs, err)
	}
}

func TestGetOpenPRsByAuthors_timeoutError(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := GetOpenPRsByAuthors(ctx, "org/r", []string{"a"}, false)
	if err == nil {
		t.Fatal("expected error from expired context")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error message, got: %s", err)
	}
}
