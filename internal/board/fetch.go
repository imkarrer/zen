package board

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mgreau/zen/internal/config"
	"github.com/mgreau/zen/internal/github"
	"github.com/mgreau/zen/internal/ui"
	"golang.org/x/sync/errgroup"
)

// fetchTimeout bounds a single board refresh across all configured repos.
const fetchTimeout = 25 * time.Second

// maxConcurrentFileChecks caps concurrent "which files does this PR touch"
// lookups used to detect watch_paths membership for Needs Your Review.
const maxConcurrentFileChecks = 5

// Review tiers, in display priority order (lower sorts first).
const (
	reviewTierAuthor    = 0 // PR author is in the `authors` config
	reviewTierWatchPath = 1 // PR touches a configured watch_paths entry
	reviewTierOther     = 2 // everything else
)

// MyPRRow is one row of the "My Pull Requests" table.
type MyPRRow struct {
	Status  Status
	Number  int
	Repo    string // config short name, or owner/repo when unconfigured
	Title   string
	URL     string
	Age     string
	ageSecs int
	BaseRef string
	HeadRef string
	// Depth is 0 for an independent PR or the root of a dependency chain,
	// and increases by 1 per level for PRs stacked on top of another.
	Depth int
}

// ReviewRow is one row of the "Needs Your Review" table.
type ReviewRow struct {
	Kind    string // github.ReviewKindNew or github.ReviewKindRereview
	Number  int
	Repo    string // config short name, or owner/repo when unconfigured
	Title   string
	Author  string
	URL     string
	Age     string
	ageSecs int
	tier    int
	Draft   bool
}

// ageOf formats how long ago an RFC3339 timestamp was, using the same
// human-readable units as the rest of zen. Returns ("", 0) if createdAt
// can't be parsed.
func ageOf(createdAt string) (string, int) {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return "", 0
	}
	secs := int(time.Since(t).Seconds())
	if secs < 0 {
		secs = 0
	}
	return ui.FormatDuration(secs), secs
}

// FetchMyPRs fetches the user's own open PRs in every repository, not just
// the configured ones: a PR opened somewhere zen has never been told about is
// still the user's to track. One unscoped search covers them all, so the cost
// of a refresh does not grow with the repo list. Each PR is classified into a
// status bucket and ordered by dependency chain (a PR immediately followed by
// anything stacked on top of it), with chains and independent PRs ranked by
// status priority then age (oldest first).
func FetchMyPRs(ctx context.Context, cfg *config.Config) ([]MyPRRow, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	prs, err := github.GetMyOpenPRs(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("fetching your PRs: %w", err)
	}
	return orderWithDependencies(myPRRows(cfg, prs)), nil
}

// myPRRows turns search results into table rows. A PR in a configured repo is
// labelled with its config short name; one anywhere else keeps its
// owner/repo, which RepoFullName passes through unchanged, so the summary
// panel and dependency grouping work for it too.
func myPRRows(cfg *config.Config, prs []github.MyPR) []MyPRRow {
	rows := make([]MyPRRow, 0, len(prs))
	for _, pr := range prs {
		repo := repoLabel(cfg, pr.Repository.NameWithOwner)
		age, ageSecs := ageOf(pr.CreatedAt)
		rows = append(rows, MyPRRow{
			Status:  ClassifyMyPR(pr),
			Number:  pr.Number,
			Repo:    repo,
			Title:   pr.Title,
			URL:     pr.URL,
			Age:     age,
			ageSecs: ageSecs,
			BaseRef: pr.BaseRefName,
			HeadRef: pr.HeadRefName,
		})
	}
	return rows
}

// repoLabel is how a row names its repository: the config short name when
// the repo is configured, otherwise its owner/repo, which RepoFullName
// passes through unchanged.
func repoLabel(cfg *config.Config, full string) string {
	if short, ok := cfg.ConfiguredRepo(full); ok {
		return short
	}
	return full
}

// reviewRows turns review-request search results into table rows, labelled
// the same way as My Pull Requests.
func reviewRows(cfg *config.Config, reqs []github.ReviewRequest) []ReviewRow {
	rows := make([]ReviewRow, 0, len(reqs))
	for _, rr := range reqs {
		age, ageSecs := ageOf(rr.CreatedAt)
		rows = append(rows, ReviewRow{
			Kind:    rr.Kind,
			Number:  rr.Number,
			Repo:    repoLabel(cfg, rr.Repository.NameWithOwner),
			Title:   rr.Title,
			Author:  rr.Author.Login,
			URL:     rr.URL,
			Age:     age,
			ageSecs: ageSecs,
			Draft:   rr.IsDraft,
		})
	}
	return rows
}

