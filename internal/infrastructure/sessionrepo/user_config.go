package sessionrepo

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

func (r *SqliteSessionRepo) GetUserConfig(ctx context.Context) (map[string]json.RawMessage, error) {
	var rows []struct{ Key, Value string }
	if err := r.db.WithContext(ctx).Table("user_config").Find(&rows).Error; err != nil {
		return nil, err
	}
	values := make(map[string]json.RawMessage, len(rows))
	for _, row := range rows {
		values[row.Key] = json.RawMessage(row.Value)
	}
	return values, nil
}

// Update only supplied keys; validation of writable keys belongs to the API.
func (r *SqliteSessionRepo) UpdateUserConfig(ctx context.Context, values map[string]json.RawMessage) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for key, value := range values {
			if err := tx.Exec(`INSERT INTO user_config (key, value) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(value)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
