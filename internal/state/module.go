// Package state 的模块/单元数据结构与调度辅助.
//
// 新版流水线引入"模块-单元"二级结构:
//   - Plan-Agent 把实施方案拆成若干 Module, 每个 Module 再拆成若干 Unit.
//   - 同一 Module 内的 Unit 之间**无依赖**, 可"流水线式"推进
//     (dev 不必等 review/test; 队列按 FIFO 调度).
//   - 不同 Module 之间**串行**: 前一 Module 全部 Unit 都 Done 后, 下一 Module 才开始.
//
// 每个 Unit 自己的循环上限 MaxLoops (默认 8), 超限 -> 该 Module 标记 failed,
// 整个流程暂停等待人工.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UnitStatus 单元执行状态. 9 档状态覆盖完整生命周期.
type UnitStatus string

const (
	UnitPending       UnitStatus = "pending"        // 尚未开始
	UnitDevRunning    UnitStatus = "dev_running"    // Dev-Agent 正在写
	UnitDevDone       UnitStatus = "dev_done"       // Dev 完成, 等 review
	UnitReviewRunning UnitStatus = "review_running" // Review-Agent 正在审
	UnitReviewFailed  UnitStatus = "review_failed"  // Review 打回, 下一步回 dev (loop+1)
	UnitReviewPassed  UnitStatus = "review_passed"  // Review 通过, 等 test
	UnitTestRunning   UnitStatus = "test_running"   // Test-Agent 正在跑
	UnitTestFailed    UnitStatus = "test_failed"    // Test 失败, 下一步回 dev (loop+1)
	UnitDone          UnitStatus = "done"           // 通过 dev/review/test
	UnitFailed        UnitStatus = "failed"         // 超过 MaxLoops, 需人工
)

// ModuleStatus 模块聚合状态.
type ModuleStatus string

const (
	ModulePending ModuleStatus = "pending"
	ModuleRunning ModuleStatus = "running"
	ModuleDone    ModuleStatus = "done"
	ModuleFailed  ModuleStatus = "failed"
)

// Unit 最小的 dev/review/test 单位.
type Unit struct {
	ID           string     `json:"id"`                    // 唯一标识, 形如 "A.1"
	Title        string     `json:"title"`                 // 人类可读标题
	Scope        string     `json:"scope,omitempty"`       // 单元负责的文件/目录范围 (自由文本)
	Deliverable  string     `json:"deliverable,omitempty"` // 验收交付物描述
	Status       UnitStatus `json:"status"`
	Iteration    int        `json:"iteration"`               // 已完成的 dev↔review/test 轮数 (0 起)
	LastFeedback string     `json:"last_feedback,omitempty"` // 最近一次 review 或 test 的反馈
	LastError    string     `json:"last_error,omitempty"`
	StartedAt    time.Time  `json:"started_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at,omitempty"`
	// 分节产物相对 ArtifactDir 的文件路径, 调试用
	DevFile    string `json:"dev_file,omitempty"`
	ReviewFile string `json:"review_file,omitempty"`
	TestFile   string `json:"test_file,omitempty"`
}

// Module 模块. 内部单元 FIFO 调度, 各单元之间无依赖.
type Module struct {
	ID     string       `json:"id"`    // 形如 "A"
	Title  string       `json:"title"` // 模块名
	Goal   string       `json:"goal,omitempty"`
	Status ModuleStatus `json:"status"`
	Units  []*Unit      `json:"units"`
}

// IsDone 所有 Unit Done.
func (m *Module) IsDone() bool {
	for _, u := range m.Units {
		if u.Status != UnitDone {
			return false
		}
	}
	return len(m.Units) > 0
}

// HasFailed 任一 Unit 超限.
func (m *Module) HasFailed() bool {
	for _, u := range m.Units {
		if u.Status == UnitFailed {
			return true
		}
	}
	return false
}

// NextRunnableUnit 按 FIFO 策略选出下一个**可执行**的单元.
// 可执行指单元当前状态需要某个 Agent 动手 (dev/review/test).
// 规则: 按 Units 数组顺序从头扫, 返回第一个"有动作要做"的单元; 已 done/failed 跳过.
// 因此"打回修复"和"新单元"共享同一条 FIFO 队列, 谁先进数组谁先做.
func (m *Module) NextRunnableUnit() *Unit {
	for _, u := range m.Units {
		switch u.Status {
		case UnitPending,
			UnitDevDone,
			UnitReviewFailed,
			UnitReviewPassed,
			UnitTestFailed:
			return u
		}
	}
	return nil
}

// ActiveModule 返回第一个尚未完成的 Module. 同一时间只有一个 Module 在跑.
func (s *State) ActiveModule() *Module {
	for _, m := range s.Modules {
		if m.Status != ModuleDone {
			return m
		}
	}
	return nil
}

