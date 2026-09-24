//go:build windows

package run_sse

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffsetHigh places the one-byte ownership lock at 4 GiB, well beyond the
// small status record stored at the beginning of the file. Windows byte-range
// locks are mandatory, so locking byte zero would prevent web.ps1 from reading
// the PID and URL while the server is running.
const lockOffsetHigh = 1

func lockOverlapped() *windows.Overlapped {
	return &windows.Overlapped{OffsetHigh: lockOffsetHigh}
}

func tryExclusiveFileLock(file *os.File) (bool, error) {
	overlapped := lockOverlapped()
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, lockOverlapped())
}
