package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*", "anything", true},
		{"foo", "foo", true},
		{"foo", "bar", false},
		{"go-*", "go-solr", true},
		{"go-*", "python-solr", false},
		{"*-tools", "my-tools", true},
		{"*-tools", "my-utils", false},
		{"*data*", "bigdata-tool", true},
		{"exact-match", "exact-match", true},
		{"exact-match", "not-exact-match", false},
	}

	for _, tt := range tests {
		got := matchPattern(tt.pattern, tt.name)
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func TestIsDirty(t *testing.T) {
	// A non-existent directory should be considered dirty
	if !isDirty("/nonexistent/path") {
		t.Error("expected non-existent path to be dirty")
	}
}

func setupTestServer(t *testing.T, repos []Repo) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"login": "testuser"})
	})

	repoHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		page := r.URL.Query().Get("page")
		if page == "" || page == "1" {
			json.NewEncoder(w).Encode(repos)
		} else {
			json.NewEncoder(w).Encode([]Repo{})
		}
	}
	mux.HandleFunc("/users/testuser/repos", repoHandler)
	mux.HandleFunc("/user/repos", repoHandler)

	return httptest.NewServer(mux)
}

func TestGitHubClientAuthenticatedUser(t *testing.T) {
	server := setupTestServer(t, nil)
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
	}

	user, err := client.AuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "testuser" {
		t.Errorf("got user %q, want %q", user, "testuser")
	}
}

