//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package ai_models

import (
	"errors"
	"os"
)

func tryFileLock(*os.File) (bool, error) {
	return false, errors.New("ChatGPT credential locking is unsupported on this platform")
}
func unlockFile(*os.File) error { return nil }
