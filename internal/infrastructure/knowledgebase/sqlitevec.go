package knowledgebase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	domainkb "github.com/mikellxy/laxcode/internal/domain/knowledgebase"
)

type SQLiteVecRetriever struct {
	db *sql.DB
}

var _ domainkb.Retriever = (*SQLiteVecRetriever)(nil)

func init() {
	sqlitevec.Auto()
}

// NewSQLiteVecRetriever opens an existing sqlite-vec database in read-only mode.
func NewSQLiteVecRetriever(path string) (*SQLiteVecRetriever, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge base %q: %w", path, err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("knowledge base %q: %w", path, err)
	}
	dsn := (&url.URL{Scheme: "file", Path: absPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open knowledge base: %w", err)
	}
	r := &SQLiteVecRetriever{db: db}
	var version string
	if err := db.QueryRow("SELECT vec_version()").Scan(&version); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite-vec: %w", err)
	}
	return r, nil
}

func (r *SQLiteVecRetriever) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *SQLiteVecRetriever) Search(ctx context.Context, vector []float32, limit int) ([]domainkb.Chunk, error) {
	if len(vector) != domainkb.EmbeddingDimensions {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, want %d",
			len(vector), domainkb.EmbeddingDimensions)
	}
	if limit <= 0 {
		return nil, errors.New("retrieval limit must be positive")
	}
	serialized, err := sqlitevec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("serialize embedding: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT v.chunk_id, c.content, v.distance
		FROM vec_chunks AS v
		JOIN chunks AS c ON c.chunk_id = v.chunk_id
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`, serialized, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chunks := make([]domainkb.Chunk, 0, limit)
	for rows.Next() {
		var chunk domainkb.Chunk
		if err := rows.Scan(&chunk.ID, &chunk.Content, &chunk.Distance); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return chunks, nil
}
