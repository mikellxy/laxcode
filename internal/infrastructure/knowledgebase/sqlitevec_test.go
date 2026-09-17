package knowledgebase

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	domainkb "github.com/mikellxy/laxcode/internal/domain/knowledgebase"
)

func TestSQLiteVecRetrieverSearchesByCosineDistance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kb.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE chunks (chunk_id INTEGER PRIMARY KEY, content TEXT NOT NULL)`,
		`CREATE VIRTUAL TABLE vec_chunks USING vec0(chunk_id INTEGER PRIMARY KEY, embedding FLOAT[1024] DISTANCE_METRIC=cosine)`,
		`INSERT INTO chunks(chunk_id, content) VALUES (1, 'nearest'), (2, 'farther')`,
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
		if _, err := db.Exec(`INSERT INTO vec_chunks(chunk_id, embedding) VALUES (?, ?)`, id+1, encoded); err != nil {
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
	if len(chunks) != 2 || chunks[0].ID != 1 || chunks[0].Content != "nearest" || chunks[0].Distance != 0 {
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
