package knowledgebase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Set LAX_MEMORY_TEST_PYTHON to the knowledge project's Python environment.
// Uses deterministic embeddings and a real SQLite file, never a paid model.
func TestPythonMemoryDatabaseCompatibility(t *testing.T) {
	python := os.Getenv("LAX_MEMORY_TEST_PYTHON")
	if python == "" {
		t.Skip("set LAX_MEMORY_TEST_PYTHON for cross-language integration")
	}
	db := filepath.Join(t.TempDir(), "kb.sqlite")
	script := `import sys
from laxcode_knowledge.user_memory import UserMemoryWriter
w=UserMemoryWriter(sys.argv[1], 'test-model', 1024)
for user,session,value in [('11111111-1111-4111-8111-111111111111','a',1.),('22222222-2222-4222-8222-222222222222','b',0.)]:
 for n in range(1 if session=='a' else 5):
  sid=session+str(n)
  data=dict(user_id=user,session_id=sid,source_key=sid+':react:3',start_turn=1,end_turn=3,content='# Preference\n'+session)
  w.ingest(data,lambda texts:[[value]+[0.]*1023 for _ in texts])
w.close()
`
	cmd := exec.Command(python, "-c", script, db)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python: %s %v", out, err)
	}
	r, err := NewUserMemoryRetriever(db, "test-model", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// The other user's vector is closer: filtering after global top-k is incorrect.
	for _, user := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		chunks, err := r.SearchUser(context.Background(), user, make([]float32, 1024))
		if err != nil {
			t.Fatal(err)
		}
		expected := 1
		if user[0] == '2' {
			expected = 3
		}
		if len(chunks) != expected {
			t.Fatalf("chunks=%v", chunks)
		}
	}
	if _, err := NewUserMemoryRetriever(db, "wrong-model", 1024); err == nil {
		t.Fatal("accepted mismatched model")
	}
	if _, err := NewUserMemoryRetriever(db, "test-model", 1536); err == nil {
		t.Fatal("accepted mismatched dimensions")
	}
}
