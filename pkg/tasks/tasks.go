package tasks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/go-kit/kit/log/level"
	"github.com/hashicorp/go-multierror"

	"github.com/grafana/go-mod-promote/pkg/command"
	gmpctx "github.com/grafana/go-mod-promote/pkg/context"
	gmperr "github.com/grafana/go-mod-promote/pkg/errors"
)

type Patch struct {
	Body []byte
}

type PatchError struct {
	Upstream error
	Reject   []byte
	msg      string
}

func (p *PatchError) Error() string {
	return p.msg
}

func (p *Patch) Apply(ctx context.Context) error {
	logger := gmpctx.LoggerFromContext(ctx)

	rejectFile, err := ioutil.TempFile("", "reject")
	if err != nil {
		return err
	}
	defer os.Remove(rejectFile.Name())
	if err := rejectFile.Close(); err != nil {
		return err
	}

	c := command.New(ctx, "patch",
		"--strip", "1", // remove the first directory of the patch paths
		"--reject-file", rejectFile.Name(), // if patch doesn't apply, parts that did not work are stored there
		"--no-backup-if-mismatch", // avoid backing up the original files
	)
	stdin, err := c.StdinPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}

	if _, err := stdin.Write(p.Body); err != nil {
		return err
	}
	if err := stdin.Close(); err != nil {
		return err
	}

	if err := c.Wait(); err != nil {
		err = fmt.Errorf("error applying patch: %w stdout=[%s] stderr=[%s]", err, c.Stdout.String(), c.Stderr.String())
		if c.ExitCode == 1 {
			rejectBody, rerr := ioutil.ReadFile(rejectFile.Name())
			if rerr != nil {
				level.Warn(logger).Log("msg", "Unable to read rejects file", "err", rerr)
				// return original patch error
				return err
			}

			if len(rejectBody) == 0 {
				return err
			}

			return &PatchError{
				Upstream: err,
				Reject:   rejectBody,
				msg:      c.Stdout.String(),
			}

		}
		return err
	}

	return nil
}

type Copy struct {
	Source      string
	Destination string // relative path to root
}

func (c *Copy) Apply(ctx context.Context) error {
	sourceFileStat, err := os.Stat(c.Source)
	if err != nil {
		return err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", c.Source)
	}

	source, err := os.Open(c.Source)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(c.Destination)
	if err != nil {
		return err
	}
	defer destination.Close()
	_, err = io.Copy(destination, source)
	return err
}

type Delete string

func (d Delete) Apply(ctx context.Context) error {
	filePath := string(d)
	fileStat, err := os.Stat(filePath)
	if err != nil {
		return err
	}

	if !fileStat.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", filePath)
	}

	return os.Remove(filePath)
}

type Result struct {
	FilesToCopy   []Copy
	FilesToDelete []Delete // relative path to root

	Patches []Patch
}

func (r *Result) IsEmpty() bool {
	if len(r.FilesToCopy) > 0 {
		return false
	}
	if len(r.FilesToDelete) > 0 {
		return false
	}
	if len(r.Patches) > 0 {
		return false
	}

	return true
}

func (r *Result) Apply(ctx context.Context) error {
	logger := gmpctx.LoggerFromContext(ctx)

	var result error

	for pos, patch := range r.Patches {
		if err := patch.Apply(ctx); err != nil {
			result = multierror.Append(result, err)
			continue
		}
		level.Info(logger).Log("msg", fmt.Sprintf("applied Patch[%d] successfully", pos))
	}

	for _, toDelete := range r.FilesToDelete {
		if err := toDelete.Apply(ctx); err != nil {
			result = multierror.Append(result, err)
			continue
		}
		level.Info(logger).Log("msg", fmt.Sprintf("deleted '%s' successfully", toDelete))
	}

	for _, toCopy := range r.FilesToCopy {
		if err := toCopy.Apply(ctx); err != nil {
			result = multierror.Append(result, err)
			continue
		}
		level.Info(logger).Log("msg", fmt.Sprintf("copied '%s' successfully", toCopy))
	}

	return result
}

func AggregateResult(results ...*Result) *Result {
	var aggregate Result
	for _, r := range results {
		if r == nil {
			continue
		}
		aggregate.FilesToCopy = append(aggregate.FilesToCopy, r.FilesToCopy...)
		aggregate.FilesToDelete = append(aggregate.FilesToDelete, r.FilesToDelete...)
		aggregate.Patches = append(aggregate.Patches, r.Patches...)
	}

	return &aggregate
}

type taskRunner interface {
	run(ctx context.Context) (*Result, error)
}

type Task struct {
	SyncDirectory *TaskSyncDirectory `yaml:"sync_directory"`
	Diff          *TaskDiff          `yaml:"diff"`
	GoModReplace  *TaskGoModReplace  `yaml:"go_mod_replace"`
	Regexp        *TaskRegexp        `yaml:"regexp"`
}

func (t *Task) Run(ctx context.Context) (*Result, error) {
	var runners []taskRunner

	if t.SyncDirectory != nil {
		runners = append(runners, t.SyncDirectory)
	}

	if t.Diff != nil {
		runners = append(runners, t.Diff)
	}

	if t.GoModReplace != nil {
		runners = append(runners, t.GoModReplace)
	}

	if t.Regexp != nil {
		runners = append(runners, t.Regexp)
	}

	if len(runners) == 0 {
		return nil, fmt.Errorf("No task implementation specified")
	}
	if len(runners) > 1 {
		return nil, fmt.Errorf("More than one task implementations specified")
	}

	return runners[0].run(ctx)
}

