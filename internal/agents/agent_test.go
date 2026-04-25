package agents

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseVerdict_PassFail 覆盖各种 PASS/FAIL 格式的解析 (含回归用例).
func TestParseVerdict_PassFail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"标准 PASS", "一些内容\n\nVERDICT: PASS", true},
		{"标准 FAIL", "一些内容\n\nVERDICT: FAIL", false},
		{"小写 verdict", "xxx\nverdict: pass", true},
		{"带前缀说明", "blah\nfinal VERDICT: FAIL, 有问题", false},
		{"裸 PASS 行", "xx\nyy\nPASS", true},
		{"裸 FAIL 行", "xx\nyy\nFAIL", false},
		{"尾部空行 + PASS", "xx\nVERDICT: PASS\n\n  \n", true},
		// --- 回归: AI 把 VERDICT 放在中部, 末尾还跟着解释性段落 ---
		{"加粗 FAIL + 末尾解释段落",
			"## 结论\n\n**VERDICT: FAIL**\n\n核心原因: xxx\n另有两处建议级问题不阻塞流程, 但建议一并完善。",
			false},
		{"加粗 PASS + 末尾解释段落",
			"## 结论\n\n**VERDICT: PASS**\n\n说明: 方案完整, 可进入 Dev 阶段。",
			true},
		{"引用块包裹 VERDICT: FAIL", "> VERDICT: FAIL\n\n后记文字", false},
		{"同一行同时出现 PASS 与 FAIL 时取 FAIL", "VERDICT: FAIL (不再 PASS)", false},
		// --- 缺省语义改为 FAIL: 宁可多跑一轮, 也不放行问题评审 ---
		{"无任何 verdict 标记 -> 缺省 FAIL", "xx\nyy\nzz", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseVerdict(c.in)
			if got != c.want {
				t.Fatalf("parseVerdict(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestReadIfExists 覆盖存在/不存在/空路径三种情况.
func TestReadIfExists(t *testing.T) {
	if got := readIfExists(""); got != "" {
		t.Fatalf("空路径应返回空串, got %q", got)
	}
	if got := readIfExists("/definitely/not/exist/xxx.md"); got != "" {
		t.Fatalf("不存在的文件应返回空串, got %q", got)
	}

	dir := t.TempDir()
	fp := filepath.Join(dir, "a.md")
	want := "hello agents\n"
	if err := os.WriteFile(fp, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readIfExists(fp); got != want {
		t.Fatalf("readIfExists(%s) = %q, want %q", fp, got, want)
	}
}
