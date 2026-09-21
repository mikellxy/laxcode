package run_sse

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func TestMissingMemoryEnvironmentDoesNotRequirePythonOrVectorDatabase(t *testing.T) {
	keys := []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"}
	for _, key := range keys {
		t.Setenv(key, "configured")
	}
	savedCLI := config.CliConf
	t.Cleanup(func() { config.CliConf = savedCLI })
	config.CliConf.KB = ""
	config.CliConf.VectorDimensions = 0
	saved := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = saved })
	// Neither the database nor a configured Python entry point is required in disabled mode.
	config.EnvAndFileConf.UserMemoryExecutable = "/nonexistent/knowledge-pipeline"
	for _, missing := range keys {
		t.Run(missing, func(t *testing.T) {
			t.Setenv(missing, "")
			dir := t.TempDir()
			repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(dir), layout.SessionRoot(dir))
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			cleanup, err := newServer(dir, false).startUserMemory(repo)
			if err != nil {
				t.Fatal(err)
			}
			cleanup()
			if _, err := os.Stat(filepath.Join(dir, "kb")); !os.IsNotExist(err) {
				t.Fatalf("disabled memory accessed vector database: %v", err)
			}
		})
	}
}

func TestSSEMemoryUsesExplicitDatabaseOutsideWorkDir(t *testing.T) {
	savedCLI, savedConfig := config.CliConf, config.EnvAndFileConf
	t.Cleanup(func() { config.CliConf = savedCLI; config.EnvAndFileConf = savedConfig })
	for _, key := range []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"} {
		t.Setenv(key, "test")
	}
	workdir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "shared vectors.sqlite")
	initArgsPath := filepath.Join(t.TempDir(), "init-args")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE user_memory_config(id INTEGER PRIMARY KEY,model TEXT,dimensions INTEGER)`,
		`INSERT INTO user_memory_config VALUES(1,'test',1024)`,
		`CREATE TABLE user_memory(id TEXT,user_id TEXT,session_id TEXT,source_key TEXT,start_turn INTEGER,end_turn INTEGER,content TEXT)`,
		`CREATE TABLE user_memory_chunk(chunk_id TEXT,user_id TEXT,content TEXT)`,
		`CREATE VIRTUAL TABLE user_memory_vectors USING vec0(chunk_id TEXT PRIMARY KEY,user_id TEXT,embedding FLOAT[1024])`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(t.TempDir(), "fake-memory-pipeline")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > \"" + initArgsPath + "\"\nprintf '{\"status\":\"initialized\"}\\n'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	config.CliConf.KB = dbPath
	config.CliConf.VectorDimensions = 1024
	config.EnvAndFileConf.UserMemoryExecutable = executable
	config.EnvAndFileConf.EmbedOpenaiModel = "test"
	config.EnvAndFileConf.EmbedOpenaiBaseUrl = "https://example.invalid/v1"
	config.EnvAndFileConf.EmbedOpenaiApiKey = "test"
	config.EnvAndFileConf.UserMemoryConcurrency = 1
	config.EnvAndFileConf.UserMemoryTimeoutSeconds = 10
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(workdir), layout.SessionRoot(workdir))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	cleanup, err := newServer(workdir, false).startUserMemory(repo)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	initArgs, err := os.ReadFile(initArgsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(initArgs); got != "--target user_memory --db "+dbPath+" --dimensions 1024 --init-schema" {
		t.Fatalf("schema init arguments = %q", got)
	}
	if _, err := os.Stat(filepath.Join(workdir, "kb")); !os.IsNotExist(err) {
		t.Fatalf("default database directory accessed: %v", err)
	}
}
