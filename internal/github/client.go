package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-github/v60/github"
	"github.com/skalluru/velocix/internal/model"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"
)

const (
	maxConcurrency = 5
	perPage        = 100
)

type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Used      int       `json:"used"`
	ResetAt   time.Time `json:"reset_at"`
	Exceeded  bool      `json:"exceeded"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Client struct {
	gh         *github.Client
	httpClient *http.Client
	graphqlURL string
	logger     *slog.Logger

	rlMu sync.RWMutex
	rl   RateLimit
}

func NewClient(token string, baseURL string, logger *slog.Logger) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	ghClient := github.NewClient(tc)

	graphqlURL := "https://api.github.com/graphql"

	if baseURL != "" {
		apiURL := baseURL + "/api/v3/"
		uploadURL := baseURL + "/api/uploads/"
		var err error
		ghClient, err = ghClient.WithEnterpriseURLs(apiURL, uploadURL)
		if err != nil {
			logger.Warn("failed to configure enterprise URLs, using default", "error", err)
		}
		graphqlURL = baseURL + "/api/graphql"
	}

	return &Client{
		gh:         ghClient,
		httpClient: tc,
		graphqlURL: graphqlURL,
		logger:     logger,
	}
}

func (c *Client) HTTPClient() *http.Client { return c.httpClient }
func (c *Client) GraphQLURL() string       { return c.graphqlURL }

func (c *Client) GetRateLimit() RateLimit {
	c.rlMu.RLock()
	defer c.rlMu.RUnlock()
	return c.rl
}

func (c *Client) updateRateLimitFromGoGithub(r *github.Response) {
	if r == nil {
		return
	}
	c.rlMu.Lock()
	c.rl = RateLimit{
		Limit:     r.Rate.Limit,
		Remaining: r.Rate.Remaining,
		Used:      r.Rate.Limit - r.Rate.Remaining,
		ResetAt:   r.Rate.Reset.Time,
		Exceeded:  r.Rate.Remaining == 0 && !r.Rate.Reset.Time.IsZero() && time.Now().Before(r.Rate.Reset.Time),
		UpdatedAt: time.Now(),
	}
	c.rlMu.Unlock()
}

func (c *Client) updateRateLimitFromHeaders(h http.Header) {
	if h == nil {
		return
	}
	limit := parseHeaderInt(h.Get("X-RateLimit-Limit"))
	remaining := parseHeaderInt(h.Get("X-RateLimit-Remaining"))
	used := parseHeaderInt(h.Get("X-RateLimit-Used"))
	resetUnix := parseHeaderInt(h.Get("X-RateLimit-Reset"))
	if limit == 0 && remaining == 0 && resetUnix == 0 {
		return // not present
	}
	var resetAt time.Time
	if resetUnix > 0 {
		resetAt = time.Unix(int64(resetUnix), 0)
	}
	c.rlMu.Lock()
	c.rl = RateLimit{
		Limit:     limit,
		Remaining: remaining,
		Used:      used,
		ResetAt:   resetAt,
		Exceeded:  remaining == 0 && !resetAt.IsZero() && time.Now().Before(resetAt),
		UpdatedAt: time.Now(),
	}
	c.rlMu.Unlock()
}

func parseHeaderInt(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func (c *Client) FetchAllWorkflowRuns(ctx context.Context, org string) ([]model.WorkflowRun, error) {
	repos, err := c.listActiveRepos(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("listing repos: %w", err)
	}

	c.logger.Info("fetching workflow runs", "org", org, "active_repos", len(repos))

	var (
		mu      sync.Mutex
		allRuns []model.WorkflowRun
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrency)

	for _, repo := range repos {
		repo := repo
		g.Go(func() error {
			runs, err := c.fetchRepoRuns(gctx, org, repo)
			if err != nil {
				c.logger.Warn("failed to fetch runs", "repo", repo, "error", err)
				return nil
			}
			mu.Lock()
			allRuns = append(allRuns, runs...)
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	c.logger.Info("fetched workflow runs", "total", len(allRuns))
	return allRuns, nil
}

// GraphQL query to list repos sorted by most recently pushed.
const repoQuery = `query($org: String!, $cursor: String) {
  organization(login: $org) {
    repositories(first: 100, after: $cursor, orderBy: {field: PUSHED_AT, direction: DESC}) {
      pageInfo { hasNextPage endCursor }
      nodes { name pushedAt }
    }
  }
}`

type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphqlResponse struct {
	Data struct {
		Organization struct {
			Repositories struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Name     string    `json:"name"`
					PushedAt time.Time `json:"pushedAt"`
				} `json:"nodes"`
			} `json:"repositories"`
		} `json:"organization"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// listActiveRepos uses GraphQL to list all org repos in a single call
// (vs paginated REST). Workflow run time filtering happens in fetchRepoRuns.
func (c *Client) listActiveRepos(ctx context.Context, org string) ([]string, error) {
	var allRepos []string
	var cursor *string

	for {
		variables := map[string]any{
			"org":    org,
			"cursor": cursor,
		}

		body, err := json.Marshal(graphqlRequest{Query: repoQuery, Variables: variables})
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, "POST", c.graphqlURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("graphql request: %w", err)
		}

		c.updateRateLimitFromHeaders(resp.Header)

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading graphql response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("graphql returned %d: %s", resp.StatusCode, string(respBody))
		}

		var result graphqlResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("parsing graphql response: %w", err)
		}

		if len(result.Errors) > 0 {
			return nil, fmt.Errorf("graphql: %s", result.Errors[0].Message)
		}

		repos := result.Data.Organization.Repositories
		for _, r := range repos.Nodes {
			allRepos = append(allRepos, r.Name)
		}

		if !repos.PageInfo.HasNextPage {
			break
		}
		endCursor := repos.PageInfo.EndCursor
		cursor = &endCursor
	}

	c.logger.Info("listed repos via graphql", "org", org, "count", len(allRepos))
	return allRepos, nil
}

func (c *Client) fetchRepoRuns(ctx context.Context, owner, repo string) ([]model.WorkflowRun, error) {
	since := time.Now().Add(-24 * time.Hour)
	opts := &github.ListWorkflowRunsOptions{
		Created: ">=" + since.Format("2006-01-02"),
		ListOptions: github.ListOptions{
			PerPage: perPage,
		},
	}

	var runs []model.WorkflowRun

	for {
		result, resp, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		if resp != nil {
			c.updateRateLimitFromGoGithub(resp)
		}
		if err != nil {
			return nil, err
		}

		for _, r := range result.WorkflowRuns {
			runs = append(runs, toWorkflowRun(owner, repo, r))
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return runs, nil
}

func toWorkflowRun(owner, repo string, r *github.WorkflowRun) model.WorkflowRun {
	return model.WorkflowRun{
		ID:           r.GetID(),
		RepoName:     repo,
		RepoOwner:    owner,
		WorkflowName: r.GetName(),
		WorkflowPath: r.GetURL(),
		HeadBranch:   r.GetHeadBranch(),
		HeadSHA:      r.GetHeadSHA(),
		Status:       r.GetStatus(),
		Conclusion:   r.GetConclusion(),
		Event:        r.GetEvent(),
		HTMLURL:      r.GetHTMLURL(),
		CreatedAt:    r.GetCreatedAt().Time,
		UpdatedAt:    r.GetUpdatedAt().Time,
		RunNumber:    r.GetRunNumber(),
		RunAttempt:   r.GetRunAttempt(),
		Actor:        r.GetActor().GetLogin(),
	}
}
