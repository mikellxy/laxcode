package artifactstore

import "testing"

func newTestStore(t *testing.T, root string) *FileStore { t.Helper(); return NewWithRoot(root, root) }