// FetchReviewRequests fetches PRs the user has been asked to review in every
// repository, configured or not, unfiltered by the `authors` config — a
// "don't miss anything" dashboard shouldn't hide a review request just
// because the author isn't in the curated inbox list or the repo isn't in
// `repos:`. One unscoped search covers them all. Sorted into three tiers —
// configured authors, PRs touching a configured watch_paths entry, then
// everyone else — and by age (most recent first) within each tier.
func FetchReviewRequests(ctx context.Context, cfg *config.Config) ([]ReviewRow, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	reqs, err := github.GetReviewRequests(ctx, "", false)
	if err != nil {
		return nil, fmt.Errorf("fetching review requests: %w", err)
	}
	all := reviewRows(cfg, reqs)

	assignReviewTiers(ctx, cfg, all)

	sort.Slice(all, func(i, j int) bool {
		if all[i].tier != all[j].tier {
			return all[i].tier < all[j].tier
		}
		return all[i].ageSecs < all[j].ageSecs
	})
	return all, nil
}

// assignReviewTiers sets tier on each row in place: reviewTierAuthor when
// the PR's author is in the `authors` config, reviewTierWatchPath when it
// touches a configured watch_paths entry, reviewTierOther otherwise.
//
// Checking watch_paths membership requires fetching each candidate PR's
// changed files — skipped entirely when no watch_paths are configured, and
// only performed for rows not already reviewTierAuthor. A failure fetching
// files or building the GitHub client leaves the affected rows at
// reviewTierOther rather than failing the whole refresh.
func assignReviewTiers(ctx context.Context, cfg *config.Config, rows []ReviewRow) {
	var needsFileCheck []int
	for i := range rows {
		if cfg.IsAuthor(rows[i].Author) {
			rows[i].tier = reviewTierAuthor
			continue
		}
		rows[i].tier = reviewTierOther
		if len(cfg.WatchPaths) > 0 {
			needsFileCheck = append(needsFileCheck, i)
		}
	}
	if len(needsFileCheck) == 0 {
		return
	}

	client, err := github.NewClient(ctx)
	if err != nil {
		return
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentFileChecks)
	for _, i := range needsFileCheck {
		g.Go(func() error {
			full := cfg.RepoFullName(rows[i].Repo)
			files, err := client.GetPRFiles(gctx, full, rows[i].Number)
			if err != nil {
				return nil
			}
			if touchesWatchPaths(files, cfg.WatchPaths) {
				rows[i].tier = reviewTierWatchPath
			}
			return nil
		})
	}
	_ = g.Wait()
}

// touchesWatchPaths reports whether any file touches a configured watch
// path, matching the same prefix rule as `zen inbox`'s watch_paths scan.
func touchesWatchPaths(files, watchPaths []string) bool {
	for _, f := range files {
		for _, wp := range watchPaths {
			if strings.HasPrefix(f, wp+"/") || strings.HasPrefix(f, wp) {
				return true
			}
		}
	}
	return false
}

// FetchWatchedPRs fetches open PRs opened by each configured repo's
// watch_authors, one search per watched repo, newest first. A repo whose
// search fails is skipped and its error returned alongside the rows from the
// repos that succeeded, so one bad repo does not blank the table.
func FetchWatchedPRs(ctx context.Context, cfg *config.Config) ([]ReviewRow, error) {
	watched := cfg.WatchedRepos()
	if len(watched) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	var all []github.ReviewRequest
	var errs []error
	for _, w := range watched {
		prs, err := github.GetOpenPRsByAuthors(ctx, w.FullName, w.Authors, cfg.IgnoreDrafts)
		if err != nil {
			errs = append(errs, fmt.Errorf("watched PRs in %s: %w", w.FullName, err))
			continue
		}
		all = append(all, prs...)
	}
	rows := reviewRows(cfg, all)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ageSecs < rows[j].ageSecs })
	return rows, errors.Join(errs...)
}

// withoutReviewRequests drops watched rows that already appear in Needs Your
// Review, so a PR that asks for your review is listed once, where `v` and
// the review tiers apply to it.
func withoutReviewRequests(watched, reviews []ReviewRow) []ReviewRow {
	if len(watched) == 0 || len(reviews) == 0 {
		return watched
	}
	inReview := make(map[string]bool, len(reviews))
	for _, r := range reviews {
		inReview[r.URL] = true
	}
	out := make([]ReviewRow, 0, len(watched))
	for _, w := range watched {
		if !inReview[w.URL] {
			out = append(out, w)
		}
	}
	return out
}