func TestGitHubClientAuthenticatedUserUnauthorized(t *testing.T) {
	server := setupTestServer(t, nil)
	defer server.Close()

	client := &GitHubClient{
		Token:      "bad-token",
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
	}

	_, err := client.AuthenticatedUser(context.Background())
	if err == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestGitHubClientListRepos(t *testing.T) {
	testRepos := []Repo{
		{Name: "repo-a", FullName: "testuser/repo-a", Description: "First repo", CloneURL: "https://github.com/testuser/repo-a.git"},
		{Name: "repo-b", FullName: "testuser/repo-b", Description: "", CloneURL: "https://github.com/testuser/repo-b.git"},
		{Name: "go-tool", FullName: "testuser/go-tool", Description: "A Go tool", CloneURL: "https://github.com/testuser/go-tool.git"},
	}

	server := setupTestServer(t, testRepos)
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
	}

	repos, err := client.ListRepos(context.Background(), "testuser", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("got %d repos, want 3", len(repos))
	}
	if repos[0].Name != "repo-a" {
		t.Errorf("first repo name = %q, want %q", repos[0].Name, "repo-a")
	}
	if repos[2].Description != "A Go tool" {
		t.Errorf("third repo description = %q, want %q", repos[2].Description, "A Go tool")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	// Use a temp dir for cache
	tmpDir := t.TempDir()
	origCacheDir := cacheDir
	// We need to override cache dir for testing; let's test via saveCache/loadCache with a known user
	// Since cacheDir is a function, we'll just test the file directly.

	testUser := "cache-test-user-" + filepath.Base(tmpDir)
	repos := []Repo{
		{Name: "cached-repo", Description: "A cached repo"},
	}

	// Override cache dir by setting XDG_CACHE_HOME
	os.Setenv("XDG_CACHE_HOME", tmpDir)
	defer os.Unsetenv("XDG_CACHE_HOME")
	_ = origCacheDir // just to avoid unused variable if cacheDir is a func

	err := saveCache(testUser, repos)
	if err != nil {
		t.Fatalf("saveCache failed: %v", err)
	}

	entry, err := loadCache(testUser, 1*time.Hour)
	if err != nil {
		t.Fatalf("loadCache failed: %v", err)
	}
	if len(entry.Repos) != 1 {
		t.Fatalf("got %d cached repos, want 1", len(entry.Repos))
	}
	if entry.Repos[0].Name != "cached-repo" {
		t.Errorf("cached repo name = %q, want %q", entry.Repos[0].Name, "cached-repo")
	}
}

func TestCacheExpired(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_CACHE_HOME", tmpDir)
	defer os.Unsetenv("XDG_CACHE_HOME")

	testUser := "expired-test-user"
	repos := []Repo{{Name: "old-repo"}}

	err := saveCache(testUser, repos)
	if err != nil {
		t.Fatalf("saveCache failed: %v", err)
	}

	// Load with zero TTL should fail
	_, err = loadCache(testUser, 0)
	if err == nil {
		t.Error("expected cache to be expired with 0 TTL")
	}
}

func TestGetReposUsesCache(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_CACHE_HOME", tmpDir)
	defer os.Unsetenv("XDG_CACHE_HOME")

	// Save to cache first
	testUser := "cached-user"
	cachedRepos := []Repo{{Name: "from-cache", Description: "cached"}}
	if err := saveCache(testUser, cachedRepos); err != nil {
		t.Fatalf("saveCache failed: %v", err)
	}

	// Server that would return different repos
	serverRepos := []Repo{{Name: "from-server", Description: "fresh"}}
	server := setupTestServer(t, serverRepos)
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
	}

	// Without force, should get cached data
	repos, err := getRepos(context.Background(), client, testUser, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "from-cache" {
		t.Errorf("expected cached repo, got %v", repos)
	}
}

func TestGetReposForceBypassesCache(t *testing.T) {
	tmpDir := t.TempDir()
	os.Setenv("XDG_CACHE_HOME", tmpDir)
	defer os.Unsetenv("XDG_CACHE_HOME")

	// Save to cache first
	testUser := "testuser"
	cachedRepos := []Repo{{Name: "from-cache", Description: "cached"}}
	if err := saveCache(testUser, cachedRepos); err != nil {
		t.Fatalf("saveCache failed: %v", err)
	}

	// Server returns different repos
	serverRepos := []Repo{{Name: "from-server", Description: "fresh"}}
	server := setupTestServer(t, serverRepos)
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
	}

	// With force, should get server data
	repos, err := getRepos(context.Background(), client, testUser, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "from-server" {
		t.Errorf("expected server repo, got %v", repos)
	}
}

func TestResolveUserExplicit(t *testing.T) {
	client := NewGitHubClient("unused")
	user, owned, err := resolveUser(context.Background(), client, "explicit-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "explicit-user" {
		t.Errorf("got %q, want %q", user, "explicit-user")
	}
	if owned {
		t.Error("expected owned=false for explicit user")
	}
}

func TestResolveUserFromAPI(t *testing.T) {
	server := setupTestServer(t, nil)
	defer server.Close()

	client := &GitHubClient{
		Token:      "test-token",
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
	}

	user, owned, err := resolveUser(context.Background(), client, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "testuser" {
		t.Errorf("got %q, want %q", user, "testuser")
	}
	if !owned {
		t.Error("expected owned=true for authenticated user")
	}
}

func TestWriteRepoList(t *testing.T) {
	repos := []Repo{
		{Name: "short", Description: "A short description", Stars: 42},
		{Name: "longer-name", Description: "Another description"},
		{Name: "no-desc"},
		{Name: "forked-repo", Description: "A forked project", Fork: true},
		{Name: "secret", Description: "A private repo", Private: true},
	}

	var buf bytes.Buffer
	writeRepoList(&buf, repos, 80)
	out := buf.String()

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(lines))
	}
	if !strings.Contains(lines[0], "short") || !strings.Contains(lines[0], "A short description") {
		t.Errorf("unexpected first line: %q", lines[0])
	}
	if !strings.Contains(lines[2], "no-desc") {
		t.Errorf("unexpected third line: %q", lines[2])
	}
	// Fork line should have the fork icon
	if !strings.Contains(lines[3], "⑂") {
		t.Errorf("expected fork icon in forked repo line: %q", lines[3])
	}
	// Private line should have the private icon
	if !strings.Contains(lines[4], "◌") {
		t.Errorf("expected private icon in private repo line: %q", lines[4])
	}
	// Starred repo should show star count
	if !strings.Contains(lines[0], "(42)") {
		t.Errorf("expected star count (42) in line: %q", lines[0])
	}
	// Zero-star repo should not show count
	if strings.Contains(lines[1], "(0)") {
		t.Errorf("zero-star repo should not show count: %q", lines[1])
	}
	// Public non-fork lines should not have icons
	if strings.Contains(lines[0], "⑂") || strings.Contains(lines[0], "◌") {
		t.Errorf("public non-fork line should not have icons: %q", lines[0])
	}
}

func TestWriteRepoListTruncation(t *testing.T) {
	repos := []Repo{
		{Name: "myrepo", Description: "This is a very long description that should be truncated"},
	}

	var buf bytes.Buffer
	// width=30: name "myrepo" (6) + 2 padding = 8, so maxDesc = 22
	writeRepoList(&buf, repos, 30)
	out := buf.String()

	line := strings.TrimSpace(out)
	if len(line) > 30 {
		t.Errorf("line exceeds width 30: len=%d %q", len(line), line)
	}
	if !strings.HasSuffix(line, "...") {
		t.Errorf("expected truncated description to end with '...': %q", line)
	}
}

func TestWriteRepoListEmpty(t *testing.T) {
	var buf bytes.Buffer
	writeRepoList(&buf, nil, 80)
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty list, got %q", buf.String())
	}
}
