package sharedkernel

import "testing"

func TestModelContentPrefersWrappedContent(t *testing.T) {
	m := Message{Role: RoleUser, Content: "question", WrappedContent: "wrapped question"}
	if got := m.ModelContent(); got != "wrapped question" {
		t.Fatalf("ModelContent = %q", got)
	}
	without := Message{Role: RoleUser, Content: "question"}
	if got := without.ModelContent(); got != "question" {
		t.Fatalf("ModelContent without wrapper = %q", got)
	}
}
