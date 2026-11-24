package board

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mgreau/zen/internal/config"
	"github.com/mgreau/zen/internal/github"
)

// refreshInterval is how often the board refetches data on its own.
const refreshInterval = 30 * time.Second

// Focus identifies which table has keyboard focus.
type Focus int

const (
	FocusMyPRs Focus = iota
	FocusReview
	// FocusWatch is the Watched Authors table, shown only when some repo
	// has watch_authors configured.
	FocusWatch
)

// Column sizing. Each column also costs 2 chars of padding (1 per side,
// see table.DefaultStyles) that must be accounted for separately.
const (
	statusColWidthMy     = 20
	statusColWidthReview = 14
	prColWidth           = 8
	repoColWidth         = 12
	authorColWidth       = 14
	ageColWidth          = 5
	colPad               = 2
	minTitleWidth        = 12
	minTableHeight       = 4
	// maxTableHeight caps how tall either table grows for a short list, so a
	// handful of PRs doesn't claim the whole screen when the other table is
	// long.
	maxTableHeight = 15
	// Lines consumed by everything in View() other than the two tables:
	// title/status, blank, 2 section headers, 2 blanks, status/error line,
	// footer.
	chromeLines = 8
	// watchChromeLines is what the Watched Authors table adds besides its
	// rows: its section header and the blank line after it.
	watchChromeLines = 2
)

// Model is the zen board bubbletea model: live tables of the user's own open
// PRs grouped by status, PRs they've been asked to review, and — when some
// repo has watch_authors — open PRs from those authors.
type Model struct {
	cfg *config.Config

	myTable     table.Model
	reviewTable table.Model
	watchTable  table.Model
	myRows      []MyPRRow
	reviewRows  []ReviewRow
	watchRows   []ReviewRow
	// showWatch is whether any repo has watch_authors, fixed at NewModel.
	showWatch bool

	focus       Focus
	loading     bool
	err         error
	statusMsg   string
	lastUpdated time.Time
	width       int
	height      int
	quitting    bool

	summaryOpen    bool
	summaryLoading bool
	summaryLines   []string
	summaryErr     error
	summaryTitle   string
}

// NewModel builds the initial board model for cfg. Data is fetched once
// Init()'s command runs; until then the tables render empty.
func NewModel(cfg *config.Config) Model {
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Foreground(cyan).Bold(true)
	styles.Selected = styles.Selected.Foreground(background).Background(cyan).Bold(true)

	myTable := table.New(
		table.WithColumns(myColumns(defaultWidth, repoColWidth)),
		table.WithFocused(true),
		table.WithHeight(defaultTableHeight),
	)
	myTable.SetStyles(styles)

	reviewTable := table.New(
		table.WithColumns(reviewColumns(defaultWidth, repoColWidth)),
		table.WithHeight(defaultTableHeight),
	)
	reviewTable.SetStyles(styles)

	watchTable := table.New(
		table.WithColumns(reviewColumns(defaultWidth, repoColWidth)),
		table.WithHeight(defaultTableHeight),
	)
	watchTable.SetStyles(styles)

	m := Model{
		cfg:         cfg,
		myTable:     myTable,
		reviewTable: reviewTable,
		watchTable:  watchTable,
		showWatch:   len(cfg.WatchedRepos()) > 0,
		focus:       FocusMyPRs,
		loading:     true,
		width:       defaultWidth,
		height:      defaultHeight,
	}
	m.myTable.SetWidth(defaultWidth)
	m.reviewTable.SetWidth(defaultWidth)
	m.watchTable.SetWidth(defaultWidth)
	return m
}

const (
	defaultWidth       = 100
	defaultHeight      = 30
	defaultTableHeight = 8
)

// dataMsg carries the result of a fetch.
type dataMsg struct {
	myRows     []MyPRRow
	reviewRows []ReviewRow
	watchRows  []ReviewRow
	err        error
}

// tickMsg fires the periodic auto-refresh.
type tickMsg time.Time

// actionResultMsg carries feedback from an 'o'/'v' keypress.
type actionResultMsg struct {
	msg string
	err error
}

func fetchCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var myRows []MyPRRow
		var reviewRows, watchRows []ReviewRow
		var err1, err2, err3 error

		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			myRows, err1 = FetchMyPRs(ctx, cfg)
		}()
		go func() {
			defer wg.Done()
			reviewRows, err2 = FetchReviewRequests(ctx, cfg)
		}()
		go func() {
			defer wg.Done()
			watchRows, err3 = FetchWatchedPRs(ctx, cfg)
		}()
		wg.Wait()

		err := err1
		if err == nil {
			err = err2
		}
		if err == nil {
			err = err3
		}
		return dataMsg{myRows: myRows, reviewRows: reviewRows, watchRows: watchRows, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func openCmd(url string) tea.Cmd {
	return func() tea.Msg {
		if url == "" {
			return actionResultMsg{err: fmt.Errorf("selected row has no URL")}
		}
		if err := openURL(url); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{msg: "Opened in browser"}
	}
}

func launchReviewCmd(repo string, number int) tea.Cmd {
	return func() tea.Msg {
		if err := launchReview(repo, number); err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{msg: fmt.Sprintf("Opened review for #%d", number)}
	}
}

// Init starts the first fetch and the auto-refresh ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchCmd(m.cfg), tickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case dataMsg:
		m.loading = false
		m.lastUpdated = time.Now()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.myRows = msg.myRows
		m.reviewRows = msg.reviewRows
		m.watchRows = withoutReviewRequests(msg.watchRows, msg.reviewRows)
		// Row counts just changed, so table heights (computed from row
		// counts in resize) need recomputing too, not just cell content.
		m.resize()
		return m, nil

	case tickMsg:
		m.loading = true
		return m, tea.Batch(fetchCmd(m.cfg), tickCmd())

	case actionResultMsg:
		if msg.err != nil {
			m.statusMsg = "Error: " + msg.err.Error()
		} else {
			m.statusMsg = msg.msg
		}
		return m, nil

	case summaryMsg:
		m.summaryLoading = false
		m.summaryErr = msg.err
		m.summaryLines = msg.lines
		m.resize()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "esc":
			if m.summaryOpen {
				m.closeSummary()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "tab":
			m.toggleFocus()
			return m, nil
		case "r":
			m.loading = true
			m.statusMsg = ""
			m.err = nil
			return m, fetchCmd(m.cfg)
		case "enter", "o":
			m.statusMsg = ""
			return m, openCmd(m.selectedURL())
		case "v":
			if m.focus == FocusReview || m.focus == FocusWatch {
				if row, ok := m.selectedReviewRow(); ok {
					// A review worktree is made from the repo's local clone,
					// which zen knows only for a configured repo.
					if m.cfg.RepoBasePath(row.Repo) == "" {
						m.statusMsg = fmt.Sprintf("%s is not in repos: in ~/.zen/config.yaml; add it to review #%d here (enter opens it in the browser)", row.Repo, row.Number)
						return m, nil
					}
					m.statusMsg = fmt.Sprintf("Opening review for #%d...", row.Number)
					return m, launchReviewCmd(row.Repo, row.Number)
				}
			}
			return m, nil
		case "s":
			if m.summaryOpen {
				m.closeSummary()
				return m, nil
			}
			repo, number, ok := m.selectedRepoAndNumber()
			if !ok {
				return m, nil
			}
			m.summaryOpen = true
			m.summaryLoading = true
			m.summaryErr = nil
			m.summaryLines = nil
			m.summaryTitle = fmt.Sprintf("#%d", number)
			m.resize()
			return m, summaryCmd(m.cfg.RepoFullName(repo), number)
		}
	}

	var cmd tea.Cmd
	t := m.focusedTable()
	*t, cmd = t.Update(msg)
	return m, cmd
}

// focusedTable is the table that currently has keyboard focus.
func (m *Model) focusedTable() *table.Model {
	switch m.focus {
	case FocusReview:
		return &m.reviewTable
	case FocusWatch:
		return &m.watchTable
	default:
		return &m.myTable
	}
}

