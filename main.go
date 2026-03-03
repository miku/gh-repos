// gh-repos fetches and manages GitHub repositories for a user.
//
// Usage:
//
//	gh-repos list [-u user] [-f]          List repos by name and description
//	gh-repos sync [-u user] [-d dir] [-f] [-p pattern] Clone or pull repos
//
// Environment:
//
//	GITHUB_TOKEN  GitHub personal access token (required)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

const (
	defaultCacheTTL = 1 * time.Hour
	cacheFileName   = "gh-repos-cache.json"
	githubAPIBase   = "https://api.github.com"

	ansiDim    = "\033[2m"
	ansiYellow = "\033[33m"
	ansiRed    = "\033[31m"
	ansiReset  = "\033[0m"
)

// Repo represents a GitHub repository.
type Repo struct {
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	Description string    `json:"description"`
	CloneURL    string    `json:"clone_url"`
	SSHURL      string    `json:"ssh_url"`
	HTMLURL     string    `json:"html_url"`
	Private     bool      `json:"private"`
	Fork        bool      `json:"fork"`
	Archived    bool      `json:"archived"`
	Stars       int       `json:"stargazers_count"`
	PushedAt    time.Time `json:"pushed_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CacheEntry stores cached repo data with a timestamp.
type CacheEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	User      string    `json:"user"`
	Repos     []Repo    `json:"repos"`
}

// GitHubClient handles communication with the GitHub API.
type GitHubClient struct {
	Token      string
	HTTPClient *http.Client
	BaseURL    string
}

// NewGitHubClient creates a new GitHub API client.
func NewGitHubClient(token string) *GitHubClient {
	return &GitHubClient{
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		BaseURL:    githubAPIBase,
	}
}

// AuthenticatedUser returns the login of the authenticated user.
func (c *GitHubClient) AuthenticatedUser(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github api error: %d %s", resp.StatusCode, string(body))
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", err
	}
	return user.Login, nil
}

// IsOrganization checks whether a GitHub account is an organization.
func (c *GitHubClient) IsOrganization(ctx context.Context, name string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/users/"+name, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("github api error: %d %s", resp.StatusCode, string(body))
	}

	var account struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return false, err
	}
	return account.Type == "Organization", nil
}

// ListRepos fetches all repositories for a user or organization, handling pagination.
// When owned is true, it uses the authenticated /user/repos endpoint
// which includes private repositories. For organizations, it uses the
// /orgs endpoint which returns all repos visible to the authenticated user.
func (c *GitHubClient) ListRepos(ctx context.Context, user string, owned bool) ([]Repo, error) {
	var allRepos []Repo
	page := 1

	isOrg := false
	if !owned {
		var err error
		isOrg, err = c.IsOrganization(ctx, user)
		if err != nil {
			return nil, fmt.Errorf("failed to determine account type: %w", err)
		}
	}

	for {
		var url string
		if owned {
			url = fmt.Sprintf("%s/user/repos?per_page=100&page=%d&sort=pushed&affiliation=owner", c.BaseURL, page)
		} else if isOrg {
			url = fmt.Sprintf("%s/orgs/%s/repos?per_page=100&page=%d&sort=pushed&type=all", c.BaseURL, user, page)
		} else {
			url = fmt.Sprintf("%s/users/%s/repos?per_page=100&page=%d&sort=pushed", c.BaseURL, user, page)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("github api error: %d %s", resp.StatusCode, string(body))
		}

		var repos []Repo
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			return nil, err
		}
		if len(repos) == 0 {
			break
		}
		allRepos = append(allRepos, repos...)
		page++
	}
	return allRepos, nil
}

// cacheDir returns the directory used for caching.
func cacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "gh-repos")
}

// cachePath returns the full path to the cache file for a user.
func cachePath(user string) string {
	return filepath.Join(cacheDir(), user+"-"+cacheFileName)
}

// loadCache loads cached repos for a user if the cache is still valid.
func loadCache(user string, ttl time.Duration) (*CacheEntry, error) {
	data, err := os.ReadFile(cachePath(user))
	if err != nil {
		return nil, err
	}
	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	if time.Since(entry.FetchedAt) > ttl {
		return nil, fmt.Errorf("cache expired")
	}
	return &entry, nil
}

// saveCache writes repo data to the cache file.
func saveCache(user string, repos []Repo) error {
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return err
	}
	entry := CacheEntry{
		FetchedAt: time.Now(),
		User:      user,
		Repos:     repos,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return os.WriteFile(cachePath(user), data, 0o644)
}

// getRepos returns repos for a user, using cache unless force is set.
// When owned is true, the authenticated /user/repos endpoint is used.
func getRepos(ctx context.Context, client *GitHubClient, user string, owned, force bool) ([]Repo, error) {
	if !force {
		if entry, err := loadCache(user, defaultCacheTTL); err == nil {
			return entry.Repos, nil
		}
	}
	repos, err := client.ListRepos(ctx, user, owned)
	if err != nil {
		return nil, err
	}
	if err := saveCache(user, repos); err != nil {
		log.Printf("warning: failed to save cache: %v", err)
	}
	return repos, nil
}

// resolveUser determines the GitHub username: explicit flag, or authenticated user.
// The second return value indicates whether this is the authenticated user (owned).
func resolveUser(ctx context.Context, client *GitHubClient, explicit string) (string, bool, error) {
	if explicit != "" {
		return explicit, false, nil
	}
	user, err := client.AuthenticatedUser(ctx)
	return user, err == nil, err
}

// isDirty checks if a git repo has uncommitted changes.
func isDirty(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return true // assume dirty on error
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// dimWriter wraps an io.Writer, prefixing output with ANSI dim and
// resetting after each Write so subprocess output appears muted.
type dimWriter struct {
	w io.Writer
}

func (d dimWriter) Write(p []byte) (int, error) {
	d.w.Write([]byte(ansiDim))
	n, err := d.w.Write(p)
	d.w.Write([]byte(ansiReset))
	return n, err
}

// gitEnv returns environment variables that prevent git from prompting
// for credentials interactively.
func gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

// gitClone clones a repo into the target directory.
func gitClone(ctx context.Context, cloneURL, targetDir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", cloneURL, targetDir)
	cmd.Env = gitEnv()
	cmd.Stdout = dimWriter{os.Stderr}
	cmd.Stderr = dimWriter{os.Stderr}
	return cmd.Run()
}

// gitPull runs git pull in the given directory.
func gitPull(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only")
	cmd.Env = gitEnv()
	cmd.Stdout = dimWriter{os.Stderr}
	cmd.Stderr = dimWriter{os.Stderr}
	return cmd.Run()
}

// termWidth returns the width of the terminal, defaulting to 80.
// When stdout is piped (e.g. into less), it falls back to stderr or
// the COLUMNS environment variable to preserve the original width.
func termWidth() int {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	if w, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && w > 0 {
		return w
	}
	return 80
}

// writeRepoList writes repos as a tab-aligned table, truncating descriptions
// so lines do not exceed width columns.
func writeRepoList(w io.Writer, repos []Repo, width int) {
	if len(repos) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	maxName := 0
	for _, r := range repos {
		if len(r.Name) > maxName {
			maxName = len(r.Name)
		}
	}
	// Find max star string width for budget calculation
	maxStars := 0
	for _, r := range repos {
		if s := len(fmt.Sprintf("%d", r.Stars)); r.Stars > 0 && s > maxStars {
			maxStars = s
		}
	}
	starsCol := 0
	if maxStars > 0 {
		starsCol = maxStars + 4 // "(nnn)" + padding
	}
	// Reserve space for icon, stars, name-desc padding, and right margin.
	maxDesc := width - maxName - starsCol - 6
	for _, r := range repos {
		icon := ""
		if r.Fork {
			icon = "⑂"
		} else if r.Private {
			icon = "◌"
		}
		stars := ""
		if r.Stars > 0 {
			stars = fmt.Sprintf("(%d)", r.Stars)
		}
		desc := r.Description
		if maxDesc > 3 && len(desc) > maxDesc {
			desc = desc[:maxDesc-3] + "..."
		} else if maxDesc <= 3 {
			desc = ""
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", icon, r.Name, stars, desc)
	}
	tw.Flush()
}

// matchPattern checks if name matches a glob-like pattern (supports * wildcards).
func matchPattern(pattern, name string) bool {
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*`, `.*`)
	matched, _ := regexp.MatchString("^"+re+"$", name)
	return matched
}