type Regexp struct {
	Path   string `yaml:"path"`
	Regexp string `yaml:"regexp"`
}

type RegexpDestination struct {
	Regexp `yaml:"inline"`
	Value  string `yaml:"value"`
}

type TaskRegexp struct {
	Source       Regexp   `yaml:"source"`
	Destinations []Regexp `yaml:"destinations"`
}

func (t *TaskRegexp) run(ctx context.Context) (*Result, error) {
	logger := gmpctx.LoggerFromContext(ctx)

	sourceRe, err := regexp.Compile(t.Source.Regexp)
	if err != nil {
		return nil, err
	}

	after := gmpctx.GoModAfterFromContext(ctx)
	sourcePath := filepath.Join(after.Dir, t.Source.Path)
	sourceData, err := ioutil.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}

	m := sourceRe.FindSubmatch(sourceData)
	if len(m) == 0 {
		return nil, fmt.Errorf("regexp '%s' doesn't match content of '%s'", sourceRe, t.Source.Path)
	}

	for pos := range m {
		level.Debug(logger).Log("msg", fmt.Sprintf("regexp '%s' submatches[%d]: '%s'", sourceRe, pos, m[pos]))
	}

	return nil, nil
}

type TaskGoModReplace struct {
	Name string `yaml:"name"`
}

func (t *TaskGoModReplace) run(ctx context.Context) (*Result, error) {
	return nil, gmperr.ErrNotImplemented{}
}

type TaskDiff struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
}

func (t *TaskDiff) run(ctx context.Context) (*Result, error) {

	before := gmpctx.GoModBeforeFromContext(ctx)
	after := gmpctx.GoModAfterFromContext(ctx)

	cmd := command.New(ctx, "diff",
		"-u",
		filepath.Join(before.Dir, t.Source),
		filepath.Join(after.Dir, t.Source),
	)

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 1 {
			return nil, err
		}
	}

	var diff []byte

	scanner := bufio.NewScanner(&cmd.Stdout)
	for scanner.Scan() {
		b := scanner.Bytes()
		var path string

		// if +++ or --- line rewrite the file paths
		if bytes.HasPrefix(b, []byte("+++")) {
			path = "new"
		} else if bytes.HasPrefix(b, []byte("---")) {
			path = "old"
		} else {
			diff = append(diff, b...)
			diff = append(diff, byte('\n'))
			continue
		}

		path = filepath.Join(path, t.Destination)

		diff = append(diff, append(
			b[:4],
			path...,
		)...)

		// add everything after the path in the original line
		offset := 3
		pos := bytes.IndexRune(b[offset:], '\t')
		if pos > 0 {
			pos += offset
			diff = append(diff, b[pos:]...)
		}

		diff = append(diff, byte('\n'))
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return &Result{
		Patches: []Patch{
			{
				Body: diff,
			},
		},
	}, nil
}

type TaskSyncDirectory struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
	Glob        string `yaml:"glob"`
	Recursive   *bool  `yaml:"recursive"`
}

func hash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func (t *TaskSyncDirectory) walkDirectory(dirPath string, m map[string]string) error {
	if err := filepath.Walk(dirPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if f.IsDir() {
			return nil
		}

		baseName := filepath.Base(path)

		relPath, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		if t.Recursive != nil && !*t.Recursive {
			if baseName != relPath {
				return nil
			}
		}

		if t.Glob != "" {
			if match, err := filepath.Match(t.Glob, baseName); err != nil {
				return err
			} else if !match {
				return nil
			}
		}

		m[relPath] = ""
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (t *TaskSyncDirectory) run(ctx context.Context) (*Result, error) {
	logger := gmpctx.LoggerFromContext(ctx)
	level.Info(logger).Log("msg", "sync task", "source", t.Source, "destination", t.Destination)

	after := gmpctx.GoModAfterFromContext(ctx)

	sourcePath := filepath.Join(after.Dir, t.Source)
	destinationPath := filepath.Join(gmpctx.RootPathFromContext(ctx), t.Destination)

	sourceFiles := make(map[string]string)
	destinationFiles := make(map[string]string)

	if err := t.walkDirectory(sourcePath, sourceFiles); err != nil {
		return nil, err
	}
	if err := t.walkDirectory(destinationPath, destinationFiles); err != nil {
		return nil, err
	}

	var result Result

	for filePath := range sourceFiles {
		if _, ok := destinationFiles[filePath]; ok {
			// exists in dest
			var err error
			sourceFiles[filePath], err = hash(filepath.Join(sourcePath, filePath))
			if err != nil {
				return nil, err
			}
		} else {
			result.FilesToCopy = append(result.FilesToCopy, Copy{
				Source:      filepath.Join(sourcePath, filePath),
				Destination: filepath.Join(t.Destination, filePath),
			})
		}
	}

	for filePath := range destinationFiles {
		if hashSource, ok := sourceFiles[filePath]; ok {
			// exists in dest
			var err error
			destinationFiles[filePath], err = hash(filepath.Join(destinationPath, filePath))
			if err != nil {
				return nil, err
			}

			if destinationFiles[filePath] != hashSource {
				result.FilesToCopy = append(result.FilesToCopy, Copy{
					Source:      filepath.Join(sourcePath, filePath),
					Destination: filepath.Join(t.Destination, filePath),
				})
			}
		} else {
			result.FilesToDelete = append(result.FilesToDelete, Delete(filepath.Join(t.Destination, filePath)))
		}
	}

	return &result, nil //cmd.Run()

}
