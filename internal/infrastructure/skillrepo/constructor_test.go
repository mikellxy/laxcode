package skillrepo

import "testing"

func newTestStore(t *testing.T, root string) *Source { t.Helper(); return NewWithRoot(root, root) }
