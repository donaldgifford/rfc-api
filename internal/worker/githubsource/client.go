// Package githubsource is the worker's GitHub access layer. It owns
// authentication (App JWT → installation token, with a PAT fallback
// for dev), the go-github client, and rate-limit retry. Callers in
// internal/worker/scanner and internal/worker/ingest depend on the
// exported methods on Client.
//
// Per IMPL-0003 RD1 the production path is App-based; a non-empty
// GITHUB_TOKEN PAT is accepted so local `rfc-api work` bootstraps
// without minting an App.
package githubsource

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v67/github"
)

// File is one Markdown file enumerated under a SourceRepo.Path.
// SHA is GitHub's blob sha — the idempotency key for `ingest` jobs
// per IMPL-0003 RD9.
type File struct {
	Path string
	SHA  string
	Size int
}

// PullRequest is the subset of fields the discussion fetcher needs
// in Phase 6. Kept minimal so tests can populate fixtures by hand.
type PullRequest struct {
	Number int
	State  string
	Merged bool
	Title  string
	URL    string
}

// Config bundles credentials + tuning knobs. Either App creds
// (AppID + InstallationID + PrivateKey) or Token must be set —
// New returns an error if both or neither are supplied. HTTPClient
// is optional; tests inject an httptest backing.
type Config struct {
	AppID          string
	InstallationID string
	PrivateKey     []byte
	Token          string
	BaseURL        string
	HTTPClient     *http.Client
	MaxRetries     int
	MaxBackoff     time.Duration
}

// Client is the concrete GitHub access seam.
type Client struct {
	api        *github.Client
	maxRetries int
	maxBackoff time.Duration
}

// New builds a Client. When App creds are populated the transport
// is wrapped in ghinstallation for automatic JWT → installation-
// token exchange + refresh. Config is taken by pointer to avoid
// the 112-byte copy gocritic flags under hugeParam.
func New(cfgPtr *Config) (*Client, error) {
	if cfgPtr == nil {
		return nil, errors.New("githubsource: nil config")
	}
	cfg := *cfgPtr

	httpClient, err := authedClient(&cfg)
	if err != nil {
		return nil, err
	}

	api, err := githubAPI(httpClient, cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	retries := cfg.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	backoff := cfg.MaxBackoff
	if backoff <= 0 {
		backoff = 30 * time.Second
	}
	return &Client{api: api, maxRetries: retries, maxBackoff: backoff}, nil
}

// authedClient returns the http.Client carrying the right
// authentication transport for the given creds. Exactly one of App
// creds or Token must be set — enforced here so New stays thin.
func authedClient(cfg *Config) (*http.Client, error) {
	hasApp := cfg.AppID != "" && cfg.InstallationID != "" && len(cfg.PrivateKey) > 0
	hasPAT := cfg.Token != ""
	if hasApp == hasPAT {
		return nil, errors.New("githubsource: exactly one of {App creds, Token} is required")
	}

	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}

	if hasApp {
		appID, err := strconv.ParseInt(cfg.AppID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse AppID: %w", err)
		}
		instID, err := strconv.ParseInt(cfg.InstallationID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse InstallationID: %w", err)
		}
		tr, err := ghinstallation.New(base.Transport, appID, instID, cfg.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("ghinstallation: %w", err)
		}
		return &http.Client{Transport: tr, Timeout: base.Timeout}, nil
	}
	return patClient(cfg.Token, base), nil
}

