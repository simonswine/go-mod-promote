package api

import (
	"strings"

	"golang.org/x/mod/modfile"
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

type GoModReplacePriority int32

const (
	GoModReplacePriorityManagedPackage = GoModReplacePriority(1000)
	GoModReplaceUpstreamPackageVersion = GoModReplacePriority(400)
	GoModReplaceUpstreamReplace        = GoModReplacePriority(200)
)

type GoModReplace struct {
	modfile.Replace
	// Higher Priority values overwrite lower priority ones
	Priority GoModReplacePriority
	// If Comment is not empty, go-mod-promote will actively manage the entry
	// (e.g. remove it after it has been removed upstream)
	Comment string
}
