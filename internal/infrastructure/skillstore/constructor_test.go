package skillstore

import "testing"

func newTestStore(t *testing.T, root string) *Store { t.Helper(); return NewWithRoot(root, root) }