// toggleFocus moves focus to the next table shown, wrapping around; the
// Watched Authors table is skipped when no repo has watch_authors.
func (m *Model) toggleFocus() {
	m.focusedTable().Blur()
	switch m.focus {
	case FocusMyPRs:
		m.focus = FocusReview
	case FocusReview:
		if m.showWatch {
			m.focus = FocusWatch
		} else {
			m.focus = FocusMyPRs
		}
	default:
		m.focus = FocusMyPRs
	}
	m.focusedTable().Focus()
}

// closeSummary hides the summary panel and gives its reserved lines back to
// the tables.
func (m *Model) closeSummary() {
	m.summaryOpen = false
	m.summaryLoading = false
	m.summaryErr = nil
	m.summaryLines = nil
	m.resize()
}

// selectedRepoAndNumber returns the short repo name and PR number of
// whichever row is currently selected in the focused table.
func (m Model) selectedRepoAndNumber() (repo string, number int, ok bool) {
	if m.focus == FocusMyPRs {
		if row, ok := m.selectedMyRow(); ok {
			return row.Repo, row.Number, true
		}
		return "", 0, false
	}
	if row, ok := m.selectedReviewRow(); ok {
		return row.Repo, row.Number, true
	}
	return "", 0, false
}

func (m Model) selectedURL() string {
	if m.focus == FocusMyPRs {
		if row, ok := m.selectedMyRow(); ok {
			return row.URL
		}
		return ""
	}
	if row, ok := m.selectedReviewRow(); ok {
		return row.URL
	}
	return ""
}

func (m Model) selectedMyRow() (MyPRRow, bool) {
	i := m.myTable.Cursor()
	if i < 0 || i >= len(m.myRows) {
		return MyPRRow{}, false
	}
	return m.myRows[i], true
}

// selectedReviewRow returns the selected row of whichever review-shaped
// table has focus: Needs Your Review, or Watched Authors.
func (m Model) selectedReviewRow() (ReviewRow, bool) {
	t, rows := m.reviewTable, m.reviewRows
	if m.focus == FocusWatch {
		t, rows = m.watchTable, m.watchRows
	}
	i := t.Cursor()
	if i < 0 || i >= len(rows) {
		return ReviewRow{}, false
	}
	return rows[i], true
}

// resize recomputes column widths and table heights for the current
// terminal size, then rebuilds table content so truncation matches.
func (m *Model) resize() {
	counts := []int{len(m.myRows), len(m.reviewRows)}
	chrome := chromeLines + m.summaryChromeLines()
	if m.showWatch {
		counts = append(counts, len(m.watchRows))
		chrome += watchChromeLines
	}
	// Each table also draws a header line, which bubbles/table counts in
	// SetHeight; splitHeights works in row lines, so add it back per table.
	const headerLines = 1
	chrome += headerLines * len(counts)
	avail := m.height - chrome
	if avail < len(counts)*minTableHeight {
		avail = len(counts) * minTableHeight
	}
	heights := splitHeights(avail, counts)

	m.myTable.SetHeight(heights[0] + headerLines)
	m.reviewTable.SetHeight(heights[1] + headerLines)
	m.myTable.SetColumns(myColumns(m.width, myRepoColWidth(m.myRows)))
	m.reviewTable.SetColumns(reviewColumns(m.width, reviewRepoColWidth(m.reviewRows)))
	m.myTable.SetWidth(m.width)
	m.reviewTable.SetWidth(m.width)
	if m.showWatch {
		m.watchTable.SetHeight(heights[2] + headerLines)
		m.watchTable.SetColumns(reviewColumns(m.width, reviewRepoColWidth(m.watchRows)))
		m.watchTable.SetWidth(m.width)
	}

	m.refreshTableContent()
}

// summaryChromeLines returns how many lines the summary panel currently
// reserves: 0 when closed, otherwise its border + title + content, plus a
// blank separator line before the footer.
func (m Model) summaryChromeLines() int {
	if !m.summaryOpen {
		return 0
	}
	return m.panelContentLineCount() + 3 + 1 // content + title + 2 border lines + separator
}

