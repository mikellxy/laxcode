package sessionrepo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const historyFile = "history.jsonl"

func appendHistory(root, sessionID string, messages []sharedkernel.Message) error {
	path := filepath.Join(root, sessionID, historyFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if err := os.Chown(filepath.Dir(path), 1000, 1000); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if err := f.Chown(1000, 1000); err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(f)
	for i := range messages {
		if err := encoder.Encode(messages[i]); err != nil {
			return err
		}
	}
	return f.Sync()
}
