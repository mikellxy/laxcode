package sessionrepo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestUserConfigPersistsAndUpdatesOnlySuppliedKeys(t *testing.T) {
	repo, root := newTestRepo(t)
	ctx := context.Background()
	// Simulate upgrading an existing database and running its migration twice.
	if err := repo.db.Exec("DROP TABLE user_config").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := repo.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	values, err := repo.GetUserConfig(ctx)
	if err != nil || len(values) != 0 {
		t.Fatalf("initial config: %v %v", values, err)
	}
	if err := repo.UpdateUserConfig(ctx, map[string]json.RawMessage{"user_guide_seen_version": json.RawMessage(`"v1"`), "other": json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateUserConfig(ctx, map[string]json.RawMessage{"user_guide_seen_version": json.RawMessage(`"v2"`)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateUserConfig(ctx, map[string]json.RawMessage{"other": json.RawMessage(`invalid`)}); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	values, err = reopened.GetUserConfig(ctx)
	if err != nil || string(values["user_guide_seen_version"]) != `"v2"` || string(values["other"]) != `true` {
		t.Fatalf("reopened config: %v %v", values, err)
	}
}