// panelContentLineCount is how many lines of actual content the summary
// panel is showing right now (a single status line while loading or on
// error, otherwise the fetched description lines, capped at
// summaryMaxLines by summaryCmd).
func (m Model) panelContentLineCount() int {
	if m.summaryLoading || m.summaryErr != nil || len(m.summaryLines) == 0 {
		return 1
	}
	return len(m.summaryLines)
}

// splitTableHeights is splitHeights for the two-table layout.
func splitTableHeights(avail, myCount, reviewCount int) (myHeight, reviewHeight int) {
	h := splitHeights(avail, []int{myCount, reviewCount})
	return h[0], h[1]
}

// splitHeights allocates avail lines among tables based on how many rows
// each actually has, so a short list doesn't reserve dead space while a long
// list is starved for visible rows. Each table gets at least minTableHeight
// and at most maxTableHeight from its own row count; any leftover is handed
// out one line at a time, round-robin, to tables that still have rows hidden
// below their current allocation. If none has hidden rows, the leftover goes
// unused rather than stretching an already-fully-shown table. When even the
// clamped wants exceed avail, lines are split in proportion to row counts,
// the last table taking the remainder.
func splitHeights(avail int, counts []int) []int {
	n := len(counts)
	heights := make([]int, n)
	if n == 0 {
		return heights
	}

	wantTotal, total := 0, 0
	for i, c := range counts {
		heights[i] = clampHeight(c)
		wantTotal += heights[i]
		total += c
	}

	if wantTotal > avail {
		used := 0
		for i, c := range counts {
			if i == n-1 {
				heights[i] = max(avail-used, minTableHeight)
				break
			}
			if total == 0 {
				heights[i] = max(avail/n, minTableHeight)
			} else {
				heights[i] = max(avail*c/total, minTableHeight)
			}
			used += heights[i]
		}
		return heights
	}

	leftover := avail - wantTotal
	hidden := make([]int, n)
	anyHidden := false
	for i, c := range counts {
		hidden[i] = max(c-heights[i], 0)
		anyHidden = anyHidden || hidden[i] > 0
	}
	for leftover > 0 && anyHidden {
		anyHidden = false
		for i := range heights {
			if leftover == 0 {
				break
			}
			if hidden[i] > 0 {
				heights[i]++
				hidden[i]--
				leftover--
			}
			anyHidden = anyHidden || hidden[i] > 0
		}
	}
	return heights
}

// clampHeight bounds a row count to [minTableHeight, maxTableHeight].
func clampHeight(rowCount int) int {
	return min(max(rowCount, minTableHeight), maxTableHeight)
}

// maxRepoColWidth caps how far a table widens its REPO column for
// owner/repo labels (see repoColWidthFor), so titles keep their room.
const maxRepoColWidth = 24

// repoColWidthFor fits a REPO column to the longest of labels, between
// repoColWidth and maxRepoColWidth. A PR outside the configured repos is
// labelled by its owner/repo, which the fixed width would truncate to an
// owner prefix that cannot tell two of that owner's repos apart.
func repoColWidthFor(labels []string) int {
	w := repoColWidth
	for _, l := range labels {
		w = max(w, utf8.RuneCountInString(l))
	}
	return min(w, maxRepoColWidth)
}

// myRepoColWidth is repoColWidthFor over My Pull Requests' rows.
func myRepoColWidth(rows []MyPRRow) int {
	labels := make([]string, len(rows))
	for i, r := range rows {
		labels[i] = r.Repo
	}
	return repoColWidthFor(labels)
}

// reviewRepoColWidth is repoColWidthFor over Needs Your Review's rows.
func reviewRepoColWidth(rows []ReviewRow) int {
	labels := make([]string, len(rows))
	for i, r := range rows {
		labels[i] = r.Repo
	}
	return repoColWidthFor(labels)
}

