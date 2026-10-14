//go:build !unix

package run_sse

import "os"

const downloadOpenFlags = os.O_RDONLY
