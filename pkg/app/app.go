package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	logkit "github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/grafana/go-mod-promote/pkg/api"
	"github.com/grafana/go-mod-promote/pkg/command"
	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
	"github.com/grafana/go-mod-promote/pkg/github"
	"github.com/grafana/go-mod-promote/pkg/gomod"
	"github.com/grafana/go-mod-promote/pkg/tasks"
)

const configFile = ".go-mod-promote.yaml"
const AppName = "go-mod-promote"

func goModDownload(ctx context.Context, path string) (*api.GoModDownloadResult, error) {
	cmd := command.New(ctx, "go", "mod", "download", "-json", path)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("error getting go mod download metadata (%s): %w", cmd.Stderr.String(), err)
	}
	var result api.GoModDownloadResult

	if err := json.Unmarshal(cmd.Stdout.Bytes(), &result); err != nil {
		return nil, err
	}

	return &result, nil
}

type Config struct {
	Packages map[string]Package `yaml:"packages"`

	GitHub GitHub `yaml:"github"`

	// If VendorDirectory is set to true, go mod vendor will be called after
	// changes to vendoring
	VendorDirectory bool `yaml:"vendor_directory"`
}

type GitHub struct {
	Owner string
	Repo  string
}

type Package struct {
	RemoteURL string       `yaml:"remote_url"`
	Branch    string       `yaml:"branch"`
	Tasks     []tasks.Task `yaml:"tasks"`
}

type Option func(*App)

func WithLogger(logger logkit.Logger) Option {
	return func(a *App) {
		a.logger = logger
	}
}

type App struct {
	cfg      *Config
	rootPath string

	logger logkit.Logger
}

func New(opts ...Option) (*App, error) {
	app := &App{
		logger: logkit.NewNopLogger(),
	}

	for _, opt := range opts {
		opt(app)
	}

	// find root path with config file
	dirPath, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	var filePath string
	for {
		filePath = filepath.Join(dirPath, configFile)

		if info, err := os.Stat(filePath); os.IsNotExist(err) {
			if dirPath == "/" {
				return nil, fmt.Errorf("no config file '%s' exists", configFile)
			}
			dirPath = filepath.Dir(dirPath)
			continue
		} else if err != nil {
			return nil, err
		} else if info.IsDir() {
			return nil, fmt.Errorf("%s is a directory", filePath)
		}

		break
	}
	app.rootPath = dirPath

	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	config := &Config{}
	if err := yaml.NewDecoder(f).Decode(&config); err != nil {
		return nil, err
	}
	app.cfg = config

	return app, nil
}

func (a *App) ctx(ctx context.Context) context.Context {
	ctx = gmpctx.RootPathIntoContext(ctx, a.rootPath)
	ctx = gmpctx.LoggerIntoContext(ctx, a.logger)
	return ctx
}

type Result interface {
	IsEmpty() bool
	Apply(context.Context) error
}

type goModUpdateResult struct {
	goMod     *gomod.GoMod
	pkg       string
	remoteURL string
	version   string
}

func (r *goModUpdateResult) Apply(ctx context.Context) error {
	return r.goMod.UpdatePackage(r.pkg, r.version)
}

func (r *goModUpdateResult) IsEmpty() bool {
	return false
}