func myColumns(totalWidth, repoWidth int) []table.Column {
	fixed := (statusColWidthMy + colPad) + (prColWidth + colPad) + (repoWidth + colPad) + (ageColWidth + colPad)
	title := totalWidth - fixed
	if title < minTitleWidth {
		title = minTitleWidth
	}
	return []table.Column{
		{Title: "STATUS", Width: statusColWidthMy},
		{Title: "PR", Width: prColWidth},
		{Title: "REPO", Width: repoWidth},
		{Title: "TITLE", Width: title},
		{Title: "AGE", Width: ageColWidth},
	}
}

func reviewColumns(totalWidth, repoWidth int) []table.Column {
	fixed := (statusColWidthReview + colPad) + (prColWidth + colPad) + (repoWidth + colPad) + (authorColWidth + colPad) + (ageColWidth + colPad)
	title := totalWidth - fixed
	if title < minTitleWidth {
		title = minTitleWidth
	}
	return []table.Column{
		{Title: "STATUS", Width: statusColWidthReview},
		{Title: "PR", Width: prColWidth},
		{Title: "REPO", Width: repoWidth},
		{Title: "TITLE", Width: title},
		{Title: "AUTHOR", Width: authorColWidth},
		{Title: "AGE", Width: ageColWidth},
	}
}

func (m *Model) refreshTableContent() {
	myTableRows := make([]table.Row, len(m.myRows))
	for i, r := range m.myRows {
		myTableRows[i] = table.Row{
			r.Status.Icon() + " " + r.Status.Label(),
			fmt.Sprintf("#%d", r.Number),
			r.Repo,
			depthPrefix(r.Depth) + r.Title,
			r.Age,
		}
	}
	m.myTable.SetRows(myTableRows)
	fixStuckCursor(&m.myTable, len(myTableRows))

	reviewTableRows := make([]table.Row, len(m.reviewRows))
	for i, r := range m.reviewRows {
		reviewTableRows[i] = table.Row{
			reviewKindLabel(r.Kind),
			fmt.Sprintf("#%d", r.Number),
			r.Repo,
			r.Title,
			r.Author,
			r.Age,
		}
	}
	m.reviewTable.SetRows(reviewTableRows)
	fixStuckCursor(&m.reviewTable, len(reviewTableRows))

	watchTableRows := make([]table.Row, len(m.watchRows))
	for i, r := range m.watchRows {
		watchTableRows[i] = table.Row{
			watchStatusLabel(r.Draft),
			fmt.Sprintf("#%d", r.Number),
			r.Repo,
			r.Title,
			r.Author,
			r.Age,
		}
	}
	m.watchTable.SetRows(watchTableRows)
	fixStuckCursor(&m.watchTable, len(watchTableRows))
}

// watchStatusLabel is the STATUS cell of a Watched Authors row.
func watchStatusLabel(draft bool) string {
	if draft {
		return "📝 Draft"
	}
	return "👀 Open"
}

// fixStuckCursor works around a bubbles/table quirk: SetRows clamps the
// cursor down when it's too high, but never back up when it's too low. The
// first SetRows call after NewModel always has zero rows (data hasn't
// loaded yet), which drives the cursor to -1 — and it silently stays there
// forever once real rows arrive, since -1 is never ">" the new row count.
// A cursor of -1 matches no row, so selection, the "current row" highlight,
// and the 's'/'enter'/'o'/'v' keys would all silently no-op until the user
// happened to press an arrow key first. Once the cursor is at a real row
// (>= 0), this is a no-op — an in-progress selection is never reset.
func fixStuckCursor(t *table.Model, rowCount int) {
	if t.Cursor() < 0 && rowCount > 0 {
		t.SetCursor(0)
	}
}

func reviewKindLabel(kind string) string {
	if kind == github.ReviewKindRereview {
		return "🔁 Re-review"
	}
	return "🆕 New"
}

// depthPrefix renders the tree marker for a My PRs row stacked on another:
// "|---- " for a direct child, indented further per level of nesting.
func depthPrefix(depth int) string {
	if depth == 0 {
		return ""
	}
	return strings.Repeat("     ", depth-1) + "|---- "
}
