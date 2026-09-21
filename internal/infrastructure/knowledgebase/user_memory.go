package knowledgebase

import (
	"context"
	"fmt"
	"math"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func NewUserMemoryRetriever(path, model string, dimensions int) (*SQLiteVecRetriever, error) {
	if dimensions <= 0 || dimensions > 8192 {
		return nil, fmt.Errorf("invalid user memory vector dimensions: %d", dimensions)
	}
	r, err := NewSQLiteVecRetriever(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*SQLiteVecRetriever, error) {
		r.Close()
		return nil, fmt.Errorf("user memory schema: %w", err)
	}
	var stored string
	var storedDimensions int
	if err := r.db.QueryRow("SELECT model,dimensions FROM user_memory_config WHERE id=1").Scan(&stored, &storedDimensions); err != nil {
		return fail(err)
	}
	if stored != model || storedDimensions != dimensions {
		return fail(fmt.Errorf("embedding model/dimension mismatch"))
	}
	r.vectorDimensions = dimensions
	if _, err := r.SearchUser(context.Background(), "schema-check", make([]float32, dimensions)); err != nil {
		return fail(err)
	}
	rows, err := r.db.Query("SELECT id,user_id,session_id,source_key,start_turn,end_turn,content FROM user_memory LIMIT 0")
	if err != nil {
		return fail(err)
	}
	rows.Close()
	return r, nil
}
func (r *SQLiteVecRetriever) SearchUser(ctx context.Context, user string, vector []float32) ([]sharedkernel.MemoryChunk, error) {
	if user == "" {
		return nil, fmt.Errorf("user ID is required")
	}
	if len(vector) != r.vectorDimensions {
		return nil, fmt.Errorf("embedding dimension mismatch: got %d, want %d", len(vector), r.vectorDimensions)
	}
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("invalid embedding value")
		}
	}
	blob, err := sqlitevec.SerializeFloat32(vector)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT v.chunk_id,c.content FROM user_memory_vectors v
 JOIN user_memory_chunk c ON c.chunk_id=v.chunk_id AND c.user_id=v.user_id
 WHERE v.embedding MATCH ? AND k=3 AND v.user_id=? ORDER BY v.distance`, blob, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sharedkernel.MemoryChunk
	for rows.Next() {
		var chunk sharedkernel.MemoryChunk
		if err := rows.Scan(&chunk.ID, &chunk.Content); err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, rows.Err()
}
