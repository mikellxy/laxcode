package ripgrep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/tools"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

// RootRunner opens searched contents through os.Root. rg receives only stdin,
// never an untrusted host pathname. Symlink entries are omitted from traversal.
// shortcut: cloud traversal ignores .gitignore; add rooted ignore processing
// if large repositories make this prototype path too expensive.
type RootRunner struct {
	root    *os.Root
	path    string
	homeDir string
	tempDir string
}

func NewRoot(path, homeDir, tempDir string) (*RootRunner, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &RootRunner{root: root, path: path, homeDir: homeDir, tempDir: tempDir}, nil
}
func (r *RootRunner) Close() error { return r.root.Close() }
func (r *RootRunner) relative(target string) (string, error) {
	rel, err := filepath.Rel(r.path, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside user root")
	}
	return rel, nil
}
func (r *RootRunner) Resolve(target string) (tools.RipgrepPathInfo, error) {
	rel, err := r.relative(target)
	if err != nil {
		return tools.RipgrepPathInfo{}, err
	}
	info, err := r.root.Stat(rel)
	if err != nil {
		return tools.RipgrepPathInfo{}, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return tools.RipgrepPathInfo{}, errors.New("path must be a regular file or directory")
	}
	return tools.RipgrepPathInfo{RealPath: target, IsDir: info.IsDir()}, nil
}

func globMatcher(glob string) (func(string) bool, error) {
	if glob == "" {
		return func(string) bool { return true }, nil
	}
	word := &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: glob}}}
	syntax.SplitBraces(word)
	var matchers []*regexp.Regexp
	for expanded, err := range expand.BracesSeq(nil, word) {
		if err != nil {
			return nil, err
		}
		if len(matchers) >= 256 {
			return nil, errors.New("too many glob alternatives")
		}
		literal, err := expand.Literal(nil, expanded)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(literal, "/") {
			literal = "**/" + literal
		}
		expression, err := pattern.Regexp(literal, pattern.Filenames|pattern.EntireString)
		if err != nil {
			return nil, err
		}
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return nil, err
		}
		matchers = append(matchers, compiled)
	}
	return func(name string) bool {
		for _, matcher := range matchers {
			if matcher.MatchString(filepath.ToSlash(name)) {
				return true
			}
		}
		return false
	}, nil
}

func (r *RootRunner) files(ctx context.Context, cwd, glob string, visit func(string, string) error) error {
	base, err := r.relative(cwd)
	if err != nil {
		return err
	}
	matches, err := globMatcher(glob)
	if err != nil {
		return err
	}
	return fs.WalkDir(r.root.FS(), filepath.ToSlash(base), func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		display, err := filepath.Rel(base, filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if !matches(display) {
			return nil
		}
		return visit(name, filepath.ToSlash(display))
	})
}

func (r *RootRunner) Search(ctx context.Context, cwd, file, expression, include string, limit int) (tools.GrepSearchResult, error) {
	result := tools.GrepSearchResult{Matches: []tools.GrepMatch{}}
	stopped := errors.New("search complete")
	search := func(name, display string) error {
		f, err := r.root.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("search target is not a regular file")
		}
		cmd := exec.CommandContext(ctx, "rg", "--no-config", "--json", "--no-messages", "--", expression, "-")
		cmd.Env = append(cmd.Environ(), "HOME="+r.homeDir, "TMPDIR="+r.tempDir, "TMP="+r.tempDir, "TEMP="+r.tempDir)
		cmd.Stdin = f
		found, err := runSearch(ctx, cmd, limit-len(result.Matches))
		if err != nil {
			return err
		}
		for _, match := range found.Matches {
			match.Path = display
			result.Matches = append(result.Matches, match)
		}
		if found.Truncated {
			result.Truncated = true
			return stopped
		}
		return nil
	}
	var err error
	if file == "." {
		err = r.files(ctx, cwd, include, search)
	} else {
		var rel string
		rel, err = r.relative(filepath.Join(cwd, file))
		if err == nil {
			var matches func(string) bool
			matches, err = globMatcher(include)
			if err == nil && matches(file) {
				err = search(rel, file)
			}
		}
	}
	if errors.Is(err, stopped) {
		err = nil
	}
	return result, err
}

func (r *RootRunner) Glob(ctx context.Context, cwd, glob string, limit int) (tools.GlobSearchResult, error) {
	result := tools.GlobSearchResult{Files: []string{}}
	stopped := errors.New("glob complete")
	err := r.files(ctx, cwd, glob, func(name, display string) error {
		// Revalidate after enumeration so a replaced entry cannot escape the root.
		info, err := r.root.Stat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if len(result.Files) == limit {
			result.Truncated = true
			return stopped
		}
		result.Files = append(result.Files, display)
		return nil
	})
	if errors.Is(err, stopped) {
		err = nil
	}
	return result, err
}
