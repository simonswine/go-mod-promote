package context

import (
	"context"

	"github.com/go-kit/kit/log"

	"github.com/grafana/go-mod-promote/pkg/api"
)

type contextKey int

const (
	contextKeyGoModBefore contextKey = iota
	contextKeyGoModAfter
	contextKeyRootPath
	contextKeyLogger
	contextKeyGoModFile
)

func GoModBeforeIntoContext(ctx context.Context, b *api.GoModDownloadResult) context.Context {
	return context.WithValue(ctx, contextKeyGoModBefore, b)
}

func GoModBeforeFromContext(ctx context.Context) *api.GoModDownloadResult {
	return ctx.Value(contextKeyGoModBefore).(*api.GoModDownloadResult)
}

func GoModAfterIntoContext(ctx context.Context, b *api.GoModDownloadResult) context.Context {
	return context.WithValue(ctx, contextKeyGoModAfter, b)
}

func GoModAfterFromContext(ctx context.Context) *api.GoModDownloadResult {
	return ctx.Value(contextKeyGoModAfter).(*api.GoModDownloadResult)
}

func RootPathIntoContext(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, contextKeyRootPath, v)
}

func RootPathFromContext(ctx context.Context) string {
	return ctx.Value(contextKeyRootPath).(string)
}

func LoggerIntoContext(ctx context.Context, v log.Logger) context.Context {
	return context.WithValue(ctx, contextKeyLogger, v)
}

func LoggerFromContext(ctx context.Context) log.Logger {
	l, ok := ctx.Value(contextKeyLogger).(log.Logger)
	if !ok {
		return log.NewNopLogger()
	}

	return l
}

type GoModFile interface {
	AddReplace(api.GoModReplace) error
}

func GoModFileIntoContext(ctx context.Context, b GoModFile) context.Context {
	return context.WithValue(ctx, contextKeyGoModFile, b)
}

func GoModFileFromContext(ctx context.Context) GoModFile {
	return ctx.Value(contextKeyGoModFile).(GoModFile)
}
