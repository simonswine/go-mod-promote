package main

import (
	"context"
	stdlog "log"
	"os"

	"github.com/go-kit/kit/log"

	gmpapp "github.com/grafana/go-mod-promote/pkg/app"
)

func main() {
	var logger log.Logger
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC, "caller", log.DefaultCaller)
	stdlog.SetOutput(log.NewStdlibAdapter(logger))

	app, err := gmpapp.New(gmpapp.WithLogger(logger))
	if err != nil {
		stdlog.Fatalf("error creating app: %v", err)
	}

	ctx := context.Background()
	err = app.Run(ctx)
	if err != nil {
		stdlog.Fatalf("error running app: %v", err)
	}
}