// githubAPI constructs the go-github client, optionally pointed at
// an enterprise URL. Used by tests to target an httptest server.
func githubAPI(httpClient *http.Client, baseURL string) (*github.Client, error) {
	api := github.NewClient(httpClient)
	if baseURL == "" {
		return api, nil
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	parsed, err := api.WithEnterpriseURLs(baseURL, baseURL)
	if err != nil {
		return nil, fmt.Errorf("set enterprise urls: %w", err)
	}
	return parsed, nil
}

// ListFiles enumerates Markdown files under path@ref one directory
// level deep. Sub-directories are ignored — IMPL-0003's scanner
// treats path as a flat document set per DESIGN-0002.
func (c *Client) ListFiles(ctx context.Context, repo, path, ref string) ([]File, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	opts := &github.RepositoryContentGetOptions{Ref: ref}

	var entries []*github.RepositoryContent
	err = c.withRetry(ctx, func() error {
		_, dirContent, _, err := c.api.Repositories.GetContents(ctx, owner, name, path, opts)
		if err != nil {
			return err //nolint:wrapcheck // wrapped at the outer call site
		}
		entries = dirContent
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list %s:%s@%s: %w", repo, path, ref, err)
	}

	files := make([]File, 0, len(entries))
	for _, entry := range entries {
		if entry.GetType() != "file" {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.GetName()), ".md") {
			continue
		}
		files = append(files, File{
			Path: entry.GetPath(),
			SHA:  entry.GetSHA(),
			Size: entry.GetSize(),
		})
	}
	return files, nil
}

// GetFile returns the raw content + blob sha for a file at ref.
// The sha matches the `content_sha` idempotency key (IMPL-0003 RD9).
func (c *Client) GetFile(ctx context.Context, repo, path, ref string) ([]byte, string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, "", err
	}
	opts := &github.RepositoryContentGetOptions{Ref: ref}

	var fileContent *github.RepositoryContent
	err = c.withRetry(ctx, func() error {
		fc, _, _, err := c.api.Repositories.GetContents(ctx, owner, name, path, opts)
		if err != nil {
			return err //nolint:wrapcheck // wrapped at the outer call site
		}
		fileContent = fc
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("get %s:%s@%s: %w", repo, path, ref, err)
	}
	if fileContent == nil {
		return nil, "", fmt.Errorf("get %s:%s@%s: not a file", repo, path, ref)
	}
	decoded, err := fileContent.GetContent()
	if err != nil {
		return nil, "", fmt.Errorf("decode %s:%s@%s: %w", repo, path, ref, err)
	}
	return []byte(decoded), fileContent.GetSHA(), nil
}

// ListPullRequestsForFile enumerates PRs that touched `path`,
// most-recent-first via the commit history. Used by Phase 6 to
// locate the PR discussion thread for a document.
func (c *Client) ListPullRequestsForFile(ctx context.Context, repo, path string) ([]PullRequest, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	var commits []*github.RepositoryCommit
	err = c.withRetry(ctx, func() error {
		cs, _, err := c.api.Repositories.ListCommits(ctx, owner, name,
			&github.CommitsListOptions{
				Path:        path,
				ListOptions: github.ListOptions{PerPage: 20},
			})
		if err != nil {
			return err //nolint:wrapcheck // wrapped at the outer call site
		}
		commits = cs
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list commits %s:%s: %w", repo, path, err)
	}

	seen := make(map[int]bool)
	out := make([]PullRequest, 0, len(commits))
	for _, commit := range commits {
		sha := commit.GetSHA()
		if sha == "" {
			continue
		}
		var prs []*github.PullRequest
		err := c.withRetry(ctx, func() error {
			got, _, err := c.api.PullRequests.ListPullRequestsWithCommit(ctx, owner, name, sha, nil)
			if err != nil {
				return err //nolint:wrapcheck // wrapped at the outer call site
			}
			prs = got
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("prs for %s@%s: %w", repo, sha, err)
		}
		for _, pr := range prs {
			if pr == nil || seen[pr.GetNumber()] {
				continue
			}
			seen[pr.GetNumber()] = true
			out = append(out, PullRequest{
				Number: pr.GetNumber(),
				State:  pr.GetState(),
				Merged: pr.GetMerged(),
				Title:  pr.GetTitle(),
				URL:    pr.GetHTMLURL(),
			})
		}
	}
	return out, nil
}

// withRetry runs fn up to maxRetries times, sleeping per GitHub's
// rate-limit signal between attempts. Non-rate-limit errors
// propagate immediately.
func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		wait := rateLimitWait(err, c.maxBackoff)
		if wait <= 0 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return fmt.Errorf("exhausted retries: %w", lastErr)
}

// rateLimitWait extracts a sleep duration from a go-github rate-
// limit error. Returns 0 when err is not a rate-limit signal so the
// caller surfaces it as a hard failure.
func rateLimitWait(err error, maxWait time.Duration) time.Duration {
	var rl *github.RateLimitError
	if errors.As(err, &rl) {
		wait := time.Until(rl.Rate.Reset.Time)
		if wait <= 0 {
			wait = time.Second
		}
		if wait > maxWait {
			wait = maxWait
		}
		return wait
	}
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) {
		if abuse.RetryAfter != nil {
			wait := *abuse.RetryAfter
			if wait > maxWait {
				wait = maxWait
			}
			return wait
		}
		return 5 * time.Second
	}
	return 0
}

// patClient wraps base with a bearer-token transport. The PAT path
// is a dev shortcut; production uses ghinstallation.
func patClient(token string, base *http.Client) *http.Client {
	tr := base.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	return &http.Client{
		Timeout:   base.Timeout,
		Transport: &bearerTransport{token: token, inner: tr},
	}
}

type bearerTransport struct {
	token string
	inner http.RoundTripper
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.inner.RoundTrip(req) //nolint:wrapcheck // transport passthrough
}

// splitRepo splits "owner/name" into its parts.
func splitRepo(s string) (string, string, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo %q: want owner/name", s)
	}
	return parts[0], parts[1], nil
}
