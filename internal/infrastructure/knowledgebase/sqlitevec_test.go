package knowledgebase

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	domainkb "github.com/mikellxy/laxcode/internal/domain/knowledgebase"
)

func TestSQLiteVecRetrieverSearchesByDistance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kb.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE documents (id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL DEFAULT (datetime('now')))`,
		`CREATE TABLE chunks (chunk_id TEXT PRIMARY KEY, document_id INTEGER NOT NULL REFERENCES documents(id), chunk_seq INTEGER NOT NULL, content TEXT NOT NULL, title TEXT NOT NULL DEFAULT '')`,
		`CREATE VIRTUAL TABLE chunk_vectors USING vec0(chunk_id TEXT PRIMARY KEY, embedding FLOAT[1024])`,
		`INSERT INTO documents(path) VALUES ('doc-a')`,
		`INSERT INTO chunks(chunk_id, document_id, chunk_seq, content) VALUES ('chunk-1', 1, 0, 'nearest'), ('chunk-2', 1, 1, 'farther')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	first := make([]float32, domainkb.EmbeddingDimensions)
	first[0] = 1
	second := make([]float32, domainkb.EmbeddingDimensions)
	second[1] = 1
	for id, vector := range [][]float32{first, second} {
		encoded, err := sqlitevec.SerializeFloat32(vector)
		if err != nil {
			t.Fatal(err)
		}
		chunkID := fmt.Sprintf("chunk-%d", id+1)
		if _, err := db.Exec(`INSERT INTO chunk_vectors(chunk_id, embedding) VALUES (?, ?)`, chunkID, encoded); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	retriever, err := NewSQLiteVecRetriever(path)
	if err != nil {
		t.Fatal(err)
	}
	defer retriever.Close()
	chunks, err := retriever.Search(context.Background(), first, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].ID != "chunk-1" || chunks[0].Content != "nearest" || chunks[0].Distance != 0 {
		t.Fatalf("unexpected search result: %+v", chunks)
	}
}

func TestWorkspaceKnowledgeBaseIsCompatible(t *testing.T) {
	path := filepath.Join("..", "..", "..", "kb", "kb.sqlite")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			t.Skip("workspace knowledge base is not present")
		}
		t.Fatal(err)
	}
	retriever, err := NewSQLiteVecRetriever(path)
	if err != nil {
		t.Fatal(err)
	}
	defer retriever.Close()
	query := make([]float32, domainkb.EmbeddingDimensions)
	query[0] = 1
	if _, err := retriever.Search(context.Background(), query, 10); err != nil {
		t.Fatal(err)
	}
}
