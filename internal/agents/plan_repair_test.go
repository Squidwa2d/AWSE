package agents

import (
	"errors"
	"strings"
	"testing"
)

// fullPlanMD 用一份"足够像样"的 plan.md, 覆盖 looksLikePartialPlan 的全部正面要求.
// 注: 长度需 ≥ 600 rune, 含两个核心章节, 含 # aswe-plan-modules marker.
const fullPlanMD = `## 总体思路
本方案使用 Go + 标准库构建一个 CLI 工具, 提供基础的待办管理能力.
通过子命令分发, 把 add/list/done/rm 拆成独立的 handler, 数据持久化到本地 JSON 文件.

## 技术选型
- 语言: Go 1.22
- 命令行: 标准库 flag (避免 cobra 引入额外依赖)
- 持久化: 单文件 JSON, 路径 ~/.todo.json
- 测试: 标准库 testing

## 模块拆分
- A 数据层: 负责 todo.go 的结构体定义 + 文件读写
- B 命令层: cmd/ 下每个 *.go 对应一个子命令的解析与执行
- C 入口层: main.go 负责命令分发与全局错误处理

## 关键接口
type Todo struct { ID int; Text string; Done bool }
func Load(path string) ([]Todo, error)
func Save(path string, items []Todo) error

## 实施步骤
1. 创建 go.mod 与项目骨架.
2. 实现 todo.go (Todo + Load + Save) 并跑单测.
3. 逐个实现 add/list/done/rm 命令.
4. 在 main.go 串联子命令分发.
5. 写 README, 给出基本用法.

## 风险与权衡
- JSON 单文件并发写入会丢数据; 第一版可接受, 文档里注明只允许单进程使用.
- ~/.todo.json 的位置在不同 OS 上可能需要适配 (Windows %USERPROFILE%).

## 需要人类批准的事项
无.

## 模块与单元拆分 (机器可读)

` + "```" + `yaml
# aswe-plan-modules
modules:
  - id: A
    title: 数据层
    goal: 定义 Todo 与文件读写
    units:
      - id: A.1
        title: 数据结构与持久化
        scope: todo.go
        deliverable: type Todo + Load/Save
  - id: B
    title: 命令层
    goal: 实现各子命令
    units:
      - id: B.1
        title: add 子命令
        scope: cmd/add.go
        deliverable: add(args []string) error
` + "```" + `

VERDICT: PASS
`

func TestLooksLikePartialPlan_FullPlanPasses(t *testing.T) {
	if reason, ok := looksLikePartialPlan(fullPlanMD); !ok {
		t.Fatalf("一份合规 plan.md 应当通过粗筛, 但被打回: %s", reason)
	}
}

func TestLooksLikePartialPlan_TooShort(t *testing.T) {
	short := "## 模块拆分\n## 实施步骤\n# aswe-plan-modules\n仅几十字, 明显不是完整方案"
	reason, ok := looksLikePartialPlan(short)
	if ok {
		t.Fatal("过短的 plan.md 应当被粗筛打回")
	}
	if !strings.Contains(reason, "长度") {
		t.Errorf("打回原因应当点出长度问题, 实际: %s", reason)
	}
}

func TestLooksLikePartialPlan_MissingSection(t *testing.T) {
	// 把 ## 模块拆分 替换成别的, 但保持总长足够
	body := strings.ReplaceAll(fullPlanMD, "## 模块拆分", "## 子系统总览")
	reason, ok := looksLikePartialPlan(body)
	if ok {
		t.Fatal("缺少核心章节的 plan.md 应当被打回")
	}
	if !strings.Contains(reason, "## 模块拆分") {
		t.Errorf("打回原因应当点名缺失的章节, 实际: %s", reason)
	}
}

func TestLooksLikePartialPlan_MissingMarker(t *testing.T) {
	body := strings.ReplaceAll(fullPlanMD, "# aswe-plan-modules", "# 模块拆分清单")
	reason, ok := looksLikePartialPlan(body)
	if ok {
		t.Fatal("缺少 aswe-plan-modules 标识行的 plan.md 应当被打回")
	}
	if !strings.Contains(reason, "aswe-plan-modules") {
		t.Errorf("打回原因应当点名缺失的标识行, 实际: %s", reason)
	}
}

// TestLooksLikePartialPlan_ChangelogStyle 复刻真实事故现场:
// 模型只回了一段"我改了什么"的总结, 内容看似完整但其实是 changelog.
// 它通常很短, 由长度门拦下.
func TestLooksLikePartialPlan_ChangelogStyle(t *testing.T) {
	changelog := `已将 Plan 第 2 轮方案写入 plan.md, 逐条回应了上一轮 Plan-Review 反馈:

阻塞级 B1 (J 模块单元间依赖) - 已修复: 合并 J.1 和 J.2 为单一单元.
建议级 S1~S4 全部采纳.

末尾保留 VERDICT: PASS, 等待 Plan-Review 评审.`
	if _, ok := looksLikePartialPlan(changelog); ok {
		t.Fatal("典型 changelog 风格输出应当被粗筛打回")
	}
}

func TestBuildPlanRepairPrompt_FlagsChangelog(t *testing.T) {
	short := "已将方案写入 plan.md, 已修复 B1, 等待评审."
	got := buildPlanRepairPrompt(short, errors.New("plan.md 长度仅 30 字"))
	// 短原文必须触发"原文不是真正 plan.md"的额外警告
	if !strings.Contains(got, "几乎可以确定它") || !strings.Contains(got, "回到 spec 重新写") {
		t.Errorf("repair prompt 应当对短原文加挂 changelog 警告, 实际:\n%s", got)
	}
	if !strings.Contains(got, "严禁") {
		t.Errorf("repair prompt 应当显式禁止 changelog 风格输出, 实际:\n%s", got)
	}
}

func TestBuildPlanRepairPrompt_LongPlanNoChangelogWarning(t *testing.T) {
	got := buildPlanRepairPrompt(fullPlanMD, errors.New("YAML 解析失败"))
	if strings.Contains(got, "几乎可以确定它") {
		t.Errorf("足够长的原文不应触发 changelog 警告, 实际:\n%s", got)
	}
	// 但禁令条款无论长短都应在
	if !strings.Contains(got, "严禁") {
		t.Errorf("repair prompt 始终应禁止 changelog, 实际:\n%s", got)
	}
}
