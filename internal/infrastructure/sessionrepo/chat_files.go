package sessionrepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"gorm.io/gorm"
)

var ErrChatNotFound = errors.New("sessionrepo: chat not found")

func (r *SqliteSessionRepo) UpsertChatFiles(ctx context.Context, sessionID, chatID string, files []session.ChatFile) error {
	if err := validSessionID(sessionID); err != nil {
		return err
	}
	if chatID == "" {
		return fmt.Errorf("chat ID is required")
	}
	for _, file := range files {
		if !fs.ValidPath(file.Path) || file.Path == "." || strings.Contains(file.Path, "\\") || (file.ChangeType != "created" && file.ChangeType != "modified") {
			return fmt.Errorf("invalid chat file")
		}
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkChat(tx, sessionID, chatID); err != nil {
			return err
		}
		now := time.Now()
		for _, file := range files {
			if err := tx.Exec(`INSERT INTO chat_upsert_files
				(session_id, chat_id, file_path, change_type, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(session_id, chat_id, file_path) DO UPDATE SET updated_at = excluded.updated_at`,
				sessionID, chatID, file.Path, file.ChangeType, now, now).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func checkChat(tx *gorm.DB, sessionID, chatID string) error {
	var count int64
	err := tx.Model(&messageModel{}).Where("session_id = ? AND chat_id = ? AND message_type = ? AND role = ?",
		sessionID, chatID, messageTypeOriginal, sharedkernel.RoleUser).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrChatNotFound
	}
	return nil
}

func (r *SqliteSessionRepo) ListChatFiles(ctx context.Context, sessionID, chatID string) ([]session.ChatFile, error) {
	if err := validSessionID(sessionID); err != nil {
		return nil, err
	}
	var files []session.ChatFile
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkChat(tx, sessionID, chatID); err != nil {
			return err
		}
		return tx.Table("chat_upsert_files").Select("file_path AS path, change_type").
			Where("session_id = ? AND chat_id = ?", sessionID, chatID).Order("file_path").Scan(&files).Error
	})
	return files, err
}

// Find the last assistant across the entire chat, not just this history page.
func attachChatFiles(tx *gorm.DB, sessionID string, messages []session.HistoryMessage) error {
	ids := make([]string, 0)
	for _, msg := range messages {
		if msg.Role == sharedkernel.RoleAssistant && msg.ChatID != "" {
			ids = append(ids, msg.ChatID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var anchors []struct {
		ChatID string
		Seq    uint64
	}
	if err := tx.Model(&messageModel{}).Select("chat_id, MAX(seq) AS seq").
		Where("session_id = ? AND message_type = ? AND role = ? AND chat_id IN ?", sessionID, messageTypeOriginal, sharedkernel.RoleAssistant, ids).
		Group("chat_id").Scan(&anchors).Error; err != nil {
		return err
	}
	var rows []struct {
		ChatID     string
		Path       string
		ChangeType string
	}
	if err := tx.Table("chat_upsert_files").Select("chat_id, file_path AS path, change_type").
		Where("session_id = ? AND chat_id IN ?", sessionID, ids).Order("file_path").Scan(&rows).Error; err != nil {
		return err
	}
	last := make(map[string]uint64, len(anchors))
	for _, anchor := range anchors {
		last[anchor.ChatID] = anchor.Seq
	}
	files := make(map[string][]session.ChatFile)
	for _, row := range rows {
		files[row.ChatID] = append(files[row.ChatID], session.ChatFile{Path: row.Path, ChangeType: row.ChangeType})
	}
	for i := range messages {
		if messages[i].Role == sharedkernel.RoleAssistant && messages[i].Seq == last[messages[i].ChatID] {
			messages[i].UpsertFiles = files[messages[i].ChatID]
		}
	}
	return nil
}
