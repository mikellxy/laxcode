//go:build unix

package run_sse

import (
	"os"
	"syscall"
)

const downloadOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
