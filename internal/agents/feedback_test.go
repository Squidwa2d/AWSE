package agents

import (
	"strings"
	"testing"
)

func TestExtractFeedbackSection_NormalCase(t *testing.T) {
	md := `# Review for unit-A

## 结论
不通过.

## 给 Dev 的反馈
- foo.go: 把 nil check 移到入口
- bar.go: 重构 fooBar 减少嵌套

## 其它
xxx

VERDICT: FAIL
`
	got := ExtractFeedbackSection(md, "给 Dev 的反馈")
	if !strings.Contains(got, "foo.go") || !strings.Contains(got, "bar.go") {
		t.Fatalf("应包含两条反馈, got=%q", got)
	}
	if strings.Contains(got, "VERDICT") || strings.Contains(got, "其它") {
		t.Fatalf("应只截到下个 H2 之前, got=%q", got)
	}
}

func TestExtractFeedbackSection_MissingFallsBack(t *testing.T) {
	md := "随便写点东西\n没有标准章节\n"
	got := ExtractFeedbackSection(md, "给 Dev 的反馈")
	if got != strings.TrimSpace(md) {
		t.Fatalf("找不到 section 时应退化为整段, got=%q", got)
	}
}

func TestSummarizePlanForUnit_KeepsModuleSection(t *testing.T) {
	plan := `# Plan

整体描述: 三段式...

## 模块 A — 数据模型
- 字段 X
- 字段 Y

## 模块 B — UI 渲染
- 页面 P
- 页面 Q
`
	got := summarizePlanForUnit(plan, "B", 4000)
	if !strings.Contains(got, "模块 B") || !strings.Contains(got, "页面 P") {
		t.Fatalf("应包含模块 B 章节, got=%q", got)
	}
	if strings.Contains(got, "字段 X") {
		t.Fatalf("不应混入其它模块章节: %q", got)
	}
}

func TestSummarizePlanForUnit_NoMatchKeepsOriginal(t *testing.T) {
	plan := "# Plan\n整体说明\n"
	got := summarizePlanForUnit(plan, "Z", 4000)
	if got != plan {
		t.Fatalf("找不到模块时应原样返回, got=%q", got)
	}
}
