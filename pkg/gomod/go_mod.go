package gomod

import (
	"fmt"
	"io/ioutil"

	"golang.org/x/mod/modfile"

	"github.com/grafana/go-mod-promote/pkg/command"
)

type GoMod struct {
	file *modfile.File
	path string
}

func NewGoModFromPath(path string) (*GoMod, error) {
	goModData, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	goMod, err := modfile.ParseLax("go.mod", goModData, nil)
	if err != nil {
		return nil, err
	}

	return &GoMod{
		file: goMod,
		path: path,
	}, nil
}

func (g *GoMod) UpdatePackage(pkg, version string) error {

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
		g.file.AddReplace(
			pkg,
			version,
			pkg,
			version,
		)
	}

	return fmt.Errorf("todo %+v", g.file.Replace)
}

func (g *GoMod) Finish() error {

	data, err := g.file.Format()
	if err != nil {
		return err
	}

	// Write go.mod
	if err := ioutil.WriteFile(g.path, data, 0); err != nil {
		return err
	}

	// Run go mod tidy
	if err := command.New(ctx, "go", "mod", "tidy").Run(); err != nil {
		return err
	}

	// Write vendor/
	// TODO: Only do if configured to do
	if err := command.New(ctx, "go", "mod", "vendor").Run(); err != nil {
		return err
	}

	return nil
}
