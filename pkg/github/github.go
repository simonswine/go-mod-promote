package github

import (
	"context"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/go-github/v33/github"
	"golang.org/x/oauth2"

	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
)

type GitHub struct {
	client *github.Client
	logger log.Logger
}

func New(ctx context.Context, token string) *GitHub {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)

	return &GitHub{
		logger: gmpctx.LoggerFromContext(ctx),
		client: github.NewClient(tc),
	}
}

type NewPullRequest = github.NewPullRequest
type PullRequest = github.PullRequest

func (g *GitHub) Username(ctx context.Context) (string, error) {
	user, _, err := g.client.Users.Get(ctx, "")
	if err != nil {
		return "", err
	}

	return *user.Name, nil
}

func (g *GitHub) CreatePR(ctx context.Context, owner, repo string, newPR *NewPullRequest) (*PullRequest, error) {
	pr, _, err := g.client.PullRequests.Create(ctx, owner, repo, newPR)
	if err != nil {
		return nil, err
	}

	level.Info(g.logger).Log("created pull request", "url", pr.GetURL())
	return pr, err
}
