package gomod

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"sort"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/grafana/go-mod-promote/pkg/api"
	"github.com/grafana/go-mod-promote/pkg/command"
	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
)

type GoMod struct {
	file     *modfile.File
	path     string
	logger   log.Logger
	replaces []api.GoModReplace
}

func NewGoModFromPath(path string) (*GoMod, error) {
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
		logger: log.NewNopLogger(),
	}, nil
}

func NewGoModFromContext(ctx context.Context) (*GoMod, error) {
	logger := gmpctx.LoggerFromContext(ctx)
	logger = log.With(logger, "module", "gomod")
	path := filepath.Join(gmpctx.RootPathFromContext(ctx), "go.mod")

	goMod, err := NewGoModFromPath(path)
	if err != nil {
		return nil, err
	}
	goMod.logger = logger

	return goMod, nil
}

func (g *GoMod) GetReplaces() []api.GoModReplace {
	replaces := make([]api.GoModReplace, len(g.file.Replace))
	for pos := range g.file.Replace {
		replaces[pos].Replace = *g.file.Replace[pos]
	}
	return replaces
}

func (g *GoMod) GetVersionForPackage(pkg string) (string, error) {

	for _, require := range g.file.Require {

		if require.Mod.Path == pkg {
			return require.Mod.Version, nil
		}
	}

	return "", fmt.Errorf("package %s not found", pkg)

}

func (g *GoMod) AddReplace(r api.GoModReplace) error {
	logger := log.With(g.logger, "pkg", r.New.Path, "version", r.New.Version)
	level.Debug(logger).Log("msg", "added replace")
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
		if err := g.AddReplace(api.GoModReplace{
			Replace: modfile.Replace{
				Old: module.Version{
					Path: pkg,
				},
				New: module.Version{
					Path:    pkg,
					Version: version,
				},
			},
			Priority: api.GoModReplacePriorityManagedPackage,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (g *GoMod) addReplace(input api.GoModReplace) error {
	// add as normal
	if err := g.file.AddReplace(input.Old.Path, input.Old.Version, input.New.Path, input.New.Version); err != nil {
		return err
	}

	// if comment is empty we are finished
	if input.Comment == "" {
		return nil
	}

	// iterate throug entries

	for _, r := range g.file.Replace {
		if r.Old.Path == input.Old.Path && (input.Old.Version == "" || r.Old.Version == input.Old.Version) {

			if r.Syntax == nil {
				r.Syntax = &modfile.Line{}
			}

			r.Syntax.Before = []modfile.Comment{{
				Token: "// [go-mod-promote] " + input.Comment,
			}}

			return nil
		}
	}

	return fmt.Errorf("error entry was not found to add comment")
}

func (g *GoMod) Finish(ctx context.Context, vendorEnabled bool) error {
	// sort replaces by priority
	sort.Slice(g.replaces, func(i, j int) bool {
		return g.replaces[i].Priority < g.replaces[j].Priority
	})

	// add replaces as necessary
	for _, replace := range g.replaces {
		if err := g.addReplace(replace); err != nil {
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
