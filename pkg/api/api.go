package api

import (
	"strings"

	"golang.org/x/mod/semver"
)

type GoModVersion string

func (v GoModVersion) Release() string {
	prerelease := semver.Prerelease(string(v))
	version := semver.Canonical(string(v))
	version = version[:len(version)-len(prerelease)]
	return version
}

func (v GoModVersion) Hash() string {
	prerelease := semver.Prerelease(string(v))
	pos := strings.LastIndex(prerelease, "-") + 1
	return prerelease[pos:]
}

type GoModDownloadResult struct {
	GoMod   string
	Path    string
	Version GoModVersion
	Dir     string
}
