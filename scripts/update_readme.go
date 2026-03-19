// update_readme.go — rewrites dynamic zones in README.md using GitHub API data.
//
// Dynamic zones are delimited by HTML comment markers:
//
//	<!-- ZONE_START --> ... <!-- ZONE_END -->
//
// Zones updated:
//   - PROJECTS: top public repos ranked by stars + recency (no forks, no profile repo)
//   - OSS:      recent external PRs from public events (private repos filtered out)
//
// Usage:
//
//	GH_TOKEN=<pat> go run scripts/update_readme.go
//
// Required:
//
//	GH_TOKEN env var — Personal Access Token with scopes: repo, read:user

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	githubUsername    = "jeziellopes"
	apiBase           = "https://api.github.com"
	topProjectsCount  = 4
	ossContribCount   = 5
)

var readmePath = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "README.md")
}()

// --- GitHub API types ---

type repo struct {
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	HTMLURL         string    `json:"html_url"`
	StargazersCount int       `json:"stargazers_count"`
	ForksCount      int       `json:"forks_count"`
	Fork            bool      `json:"fork"`
	Private         bool      `json:"private"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type event struct {
	Type    string      `json:"type"`
	Repo    eventRepo   `json:"repo"`
	Payload prPayload   `json:"payload"`
}

type eventRepo struct {
	Name string `json:"name"`
}

type prPayload struct {
	PullRequest prRef `json:"pull_request"`
}

type prRef struct {
	URL    string `json:"url"`
	Number int    `json:"number"`
}

type pullRequest struct {
	Title    string    `json:"title"`
	HTMLURL  string    `json:"html_url"`
	State    string    `json:"state"`
	MergedAt *string   `json:"merged_at"`
	Base     prBase    `json:"base"`
}

type prBase struct {
	Repo baseRepo `json:"repo"`
}

type baseRepo struct {
	Private bool `json:"private"`
}

// --- HTTP helper ---

func ghGet(path, token string, out any) error {
	req, err := http.NewRequest("GET", apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API %s returned %d: %s", path, resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}

// --- Section builders ---

func repoScore(r repo) float64 {
	daysAgo := time.Since(r.UpdatedAt).Hours() / 24
	recency := max(0, 365-daysAgo) / 365
	return float64(r.StargazersCount)*10 + recency
}

func buildProjectsSection(token string) (string, error) {
	var repos []repo
	if err := ghGet(fmt.Sprintf("/users/%s/repos?sort=updated&per_page=100&type=owner", githubUsername), token, &repos); err != nil {
		return "", err
	}

	var public []repo
	for _, r := range repos {
		if !r.Private && !r.Fork && r.Name != githubUsername {
			public = append(public, r)
		}
	}
	sort.Slice(public, func(i, j int) bool {
		return repoScore(public[i]) > repoScore(public[j])
	})
	if len(public) > topProjectsCount {
		public = public[:topProjectsCount]
	}

	var sb strings.Builder
	sb.WriteString("### What I've shipped lately\n\n")
	for _, r := range public {
		desc := strings.TrimRight(r.Description, ".")
		stars := ""
		if r.StargazersCount > 0 {
			stars = fmt.Sprintf(" ⭐ %d", r.StargazersCount)
		}
		fmt.Fprintf(&sb, "- **[%s](%s)**%s — %s.\n", r.Name, r.HTMLURL, stars, desc)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func buildOSSSection(token string) (string, error) {
	var events []event
	if err := ghGet(fmt.Sprintf("/users/%s/events?per_page=100", githubUsername), token, &events); err != nil {
		return "", err
	}

	type contribution struct {
		title, prURL, repoName, repoURL, status string
	}

	seen := map[string]bool{}
	var contribs []contribution

	for _, e := range events {
		if e.Type != "PullRequestEvent" {
			continue
		}
		parts := strings.SplitN(e.Repo.Name, "/", 2)
		if len(parts) < 2 || parts[0] == githubUsername {
			continue
		}

		apiURL := e.Payload.PullRequest.URL
		if apiURL == "" || seen[apiURL] {
			continue
		}
		seen[apiURL] = true

		// Fetch full PR to get title, state, and privacy flag
		var pr pullRequest
		apiPath := strings.TrimPrefix(apiURL, apiBase)
		if err := ghGet(apiPath, token, &pr); err != nil {
			continue
		}

		// Never show PRs in private repos
		if pr.Base.Repo.Private {
			continue
		}

		var status string
		switch {
		case pr.MergedAt != nil:
			status = "✅ Merged"
		case pr.State == "open":
			status = "🔄 Open"
		default:
			status = "❌ Closed"
		}

		prURL := pr.HTMLURL
		if prURL == "" {
			prURL = fmt.Sprintf("https://github.com/%s/pull/%d", e.Repo.Name, e.Payload.PullRequest.Number)
		}
		title := pr.Title
		if title == "" {
			title = e.Repo.Name
		}

		contribs = append(contribs, contribution{
			title:    title,
			prURL:    prURL,
			repoName: e.Repo.Name,
			repoURL:  "https://github.com/" + e.Repo.Name,
			status:   status,
		})
		if len(contribs) >= ossContribCount {
			break
		}
	}

	if len(contribs) == 0 {
		return "### Recent OSS\n\n_No recent external contributions found._", nil
	}

	var sb strings.Builder
	sb.WriteString("### Recent OSS\n\n")
	for _, c := range contribs {
		fmt.Fprintf(&sb, "- %s **[%s](%s)** into [%s](%s)\n", c.status, c.title, c.prURL, c.repoName, c.repoURL)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// --- Zone rewriter ---

func rewriteZone(content, zone, body string) (string, bool) {
	pattern := regexp.MustCompile(`(?s)(<!-- ` + zone + `_START -->)\n.*?\n(<!-- ` + zone + `_END -->)`)
	replaced := false
	result := pattern.ReplaceAllStringFunc(content, func(match string) string {
		replaced = true
		start := "<!-- " + zone + "_START -->"
		end := "<!-- " + zone + "_END -->"
		return start + "\n" + body + "\n" + end
	})
	return result, replaced
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func main() {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "ERROR: GH_TOKEN environment variable is not set.")
		os.Exit(1)
	}

	absPath, err := filepath.Abs(readmePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}

	original, err := os.ReadFile(absPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR reading README:", err)
		os.Exit(1)
	}

	projects, err := buildProjectsSection(token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR fetching projects:", err)
		os.Exit(1)
	}

	oss, err := buildOSSSection(token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR fetching OSS contributions:", err)
		os.Exit(1)
	}

	updated := string(original)
	updated, _ = rewriteZone(updated, "PROJECTS", projects)
	updated, _ = rewriteZone(updated, "OSS", oss)

	if updated == string(original) {
		fmt.Println("README.md is already up to date.")
		return
	}

	if err := os.WriteFile(absPath, []byte(updated), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR writing README:", err)
		os.Exit(1)
	}
	fmt.Println("README.md updated.")
}
