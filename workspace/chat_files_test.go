package workspace

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChatZIPFilename(t *testing.T) {
	now := time.Date(2026, 10, 28, 23, 30, 0, 0, time.UTC)
	for _, tc := range []struct{ title, want string }{
		{" 登录页优化 ", "登录页优化_20261029_0730.zip"},
		{" . ", "未命名会话_20261029_0730.zip"},
		{"a/b\\c:\n?", "a_b_c____20261029_0730.zip"},
	} {
		if got := chatZIPFilename(tc.title, now); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.title, got, tc.want)
		}
	}
	got := chatZIPFilename(strings.Repeat("长", 120), now)
	if len(got) > 255 || !utf8.ValidString(got) {
		t.Fatalf("invalid filename %q", got)
	}
}
