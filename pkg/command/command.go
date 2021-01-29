package command

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
)

type Cmd struct {
	*exec.Cmd

	logger log.Logger
	Stdout bytes.Buffer
	Stderr bytes.Buffer
}

func New(ctx context.Context, command string, args ...string) *Cmd {
	c := &Cmd{
		Cmd: exec.CommandContext(ctx, command, args...),

		logger: log.With(gmpctx.LoggerFromContext(ctx), "command", fmt.Sprintf("%v", append([]string{command}, args...))),
	}

	c.Cmd.Stdout = &c.Stdout
	c.Cmd.Stderr = &c.Stderr

	return c

}

func (c *Cmd) Start() error {
	level.Debug(c.logger).Log("msg", "Started execution")
	if err := c.Cmd.Start(); err != nil {
		return err
	}

	return nil
}

func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()
	logger := c.logger
	if err != nil {
		logger = log.With(logger, "err", err)
	}
	level.Debug(logger).Log("msg", "Finished execution")
	return err
}

func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}
