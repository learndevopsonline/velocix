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

type Client struct {
	gh         *github.Client
	httpClient *http.Client
	graphqlURL string
	logger     *slog.Logger
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

// listActiveRepos uses a single GraphQL call to get all repos sorted by
// push date, then returns only repos pushed within the last 24 hours.
// This replaces the paginated REST approach and skips dormant repos entirely.
func (c *Client) listActiveRepos(ctx context.Context, org string) ([]string, error) {
	since := time.Now().Add(-24 * time.Hour)
	var activeRepos []string
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
			if r.PushedAt.Before(since) {
				// Sorted by pushedAt DESC — all remaining repos are older, stop early
				c.logger.Info("skipping dormant repos", "org", org, "active", len(activeRepos))
				return activeRepos, nil
			}
			activeRepos = append(activeRepos, r.Name)
		}

		if !repos.PageInfo.HasNextPage {
			break
		}
		endCursor := repos.PageInfo.EndCursor
		cursor = &endCursor
	}

	return activeRepos, nil
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
