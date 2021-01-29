package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/davecgh/go-spew/spew"
	logkit "github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/grafana/go-mod-promote/pkg/api"
	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
	"github.com/grafana/go-mod-promote/pkg/tasks"
)

const configFile = ".go-mod-promote.yaml"
const AppName = "go-mod-promote"

func goMod(ctx context.Context, args ...string) *exec.Cmd {

	logger := gmpctx.LoggerFromContext(ctx)

	exe := "go"
	args = append([]string{"mod"}, args...)

	logger.Log("msg", "execute go mod", "command", append([]string{exe}, args...))
	return exec.Command(
		exe,
		args...,
	)
}

func goModDownload(ctx context.Context, path string) (*api.GoModDownloadResult, error) {
	cmd := goMod(ctx, "download", "-json", path)
	stdout := bytes.Buffer{}
	cmd.Stdout = &stdout
	stderr := bytes.Buffer{}
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("error getting go mod download metadata (%s): %w", stderr.String(), err)
	}
	var result api.GoModDownloadResult

	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, err
	}

	return &result, nil
}

type Config struct {
	Packages map[string]Package
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
	//ctx = gmpctx.LoggerIntoContext(ctx, a.logger)

	return ctx
}

func (a *App) Run(ctx context.Context) error {
	level.Debug(a.logger).Log("running_config", spew.Sdump(a.cfg))
	ctx = a.ctx(ctx)

	//
	/*
		goModPath := filepath.Join(a.rootPath, "go.mod")
		goModBytes, err := ioutil.ReadFile(goModPath)
		if err != nil {
			return err
		}

		goMod, err := modfile.ParseLax("go.mod", goModBytes, nil)
		if err != nil {
			return err
		}
	*/

	var result = &tasks.Result{}
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

		if modBefore.Version == modAfter.Version {
			level.Info(a.logger).Log("msg", "versions matching nothing to do", "package", pkg)
			return nil
		}

		ctx = gmpctx.GoModAfterIntoContext(ctx, modAfter)
		var results = make([]*tasks.Result, len(cfg.Tasks))
		for pos, task := range cfg.Tasks {
			var err error
			results[pos], err = task.Run(ctx)
			if err != nil {
				return err
			}
		}

		result = tasks.AggregateResult(append(results, result)...)

		/*
			var existingModule *modfile.Require
			for _, module := range goMod.Require {
				if module.Mod.Path == pkg {
					existingModule = module
				}
			}

			if existingModule == nil {
				return fmt.Errorf("package not found in go.mod: %s", pkg)
			}

		*/
	}

	if result.IsEmpty() {
		level.Info(a.logger).Log("msg", "No changes necessary")
		return nil
	}

	if err := result.Apply(); err != nil {
		return errors.Wrap(err, "error applying changes")
	}

	level.Debug(a.logger).Log("results", spew.Sdump(result))

	// TODO: aggregate results

	// TODO: exit here if there is nothing to do

	// test if the git working dir is clean
	workingDirClean, err := gitIsWorkingDirClean()
	if err != nil {
		return err
	}

	if !workingDirClean {
		// stash changes including unstaged
		level.Info(a.logger).Log("msg", "Stashing dirty working directory")

		if err := gitCommand(
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
			if err := gitCommand("stash", "pop").Run(); err != nil {
				level.Error(a.logger).Log("msg", "Failed to restore dirty working directory from stash", "error", err)
			} else {
				level.Info(a.logger).Log("msg", "Restored dirty working directory from stash")
			}
		}()
	}

	return nil
}

func gitIsWorkingDirClean() (bool, error) {
	out, err := gitCommand("status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("Failed to gather git status: %w", err)
	}

	if len(out) > 0 {
		return false, nil
	}

	return true, nil
}

func gitCommand(args ...string) *exec.Cmd {
	return exec.Command("git", args...)
}
