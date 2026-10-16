package artifactstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

type FileStore struct {
	Root     string
	UserRoot string
}

func validSessionID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid session ID")
	}
	return nil
}

func artifactID(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (s *FileStore) PutArtifact(ctx context.Context, sessionID, content string) (sharedkernel.ArtifactRef, error) {
	if err := validSessionID(sessionID); err != nil {
		return sharedkernel.ArtifactRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return sharedkernel.ArtifactRef{}, err
	}
	if !utf8.ValidString(content) {
		return sharedkernel.ArtifactRef{}, fmt.Errorf("artifact content is not valid UTF-8")
	}
	ref := sharedkernel.ArtifactRef{ID: artifactID([]byte(content)), ByteSize: len(content)}
	return s.putRooted(sessionID, content, ref)
}

func (s *FileStore) ReadArtifact(ctx context.Context, sessionID, id string, offset, limit int) (tools.ArtifactPage, error) {
	var page tools.ArtifactPage
	if err := validSessionID(sessionID); err != nil {
		return page, err
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != id {
		return page, fmt.Errorf("invalid artifact ID")
	}
	if offset < 0 || limit < 1 || limit > tools.MaxArtifactPageRunes {
		return page, fmt.Errorf("invalid artifact page range")
	}
	root, err := s.artifactRoot(sessionID)
	if err != nil {
		return page, fmt.Errorf("open session artifacts: %w", err)
	}
	defer root.Close()
	f, err := root.Open(id)
	if err != nil {
		return page, fmt.Errorf("read artifact %s: %w", id, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return page, fmt.Errorf("read artifact %s: %w", id, err)
	}
	if artifactID(data) != id {
		return page, fmt.Errorf("artifact digest mismatch: %s", id)
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	runes := []rune(string(data))
	if offset > len(runes) {
		return page, fmt.Errorf("artifact offset beyond end (%d)", len(runes))
	}
	end := offset + min(limit, len(runes)-offset)
	return tools.ArtifactPage{
		ID: id, Content: string(runes[offset:end]), Offset: offset,
		NextOffset: end, TotalRunes: len(runes), EOF: end == len(runes),
	}, nil
}

var _ tools.ArtifactStore = (*FileStore)(nil)

func NewWithRoot(root, userRoot string) *FileStore { return &FileStore{Root: root, UserRoot: userRoot} }
func (s *FileStore) artifactRoot(sessionID string) (*os.Root, error) {
	target := filepath.Join(s.Root, sessionID, "artifacts")

	root, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(s.UserRoot, target)
	if err != nil {
		return nil, err
	}
	return root.OpenRoot(rel)
}
func (s *FileStore) putRooted(sessionID, content string, ref sharedkernel.ArtifactRef) (sharedkernel.ArtifactRef, error) {
	root, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		return ref, err
	}
	defer root.Close()
	dir, err := filepath.Rel(s.UserRoot, filepath.Join(s.Root, sessionID, "artifacts"))
	if err != nil {
		return ref, err
	}
	if err := root.MkdirAll(dir, 0700); err != nil {
		return ref, err
	}
	name := filepath.Join(dir, ref.ID)
	if data, err := root.ReadFile(name); err == nil {
		if artifactID(data) != ref.ID {
			return ref, fmt.Errorf("artifact digest mismatch")
		}
		return ref, nil
	} else if !os.IsNotExist(err) {
		return ref, err
	}
	temp := filepath.Join(dir, "artifact-"+rand.Text())
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ref, err
	}
	defer root.Remove(temp)
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return ref, err
	}
	if err := f.Sync(); err != nil {
		return ref, err
	}
	if err := f.Close(); err != nil {
		return ref, err
	}
	if err := root.Rename(temp, name); err != nil {
		return ref, err
	}
	d, err := root.Open(dir)
	if err != nil {
		return ref, err
	}
	defer d.Close()
	return ref, d.Sync()
}