// cmdList implements the "list" subcommand.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	user := fs.String("u", "", "GitHub username (default: authenticated user)")
	force := fs.Bool("f", false, "Force fresh API request, ignoring cache")
	fs.Parse(args)

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	ctx := context.Background()
	client := NewGitHubClient(token)

	username, owned, err := resolveUser(ctx, client, *user)
	if err != nil {
		return fmt.Errorf("failed to resolve user: %w", err)
	}

	repos, err := getRepos(ctx, client, username, owned, *force)
	if err != nil {
		return fmt.Errorf("failed to list repos: %w", err)
	}

	writeRepoList(os.Stdout, repos, termWidth())
	return nil
}

// cmdSync implements the "sync" subcommand.
func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	user := fs.String("u", "", "GitHub username (default: authenticated user)")
	dir := fs.String("d", ".", "Target directory for cloned repos")
	force := fs.Bool("f", false, "Force fresh API request, ignoring cache")
	pattern := fs.String("p", "", "Filter repos by name pattern (glob with * wildcards)")
	fs.Parse(args)

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	ctx := context.Background()
	client := NewGitHubClient(token)

	username, owned, err := resolveUser(ctx, client, *user)
	if err != nil {
		return fmt.Errorf("failed to resolve user: %w", err)
	}

	repos, err := getRepos(ctx, client, username, owned, *force)
	if err != nil {
		return fmt.Errorf("failed to list repos: %w", err)
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	var (
		cloned  int
		pulled  int
		skipped int
		errors  int
	)

	for _, r := range repos {
		if *pattern != "" && !matchPattern(*pattern, r.Name) {
			continue
		}

		repoDir := filepath.Join(*dir, r.Name)
		if info, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && info.IsDir() {
			// Repo exists, try to pull
			if isDirty(repoDir) {
				fmt.Fprintf(os.Stderr, "%sskipping %s: working directory is dirty%s\n", ansiYellow, r.Name, ansiReset)
				skipped++
				continue
			}
			fmt.Fprintf(os.Stderr, "pulling %s ...\n", r.Name)
			if err := gitPull(ctx, repoDir); err != nil {
				fmt.Fprintf(os.Stderr, "%serror pulling %s: %v%s\n", ansiRed, r.Name, err, ansiReset)
				errors++
				continue
			}
			pulled++
		} else {
			// Clone
			fmt.Fprintf(os.Stderr, "cloning %s ...\n", r.Name)
			if err := gitClone(ctx, r.SSHURL, repoDir); err != nil {
				fmt.Fprintf(os.Stderr, "%serror cloning %s: %v%s\n", ansiRed, r.Name, err, ansiReset)
				errors++
				continue
			}
			cloned++
		}
	}

	fmt.Fprintf(os.Stderr, "\ndone: %d cloned, %d pulled, %d skipped (dirty), %d errors\n",
		cloned, pulled, skipped, errors)
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `gh-repos - fetch and manage GitHub repositories

Usage:
  gh-repos list [-u user] [-f]                List repos by name and description
  gh-repos sync [-u user] [-d dir] [-f] [-p pattern]  Clone or pull repos

Environment:
  GITHUB_TOKEN  GitHub personal access token (required)

Subcommands:
  list    List all repositories for a user
  sync    Clone new repos and pull existing ones

Flags:
  -u string  GitHub username (default: authenticated user)
  -f         Force fresh API request, ignoring cache
  -d string  Target directory for cloned repos (sync only, default: ".")
  -p string  Filter repos by name pattern with * wildcards (sync only)
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "list":
		err = cmdList(os.Args[2:])
	case "sync":
		err = cmdSync(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		log.Fatal(err)
	}
}
