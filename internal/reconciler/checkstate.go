package reconciler

import (
	"encoding/json"
	"sort"
)

// CheckFile is the on-disk shape of ~/.zen/state/last_check.json.
type CheckFile struct {
	Timestamp   string            `json:"timestamp"`
	PRCount     int               `json:"pr_count"`
	AppliedSHAs map[string]string `json:"applied_shas,omitempty"`
	NotifiedNew []string          `json:"notified_new,omitempty"`
	SeenPRs     []string          `json:"seen_prs,omitempty"` // pre-#18 number-only keys
	// Sessions lists the PR keys whose review worktree has had an agent-deck
	// session registered (watch.review_sessions).
	Sessions []string `json:"agent_deck_sessions,omitempty"`
}

// PollMemory is the daemon's in-memory view of last_check.json.
type PollMemory struct {
	NotifiedNew map[string]bool
	AppliedSHAs map[string]string
	// Sessions holds the PR keys that have had an agent-deck session
	// registered, so one deleted in agent-deck is not registered again.
	Sessions map[string]bool
	// LegacySeen is set when the file still has seen_prs and no notified_new.
	// The first poll after upgrade suppresses "new review" notifications so
	// existing users are not spammed; SHA refresh still runs. The next save
	// writes notified_new and drops seen_prs.
	LegacySeen bool
}

// DecodePollMemory parses last_check.json. A missing or empty file is a
// fresh start (LegacySeen false).
func DecodePollMemory(data []byte) PollMemory {
	m := PollMemory{
		NotifiedNew: make(map[string]bool),
		AppliedSHAs: make(map[string]string),
		Sessions:    make(map[string]bool),
	}
	if len(data) == 0 {
		return m
	}
	var file CheckFile
	if err := json.Unmarshal(data, &file); err != nil {
		return m
	}
	for _, k := range file.NotifiedNew {
		m.NotifiedNew[k] = true
	}
	for _, k := range file.Sessions {
		m.Sessions[k] = true
	}
	for k, sha := range file.AppliedSHAs {
		m.AppliedSHAs[k] = sha
	}
	if len(file.SeenPRs) > 0 && len(file.NotifiedNew) == 0 {
		m.LegacySeen = true
	}
	return m
}

// EncodeCheckFile serializes PollMemory for last_check.json. seen_prs is never
// written; upgraded installs keep notified_new + applied_shas only.
func EncodeCheckFile(m PollMemory, timestamp string, prCount int) ([]byte, error) {
	notified := make([]string, 0, len(m.NotifiedNew))
	for k := range m.NotifiedNew {
		notified = append(notified, k)
	}
	sessions := make([]string, 0, len(m.Sessions))
	for k := range m.Sessions {
		sessions = append(sessions, k)
	}
	sort.Strings(sessions)
	file := CheckFile{
		Timestamp:   timestamp,
		PRCount:     prCount,
		AppliedSHAs: m.AppliedSHAs,
		NotifiedNew: notified,
		Sessions:    sessions,
	}
	return json.MarshalIndent(file, "", "  ")
}
