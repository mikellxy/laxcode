package agentasm

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestAssembleRAGUsesExplicitDatabaseOutsideWorkDir(t *testing.T) {
	workdir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "custom vectors.sqlite")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE fixture(id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	a, err := Assemble(context.Background(), Input{Mode: ModeRAG, WorkDir: workdir, HomeDir: t.TempDir(), SessionID: "rag-custom", KBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cleanup()
	if _, err := os.Stat(filepath.Join(workdir, "kb")); !os.IsNotExist(err) {
		t.Fatalf("default database directory accessed: %v", err)
	}
}
func TestAssembleRAGRejectsMissingOrRelativeDatabase(t *testing.T) {
	for _, path := range []string{"", "kb/test.sqlite"} {
		if _, err := Assemble(context.Background(), Input{Mode: ModeRAG, WorkDir: t.TempDir(), HomeDir: t.TempDir(), KBPath: path}); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
