package ripgrep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

const (
	maxRecordBytes = 64 * 1024
	maxErrorBytes  = 8 * 1024
)

type Runner struct{}

func New() *Runner { return &Runner{} }

var _ tools.RipgrepRunner = (*Runner)(nil)

func (*Runner) Resolve(absPath string) (tools.RipgrepPathInfo, error) {
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return tools.RipgrepPathInfo{}, err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return tools.RipgrepPathInfo{}, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return tools.RipgrepPathInfo{}, fmt.Errorf("path %q is not a regular file or directory", absPath)
	}
	return tools.RipgrepPathInfo{RealPath: realPath, IsDir: info.IsDir()}, nil
}

type rawMatch struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		Line int `json:"line_number"`
	} `json:"data"`
}

func (*Runner) Search(ctx context.Context, cwd, file, pattern, include string, limit int) (tools.GrepSearchResult, error) {
	args := []string{"--no-config", "--json", "--hidden", "--no-messages"}
	if include != "" {
		args = append(args, "--glob="+include)
	}
	args = append(args, "--glob=!**/.git/**", "--", pattern, file)
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return tools.GrepSearchResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return tools.GrepSearchResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return tools.GrepSearchResult{}, fmt.Errorf("start rg: %w", err)
	}

	var errorOutput bytes.Buffer
	stderrDone := make(chan error, 1)
	go func() {
		_, firstErr := io.CopyN(&errorOutput, stderr, maxErrorBytes)
		if firstErr != nil && !errors.Is(firstErr, io.EOF) {
			stderrDone <- firstErr
			return
		}
		_, restErr := io.Copy(io.Discard, stderr)
		stderrDone <- restErr
	}()

	result := tools.GrepSearchResult{Matches: make([]tools.GrepMatch, 0, min(limit, 100))}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxRecordBytes+2)
	for scanner.Scan() {
		record := scanner.Bytes()
		if len(record) > maxRecordBytes {
			err = fmt.Errorf("ripgrep JSON record exceeded %d bytes", maxRecordBytes)
			break
		}
		var row rawMatch
		if err = json.Unmarshal(record, &row); err != nil {
			err = fmt.Errorf("invalid ripgrep JSON output: %w", err)
			break
		}
		if row.Type != "match" {
			continue
		}
		if row.Data.Path.Text == "" || row.Data.Line < 1 {
			err = errors.New("invalid ripgrep match output")
			break
		}
		path := strings.TrimPrefix(row.Data.Path.Text, "."+string(filepath.Separator))
		result.Matches = append(result.Matches, tools.GrepMatch{
			Path: path,
			Line: row.Data.Line,
			Text: row.Data.Lines.Text,
		})
		if len(result.Matches) > limit {
			result.Matches = result.Matches[:limit]
			result.Truncated = true
			break
		}
	}
	if err == nil {
		err = scanner.Err()
	}
	if err != nil || result.Truncated {
		_ = cmd.Process.Kill()
	}
	stderrErr := <-stderrDone
	waitErr := cmd.Wait()
	if err != nil {
		return tools.GrepSearchResult{}, err
	}
	if ctx.Err() != nil {
		return tools.GrepSearchResult{}, ctx.Err()
	}
	if stderrErr != nil && !result.Truncated {
		return tools.GrepSearchResult{}, stderrErr
	}
	if result.Truncated {
		return result, nil
	}
	if waitErr == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) {
		return tools.GrepSearchResult{}, waitErr
	}
	if exit.ExitCode() == 1 {
		return result, nil
	}
	message := strings.TrimSpace(errorOutput.String())
	if exit.ExitCode() == 2 && !strings.Contains(message, "regex parse error") && !strings.Contains(message, "error parsing regex") {
		return result, nil
	}
	if message == "" {
		message = waitErr.Error()
	}
	return tools.GrepSearchResult{}, fmt.Errorf("rg failed: %s", message)
}

func (*Runner) Glob(ctx context.Context, cwd, pattern string, limit int) (tools.GlobSearchResult, error) {
	args := []string{"--no-config", "--files", "--glob=" + pattern, "--glob=!**/.git/**", "."}
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = cwd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return tools.GlobSearchResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return tools.GlobSearchResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return tools.GlobSearchResult{}, fmt.Errorf("start rg: %w", err)
	}

	var errorOutput bytes.Buffer
	stderrDone := make(chan error, 1)
	go func() {
		_, firstErr := io.CopyN(&errorOutput, stderr, maxErrorBytes)
		if firstErr != nil && !errors.Is(firstErr, io.EOF) {
			stderrDone <- firstErr
			return
		}
		_, restErr := io.Copy(io.Discard, stderr)
		stderrDone <- restErr
	}()

	result := tools.GlobSearchResult{Files: make([]string, 0, min(limit, 100))}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxRecordBytes+2)
	for scanner.Scan() {
		file := scanner.Text()
		file = strings.TrimPrefix(file, "."+string(filepath.Separator))
		if file == "" {
			continue
		}
		result.Files = append(result.Files, filepath.ToSlash(file))
		if len(result.Files) > limit {
			result.Files = result.Files[:limit]
			result.Truncated = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		<-stderrDone
		_ = cmd.Wait()
		return tools.GlobSearchResult{}, err
	}
	if result.Truncated {
		_ = cmd.Process.Kill()
	}
	stderrErr := <-stderrDone
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return tools.GlobSearchResult{}, ctx.Err()
	}
	if stderrErr != nil && !result.Truncated {
		return tools.GlobSearchResult{}, stderrErr
	}
	if result.Truncated || waitErr == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) {
		return tools.GlobSearchResult{}, waitErr
	}
	if exit.ExitCode() == 1 || exit.ExitCode() == 2 {
		return result, nil
	}
	message := strings.TrimSpace(errorOutput.String())
	if message == "" {
		message = waitErr.Error()
	}
	return tools.GlobSearchResult{}, fmt.Errorf("rg failed: %s", message)
}
