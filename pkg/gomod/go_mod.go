package gomod

import (
	"context"
	"io/ioutil"
	"path/filepath"
	"sort"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/grafana/go-mod-promote/pkg/command"
	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
)

type GoMod struct {
	file     *modfile.File
	path     string
	logger   log.Logger
	replaces []Replace
}

type ReplacePriority int32

const (
	ReplacePriorityManagedPackage = ReplacePriority(1000)
	ReplaceUpstreamPackageVersion = ReplacePriority(400)
	ReplaceUpstreamReplace        = ReplacePriority(200)
)

type Replace struct {
	modfile.Replace
	// Higher Priority values overwrite lower priority ones
	Priority ReplacePriority
}

func NewGoModFromContext(ctx context.Context) (*GoMod, error) {
	logger := gmpctx.LoggerFromContext(ctx)
	logger = log.With(logger, "module", "gomod")
	path := filepath.Join(gmpctx.RootPathFromContext(ctx), "go.mod")

	goModData, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	goMod, err := modfile.Parse("go.mod", goModData, nil)
	if err != nil {
		return nil, err
	}

	return &GoMod{
		file:   goMod,
		path:   path,
		logger: logger,
	}, nil
}

func (g *GoMod) AddReplace(r Replace) error {
	g.replaces = append(g.replaces, r)
	return nil
}

func (g *GoMod) UpdatePackage(pkg, version string) error {
	logger := log.With(g.logger, "pkg", pkg, "version", version)
	level.Debug(logger).Log("msg", "update package")

	if err := g.file.AddRequire(pkg, version); err != nil {
		return err
	}

	replaceExists := false
	for _, replace := range g.file.Replace {
		if replace.Old.Path == pkg {
			replaceExists = true
		}
	}

	if replaceExists {
		level.Info(logger).Log("msg", "update existing replace statement")
		if err := g.AddReplace(Replace{
			Replace: modfile.Replace{
				Old: module.Version{
					Path: pkg,
				},
				New: module.Version{
					Path:    pkg,
					Version: version,
				},
			},
		}); err != nil {
			return err
		}
	}

	return nil
}

func (g *GoMod) Finish(ctx context.Context, vendorEnabled bool) error {
	// sort replaces by  TODO: evaluat
	sort.Slice(g.replaces, func(i, j int) bool {
		return g.replaces[i].Priority < g.replaces[j].Priority
	})
	for _, replace := range g.replaces {
		if err := g.file.AddReplace(
			replace.Old.Path,
			replace.Old.Version,
			replace.New.Path,
			replace.New.Version,
		); err != nil {
			return err
		}
	}

	data, err := g.file.Format()
	if err != nil {
		return err
	}

	// Write go.mod
	if err := ioutil.WriteFile(g.path, data, 0); err != nil {
		return err
	}

	// Run go mod verify
	if err := command.New(ctx, "go", "mod", "verify").Run(); err != nil {
		return err
	}

	// Write vendor folder only do if configured to do so
	if vendorEnabled {
		if err := command.New(ctx, "go", "mod", "vendor").Run(); err != nil {
			return err
		}
	}

	return nil
}