// ModuleByID 按 ID 取模块.
func (s *State) ModuleByID(id string) *Module {
	for _, m := range s.Modules {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// UnitByID 全局按 ID (比如 "A.2") 找单元, 返回 (module, unit).
func (s *State) UnitByID(id string) (*Module, *Unit) {
	for _, m := range s.Modules {
		for _, u := range m.Units {
			if u.ID == id {
				return m, u
			}
		}
	}
	return nil, nil
}

// AllModulesDone 所有模块均已完成.
func (s *State) AllModulesDone() bool {
	if len(s.Modules) == 0 {
		return false
	}
	for _, m := range s.Modules {
		if m.Status != ModuleDone {
			return false
		}
	}
	return true
}

// WriteTasksMD 把当前模块/单元状态渲染成 tasks.md 落盘到 changeDir.
// 该文件给人类阅读, 机器真相以 state.json 为准.
func WriteTasksMD(changeDir string, st *State) error {
	if changeDir == "" {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Tasks — %s\n\n", st.ChangeID)
	fmt.Fprintf(&b, "_generated at %s_\n\n", time.Now().Format(time.RFC3339))

	if len(st.Modules) == 0 {
		// 没有模块时退化为线性 stage 视图: 让用户在 plan 之前/线性兜底路径下也能看到进度.
		b.WriteString("> 暂无模块拆分 (仍在 spec/plan 之前, 或运行在线性兜底路径).\n\n")
		b.WriteString("## Stage 概览\n\n")
		b.WriteString("| Stage | 状态 | Adapter | Summary |\n")
		b.WriteString("|-------|------|---------|---------|\n")
		stages := []Stage{
			StageSpec, StagePlan, StagePlanReview,
			StageDev, StageReview, StageTest,
		}
		for _, s := range stages {
			n, ok := st.Nodes[s]
			if !ok || n == nil {
				fmt.Fprintf(&b, "| %s | _未运行_ |  |  |\n", s)
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
				s, n.Status, n.Adapter, escapeMD(truncate(n.Summary, 80)))
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "当前 stage: **%s**, plan iter=%d, code iter=%d\n",
			st.CurrentStage, st.PlanIteration, st.CodeIteration)
		return os.WriteFile(filepath.Join(changeDir, "tasks.md"), []byte(b.String()), 0o644)
	}

	// 总览表
	b.WriteString("## 总览\n\n")
	b.WriteString("| 模块 | 标题 | 状态 | 单元数 | 完成 |\n")
	b.WriteString("|------|------|------|--------|------|\n")
	for _, m := range st.Modules {
		done := 0
		for _, u := range m.Units {
			if u.Status == UnitDone {
				done++
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %s %s | %d | %d |\n",
			m.ID, escapeMD(m.Title), moduleEmoji(m.Status), m.Status, len(m.Units), done)
	}
	b.WriteString("\n")

	// 详细: 每个模块展开
	for _, m := range st.Modules {
		fmt.Fprintf(&b, "## 模块 %s — %s\n\n", m.ID, escapeMD(m.Title))
		if m.Goal != "" {
			fmt.Fprintf(&b, "**目标**: %s\n\n", m.Goal)
		}
		fmt.Fprintf(&b, "状态: %s %s\n\n", moduleEmoji(m.Status), m.Status)

		if len(m.Units) == 0 {
			b.WriteString("_(无单元)_\n\n")
			continue
		}
		b.WriteString("| 单元 | 标题 | 状态 | 轮次 | 范围 |\n")
		b.WriteString("|------|------|------|------|------|\n")
		for _, u := range m.Units {
			fmt.Fprintf(&b, "| %s | %s | %s %s | %d | %s |\n",
				u.ID, escapeMD(u.Title),
				unitEmoji(u.Status), u.Status,
				u.Iteration, escapeMD(truncate(u.Scope, 60)))
		}
		b.WriteString("\n")
	}

	// 最近反馈
	if hasAnyFeedback(st) {
		b.WriteString("## 最近一次反馈 (仅展示最近更新过的 3 条)\n\n")
		feeds := collectRecentFeedbacks(st, 3)
		for _, f := range feeds {
			fmt.Fprintf(&b, "### %s — %s\n\n```\n%s\n```\n\n",
				f.UnitID, f.Status, truncate(f.Feedback, 800))
		}
	}

	return os.WriteFile(filepath.Join(changeDir, "tasks.md"), []byte(b.String()), 0o644)
}

type feedbackEntry struct {
	UnitID   string
	Status   UnitStatus
	Feedback string
	UpdateAt time.Time
}

func collectRecentFeedbacks(st *State, n int) []feedbackEntry {
	var list []feedbackEntry
	for _, m := range st.Modules {
		for _, u := range m.Units {
			if strings.TrimSpace(u.LastFeedback) == "" {
				continue
			}
			list = append(list, feedbackEntry{
				UnitID: u.ID, Status: u.Status, Feedback: u.LastFeedback, UpdateAt: u.UpdatedAt,
			})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UpdateAt.After(list[j].UpdateAt) })
	if len(list) > n {
		list = list[:n]
	}
	return list
}

func hasAnyFeedback(st *State) bool {
	for _, m := range st.Modules {
		for _, u := range m.Units {
			if strings.TrimSpace(u.LastFeedback) != "" {
				return true
			}
		}
	}
	return false
}

func moduleEmoji(s ModuleStatus) string {
	switch s {
	case ModuleDone:
		return "✅"
	case ModuleFailed:
		return "❌"
	case ModuleRunning:
		return "🔄"
	}
	return "⏸"
}

func unitEmoji(s UnitStatus) string {
	switch s {
	case UnitDone:
		return "✅"
	case UnitFailed:
		return "❌"
	case UnitDevRunning, UnitReviewRunning, UnitTestRunning:
		return "🔄"
	case UnitReviewFailed, UnitTestFailed:
		return "🔁"
	case UnitDevDone, UnitReviewPassed:
		return "⏭"
	}
	return "⏸"
}

func escapeMD(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// truncate 按 rune 截断字符串, 中英混合字符串不会切到多字节字符中段.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