func (a *App) Run(ctx context.Context) error {
	level.Debug(a.logger).Log("running_config", spew.Sdump(a.cfg))
	ctx = a.ctx(ctx)

	// TODO: test github token if not a
	githubToken := os.Getenv("GITHUB_TOKEN")

	goMod, err := gomod.NewGoModFromContext(ctx)
	if err != nil {
		return err
	}
	ctx = gmpctx.GoModFileIntoContext(ctx, goMod)

	var results []Result
	var packagesUpdated []string
	for pkg, cfg := range a.cfg.Packages {
		modBefore, err := goModDownload(ctx, pkg)
		if err != nil {
			return err
		}
		level.Info(a.logger).Log("msg", "existing package version in go.mod", "package", pkg, "version", modBefore.Version.Release(), "hash", modBefore.Version.Hash())
		ctx = gmpctx.GoModBeforeIntoContext(ctx, modBefore)

		if cfg.Branch == "" {
			cfg.Branch = "master"
		}
		if cfg.RemoteURL == "" {
			cfg.RemoteURL = pkg
		}

		modAfter, err := goModDownload(ctx, fmt.Sprintf("%s@%s", cfg.RemoteURL, cfg.Branch))
		if err != nil {
			return err
		}
		level.Info(a.logger).Log("msg", "new package version for go.mod", "package", pkg, "version", modAfter.Version.Release(), "hash", modAfter.Version.Hash())
		ctx = gmpctx.GoModAfterIntoContext(ctx, modAfter)

		if modBefore.Version == modAfter.Version {
			level.Info(a.logger).Log("msg", "versions matching nothing to do", "package", pkg)
			continue
		}

		packagesUpdated = append(packagesUpdated, pkg)

		var taskResults = make([]*tasks.Result, len(cfg.Tasks))
		for pos, task := range cfg.Tasks {
			var err error
			taskResults[pos], err = task.Run(ctx)
			if err != nil {
				return err
			}
		}

		// add results to global results
		results = append(results,
			&goModUpdateResult{
				goMod:     goMod,
				pkg:       pkg,
				remoteURL: cfg.RemoteURL,
				version:   modAfter.Version.Hash(),
			},
			tasks.AggregateResult(taskResults...),
		)
	}

	// exit here if there is nothing to do
	// TODO: also check for go mod changes
	workToDo := false
	for _, r := range results {
		if !r.IsEmpty() {
			workToDo = true
		}
	}
	if !workToDo {
		level.Info(a.logger).Log("msg", "No changes necessary")
		return nil
	}

	// test if the git working dir is clean
	workingDirClean, err := gitIsWorkingDirClean(ctx)
	if err != nil {
		return err
	}

	if !workingDirClean {
		// stash changes including unstaged
		level.Info(a.logger).Log("msg", "Stashing dirty working directory")

		if err := gitCommand(
			ctx,
			"stash",
			"push",
			"-m", fmt.Sprintf(
				"[%s] stashed dirty working directory at %s",
				AppName,
				time.Now().Format(time.RFC3339),
			)).Run(); err != nil {
			return fmt.Errorf("Failed to stash dirty working directory: %w", err)
		}

		// stash pop changes including unstaged
		defer func() {
			if err := gitCommand(ctx, "stash", "pop").Run(); err != nil {
				level.Error(a.logger).Log("msg", "Failed to restore dirty working directory from stash", "error", err)
			} else {
				level.Info(a.logger).Log("msg", "Restored dirty working directory from stash")
			}
		}()
	}

	// apply changes from results
	for _, result := range results {
		if err := result.Apply(ctx); err != nil {
			if merr, ok := err.(*multierror.Error); ok {
				for pos, err := range merr.Errors {
					level.Warn(a.logger).Log("msg", "error applying result", "pos", pos, "err", err)
				}
			}
			return errors.Wrap(err, "error applying changes")
		}
	}

	// write go mod
	if err := goMod.Finish(ctx, a.cfg.VendorDirectory); err != nil {
		return err
	}

	// create a new branch
	branchName := fmt.Sprintf(
		"vendor_go-mod-promote_%s",
		time.Now().Format("2006-01-02_150405"),
	)
	if err := gitCommand(ctx, "checkout", "-b", branchName).Run(); err != nil {
		return err
	}

	// create a git commit with changes
	if err := gitCommand(ctx, "add", "-A", ".").Run(); err != nil {
		return err
	}

	// TODO: Handle no changes
	if err := gitCommand(ctx, "commit", "--message", "chore: Update vendor", "--author", "Grafanabot go-mod-vendor <bot@grafana.com>", "--allow-empty").Run(); err != nil {
		return err
	}

	// figure out github user
	gh := github.New(ctx, githubToken)
	githubUsername, err := gh.Username(ctx)
	if err != nil {
		return err
	}

	// push commit
	githubURL := &url.URL{
		Host:   "github.com",
		Scheme: "https",
		Path:   fmt.Sprintf("/%s/%s.git", a.cfg.GitHub.Owner, a.cfg.GitHub.Repo),
		User:   url.UserPassword(githubUsername, githubToken),
	}
	if err := gitCommand(ctx, "push", githubURL.String(), branchName).Run(); err != nil {
		return err
	}

	// create PR
	baseBranch := "main"
	title := fmt.Sprintf("[go-mod-promote] Vendor update %s", strings.Join(packagesUpdated, ", "))
	_, err = gh.CreatePR(ctx, a.cfg.GitHub.Owner, a.cfg.GitHub.Repo, &github.NewPullRequest{
		Base:  &baseBranch,
		Head:  &branchName,
		Title: &title,
	})
	if err != nil {
		return err
	}

	return nil
}

func gitIsWorkingDirClean(ctx context.Context) (bool, error) {
	cmd := gitCommand(ctx, "status", "--porcelain")
	if err := cmd.Run(); err != nil {
		return false, err
	}
	if len(cmd.Stdout.String()) > 0 {
		return false, nil
	}

	return true, nil
}

func gitCommand(ctx context.Context, args ...string) *command.Cmd {
	return command.New(ctx, "git", args...)
}
