//go:build !unix

package workspace

import "os"

const downloadOpenFlags = os.O_RDONLY
