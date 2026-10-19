//go:build unix

package workspace

import (
	"os"
	"syscall"
)

const downloadOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
