package pm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes_PreservesUTF8(t *testing.T) {
	in := strings.Repeat("中", 80) // 80 个汉字
	out := truncateRunes(in, 60)
	if utf8.RuneCountInString(out) != 60 {
		t.Fatalf("应保留 60 个 rune, 实际 %d", utf8.RuneCountInString(out))
	}
	if !utf8.ValidString(out) {
		t.Fatalf("截断结果不是合法 UTF-8: %q", out)
	}
}

func TestExtractTitle_ChineseFallback(t *testing.T) {
	got := extractTitle(strings.Repeat("做", 80), "")
	if !utf8.ValidString(got) {
		t.Fatalf("extractTitle fallback 截断后不是合法 UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) != 50 {
		t.Fatalf("fallback 应裁到 50 个 rune, 实际 %d", utf8.RuneCountInString(got))
	}
}

func TestMakeChangeID_NoBrokenUTF8(t *testing.T) {
	id := makeChangeID(strings.Repeat("贪吃蛇网页小游戏", 20))
	if !utf8.ValidString(id) {
		t.Fatalf("makeChangeID 输出非法 UTF-8: %q", id)
	}
}
