package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadWatchAuthors(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	zenDir := filepath.Join(tmpDir, ".zen")
	if err := os.MkdirAll(zenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yamlContent := `repos:
  floxhub:
    full_name: flox/floxhub
    base_path: ~/src
    watch_authors:
      - gilmishal
      - nicklewis
  flox:
    full_name: flox/flox
    base_path: ~/src
authors:
  - garbas
`
	if err := os.WriteFile(filepath.Join(zenDir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got, want := cfg.Repos["floxhub"].WatchAuthors, []string{"gilmishal", "nicklewis"}; !reflect.DeepEqual(got, want) {
		t.Errorf("floxhub watch_authors = %v, want %v", got, want)
	}
	if got := cfg.Repos["flox"].WatchAuthors; len(got) != 0 {
		t.Errorf("flox watch_authors = %v, want none", got)
	}
	// watch_authors is separate from the top-level authors list.
	if cfg.IsAuthor("gilmishal") {
		t.Error("watch_authors leaked into the top-level authors list")
	}
}

func TestWatchedRepos(t *testing.T) {
	cfg := &Config{Repos: map[string]RepoConfig{
		"zeta":    {FullName: "org/zeta", WatchAuthors: []string{"z"}},
		"alpha":   {FullName: "org/alpha", WatchAuthors: []string{"a", "b"}},
		"none":    {FullName: "org/none"},
		"no-name": {WatchAuthors: []string{"x"}},
	}}

	got := cfg.WatchedRepos()
	want := []WatchedRepo{
		{Name: "alpha", FullName: "org/alpha", Authors: []string{"a", "b"}},
		{Name: "zeta", FullName: "org/zeta", Authors: []string{"z"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WatchedRepos() = %+v, want %+v", got, want)
	}
}

func TestWatchedRepos_noneConfigured(t *testing.T) {
	cfg := &Config{Repos: map[string]RepoConfig{"r": {FullName: "org/r"}}}
	if got := cfg.WatchedRepos(); len(got) != 0 {
		t.Errorf("WatchedRepos() = %+v, want empty", got)
	}
}
