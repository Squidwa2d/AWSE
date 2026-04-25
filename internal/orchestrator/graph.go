// Package orchestrator 阶段 3+ 的编排骨架.
//
// DAG:
//
//	spec → plan → plan-review ─PASS─▶ dev → review ─PASS─▶ test ─PASS─▶ done
//	            │         ▲                 │              │
//	            │         │ FAIL(≤MaxPlanLoops)            │
//	            │         └──回 plan                       │
//	            │                                          │
//	            │                FAIL(共享 CodeLoops 计数) │
//	            │                    ┌──回 dev ◀──────────┤
//	            │                    │                     │
//	            │                    └◀────FAIL────────────┘
//	            │
//	            └ 方案循环超限 → failed
//
// - plan ↔ plan-review: 独立计数, 上限 MaxPlanLoops (默认 8).
// - dev ↔ review ↔ test: 共享一个计数, 上限 MaxCodeLoops (默认 8).
// - 任一循环超限 → 进入 StageFailed, orchestrator 打印提示后退出.
// - 自动循环期间不再每步询问用户(避免打断), 仅在 key 节点 (spec/第一轮 plan/第一轮 dev) 询问.
package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aswe/aswe/internal/agents"
	"github.com/aswe/aswe/internal/config"
	"github.com/aswe/aswe/internal/state"
)

// 默认循环上限.
const (
	DefaultPlanLoops    = 8
	DefaultCodeLoops    = 8
	DefaultMinPlanLoops = 2 // plan<->plan-review 至少跑多少轮
)

// Orchestrator 编排器.
type Orchestrator struct {
	store        *state.Store
	nodes        map[state.Stage]agents.Agent
	edges        map[state.Stage]state.Stage // 默认静态边
	mode         config.AutomationMode
	keyNodes     map[state.Stage]bool
	maxPlanLoops int
	minPlanLoops int // plan<->plan-review 最小循环数, 即便 AI 第一轮判 PASS 也强制再跑
	maxCodeLoops int
	projectDir   string
	artifactDir  string

	// 单元化 Agent 三件套; 若非 nil, plan-review PASS 后优先走模块化流水线.
	unitAgents *unitAgents

	in  *bufio.Reader
	out io.Writer
}

// Options 构造参数.
type Options struct {
	Store        *state.Store
	Nodes        map[state.Stage]agents.Agent
	Mode         config.AutomationMode
	ProjectDir   string
	ArtifactDir  string
	MaxPlanLoops int // 0 则使用默认 8
	MinPlanLoops int // 0 则使用默认 2
	MaxCodeLoops int // 0 则使用默认 8
	// 单元化 Agent; 可选. 传入后启用模块化流水线.
	DevUnit    *agents.DevUnitAgent
	ReviewUnit *agents.ReviewUnitAgent
	TestUnit   *agents.TestUnitAgent
	In         io.Reader
	Out        io.Writer
}

// New 构造默认 DAG.
func New(opts Options) *Orchestrator {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Mode == "" {
		opts.Mode = config.ModeInteractive
	}
	if opts.MaxPlanLoops <= 0 {
		opts.MaxPlanLoops = DefaultPlanLoops
	}
	if opts.MinPlanLoops <= 0 {
		opts.MinPlanLoops = DefaultMinPlanLoops
	}
	if opts.MinPlanLoops > opts.MaxPlanLoops {
		opts.MinPlanLoops = opts.MaxPlanLoops
	}
	if opts.MaxCodeLoops <= 0 {
		opts.MaxCodeLoops = DefaultCodeLoops
	}
	o := &Orchestrator{
		store: opts.Store,
		nodes: opts.Nodes,
		mode:  opts.Mode,
		keyNodes: map[state.Stage]bool{
			// interactive 模式下, 仅在每段流程刚启动时询问一次
			state.StageSpec: true,
			state.StagePlan: true,
			state.StageDev:  true,
		},
		maxPlanLoops: opts.MaxPlanLoops,
		minPlanLoops: opts.MinPlanLoops,
		maxCodeLoops: opts.MaxCodeLoops,
		projectDir:   opts.ProjectDir,
		artifactDir:  opts.ArtifactDir,
		in:           bufio.NewReader(opts.In),
		out:          opts.Out,
	}
	// 默认静态边 (失败分支在 transition 里动态决策)
	o.edges = map[state.Stage]state.Stage{
		state.StageSpec:       state.StagePlan,
		state.StagePlan:       state.StagePlanReview,
		state.StagePlanReview: state.StageDev,
		state.StageDev:        state.StageReview,
		state.StageReview:     state.StageTest,
		state.StageTest:       state.StageDone,
	}
	// 组装单元化 Agent (三者都提供才启用)
	if opts.DevUnit != nil && opts.ReviewUnit != nil && opts.TestUnit != nil {
		o.unitAgents = &unitAgents{
			dev:    opts.DevUnit,
			review: opts.ReviewUnit,
			test:   opts.TestUnit,
		}
	}
	return o
}

// Run 从 State.CurrentStage 开始执行, 直到 done 或 failed.
func (o *Orchestrator) Run(ctx context.Context, changeDir, proposalPath string) error {
	st := o.store.State()
	if st.CurrentStage == "" {
		st.CurrentStage = state.StageSpec
	}
	st.ProposalPath = proposalPath
	if o.projectDir != "" {
		st.ProjectDir = o.projectDir
	}
	if o.artifactDir == "" {
		o.artifactDir = st.ArtifactDir
	}
	if o.artifactDir == "" {
		o.artifactDir = changeDir
	}
	st.ArtifactDir = o.artifactDir
	if err := os.MkdirAll(o.artifactDir, 0o755); err != nil {
		return fmt.Errorf("create artifact dir: %w", err)
	}

	// resume 兜底: 上次跑挂在 failed 时, PlanIteration / CodeIteration 通常已贴上限,
	// 直接续跑会被 transition() 立即判定 "loop budget exceeded" 而再次 failed,
	// 用户感觉 "我什么都没做就又挂了". 这里把 stage 拉回非 failed, 同时把已贴顶的
	// 计数器降一档, 让用户能借助本次修改至少多跑两轮再决定.
	if st.CurrentStage == state.StageFailed {
		st.CurrentStage = state.StagePlan
		fmt.Fprintln(o.out, "ℹ️  上次以 failed 收尾, 自动从 plan 阶段续跑")
	}
	if st.PlanIteration >= o.maxPlanLoops {
		newIter := o.maxPlanLoops - 2
		if newIter < 0 {
			newIter = 0
		}
		fmt.Fprintf(o.out, "ℹ️  PlanIteration=%d 已到上限, 续跑前降到 %d\n", st.PlanIteration, newIter)
		st.PlanIteration = newIter
	}
	if st.CodeIteration >= o.maxCodeLoops {
		newIter := o.maxCodeLoops - 2
		if newIter < 0 {
			newIter = 0
		}
		fmt.Fprintf(o.out, "ℹ️  CodeIteration=%d 已到上限, 续跑前降到 %d\n", st.CodeIteration, newIter)
		st.CodeIteration = newIter
	}
	_ = o.store.Save()

	for st.CurrentStage != state.StageDone && st.CurrentStage != state.StageFailed {
		stage := st.CurrentStage

		// === 模块化流水线钩子 ===
		// 一旦进入 Dev 阶段 (无论是 plan-review PASS 首次进入, 还是断点续跑),
		// 优先尝试走模块化流水线. plan.md 解析失败应中止并暴露错误, 不再静默降级.
		if stage == state.StageDev && o.unitAgents != nil {
			handled, err := o.runModulePipeline(ctx, changeDir, o.artifactDir, o.unitAgents)
			if handled {
				if err != nil {
					return err
				}
				// pipeline 已把 CurrentStage 推进到 done/failed, 本轮主循环继续让 for 条件判断
				continue
			}
			// 未处理 -> 回退到旧的线性 Dev
		}

		agent, ok := o.nodes[stage]
		if !ok {
			return fmt.Errorf("no agent registered for stage %q", stage)
		}

		// 交互拦截: 只在 key 节点且非循环迭代中询问
		if o.shouldAsk(st, stage) {
			skip, err := o.askBefore(stage)
			if err != nil {
				return err
			}
			if skip {
				fmt.Fprintf(o.out, "⏭  用户选择跳过节点 %s\n", stage)
				st.CurrentStage = o.edges[stage]
				if st.CurrentStage == "" {
					st.CurrentStage = state.StageDone
				}
				_ = o.store.Save()
				continue
			}
		}

		// 组装前序输出 (从文件读)
		prev := map[state.Stage]string{}
		for _, s := range []state.Stage{
			state.StageSpec, state.StagePlan, state.StagePlanReview,
			state.StageDev, state.StageReview, state.StageTest,
		} {
			if data := readNodeOutputAt(o.artifactDir, s, changeDir); data != "" {
				prev[s] = data
			}
		}

		in := &agents.RunInput{
			ChangeID:       st.ChangeID,
			WorkspaceDir:   st.WorkspaceDir,
			ChangeDir:      changeDir,
			ArtifactDir:    o.artifactDir,
			ProjectDir:     st.ProjectDir,
			ProposalPath:   proposalPath,
			PrevOutputs:    prev,
			PlanIteration:  st.PlanIteration,
			PlanFeedback:   st.PlanFeedback,
			CodeIteration:  st.CodeIteration,
			ReviewFeedback: st.ReviewFeedback,
			TestFeedback:   st.TestFeedback,
		}

		node := &state.NodeResult{
			Stage:     stage,
			Status:    state.StatusRunning,
			StartedAt: time.Now(),
		}
		st.Nodes[stage] = node
		_ = o.store.Save()
		_ = o.store.Emit(state.Event{Stage: stage, Type: "start"})

		iterTag := o.iterTag(st, stage)
		fmt.Fprintf(o.out, "\n▶ %s Agent 开始执行%s...\n", stage, iterTag)

		out, err := agent.Run(ctx, in)
		node.EndedAt = time.Now()
		if err != nil {
			node.Status = state.StatusFailed
			node.Error = err.Error()
			_ = o.store.Save()
			_ = o.store.Emit(state.Event{Stage: stage, Type: "error", Message: err.Error()})
			return fmt.Errorf("%s agent failed: %w", stage, err)
		}

		if out.Passed {
			node.Status = state.StatusPassed
		} else {
			node.Status = state.StatusFailed
		}
		node.Adapter = out.Adapter
		node.Summary = out.Summary
		_ = o.store.Save()
		_ = o.store.Emit(state.Event{Stage: stage, Type: "end",
			Message: fmt.Sprintf("passed=%t adapter=%s", out.Passed, out.Adapter)})

		passLabel := "✅ PASS"
		if !out.Passed {
			passLabel = "❌ FAIL"
		}
		fmt.Fprintf(o.out, "%s  %s Agent 完成 (adapter=%s)\n  产物: %s\n", passLabel, stage, out.Adapter, out.OutputPath)

		// 决定下一个节点 (含循环回跳与超限判断)
		st.CurrentStage = o.transition(st, stage, out, changeDir)
		_ = o.store.Save()
		// 每步都刷一次 tasks.md, 让人在线性路径下也能 cat 它看进度.
		// (模块化路径自己也会刷, 这里多刷一次无副作用.)
		_ = state.WriteTasksMD(o.artifactDir, st)
	}

	if st.CurrentStage == state.StageFailed {
		fmt.Fprintln(o.out, "\n🛑 循环超限, 流程终止, 请人工介入后再续跑.")
		_ = o.store.Emit(state.Event{Type: "failed"})
		return fmt.Errorf("workflow halted: exceeded loop budget")
	}

	fmt.Fprintln(o.out, "\n🎉 全流程完成.")
	_ = o.store.Emit(state.Event{Type: "done"})
	return nil
}

// transition 决定下一个节点. 核心逻辑:
//   - plan-review PASS 但未达 MinPlanLoops → 降级为 FAIL, 回 plan 再跑 (避免 AI 一眼放行)
//   - plan-review PASS 且 plan.md 缺少合法的 aswe-plan-modules YAML → 同样降级 FAIL
//   - plan-review FAIL → 回 plan (+计数); 达上限 → failed
//   - review/test FAIL → 回 dev (+计数); 达上限 → failed
//   - 其它节点走静态边.
func (o *Orchestrator) transition(st *state.State, cur state.Stage, out *agents.RunOutput, changeDir string) state.Stage {
	switch cur {
	case state.StagePlanReview:
		// 机器侧强校验: YAML 必须存在且合法, 否则不管 AI 怎么说都视为 FAIL.
		// 同时挂上 rescue: 若 artifact 里的 plan.md 缺关键 YAML 标记,
		// 扫几个 AI 可能用 Write 落盘的候选位置(workspace/projectDir/changeDir 等)
		// 找一份含标记的覆盖回去, 然后再校验.
		planMD := readPlanWithRescue(o.artifactDir, st.ProjectDir, changeDir, st.WorkspaceDir, st.ChangeID, o.out)
		yamlErr := validatePlanModules(planMD)
		// 最小轮数兜底: 第一轮(PlanIteration=0) 总算已完成 1 轮, 若 minPlanLoops=2
		// 则仅完成轮数 < 2 时要强制再跑; 仅在 AI 已判 PASS 且机校通过时才生效,
		// 避免 FAIL 路径重复加计数.
		completed := st.PlanIteration + 1
		if out.Passed && yamlErr == nil && completed < o.minPlanLoops {
			fmt.Fprintf(o.out, "ℹ️  plan<->plan-review 最小轮数未达 %d (已完成 %d), 强制再跑一轮以加深打磨\n",
				o.minPlanLoops, completed)
			out.Passed = false
			appendPlanReviewOverride(o.artifactDir,
				"调度器要求进行额外质量打磨, 本轮不放行进入 Dev. "+
					"请 Plan-Agent 不要只做表层润色, 必须重新检查并细化: spec 覆盖关系、模块依赖、"+
					"每个单元的 scope/deliverable 独立可验收性、实施步骤粒度、风险缓解和验收标准可衡量性.")
		}
		// 机校不过关 → 撤销 AI 的 PASS
		if out.Passed && yamlErr != nil {
			fmt.Fprintf(o.out, "⚠️  plan.md 缺少/不合法 aswe-plan-modules YAML (%v), 撤销本轮 PASS\n", yamlErr)
			out.Passed = false
			appendPlanReviewOverride(o.artifactDir, fmt.Sprintf(
				"机器校验失败: plan.md 未提供合法的 `# aswe-plan-modules` YAML 代码块.\n"+
					"请 Plan-Agent 下一轮在 plan.md 末尾补上符合规范的 YAML 块, 原因: %v", yamlErr))
		}

		if out.Passed {
			st.PlanFeedback = ""
			fmt.Fprintln(o.out, "✅ 方案评审通过, 进入代码实现阶段")
			return state.StageDev
		}
		if st.PlanIteration >= o.maxPlanLoops {
			fmt.Fprintf(o.out, "⚠️  方案循环已达上限 %d, 仍未通过\n", o.maxPlanLoops)
			return state.StageFailed
		}
		st.PlanIteration++
		st.PlanFeedback = readNodeOutputAt(o.artifactDir, state.StagePlanReview, changeDir)
		fmt.Fprintf(o.out, "🔁 方案未通过, 进入第 %d/%d 轮 plan<->plan-review 循环\n",
			st.PlanIteration, o.maxPlanLoops)
		return state.StagePlan

	case state.StageReview:
		if out.Passed {
			st.ReviewFeedback = ""
			return state.StageTest
		}
		if st.CodeIteration >= o.maxCodeLoops {
			fmt.Fprintf(o.out, "⚠️  代码循环已达上限 %d, 仍未通过 review\n", o.maxCodeLoops)
			return state.StageFailed
		}
		st.CodeIteration++
		st.ReviewFeedback = readNodeOutputAt(o.artifactDir, state.StageReview, changeDir)
		fmt.Fprintf(o.out, "🔁 代码评审未通过, 进入第 %d/%d 轮 dev 修复循环\n",
			st.CodeIteration, o.maxCodeLoops)
		return state.StageDev

	case state.StageTest:
		if out.Passed {
			st.TestFeedback = ""
			return state.StageDone
		}
		if st.CodeIteration >= o.maxCodeLoops {
			fmt.Fprintf(o.out, "⚠️  代码循环已达上限 %d, 测试仍未通过\n", o.maxCodeLoops)
			return state.StageFailed
		}
		st.CodeIteration++
		st.TestFeedback = readNodeOutputAt(o.artifactDir, state.StageTest, changeDir)
		fmt.Fprintf(o.out, "🔁 测试未通过, 进入第 %d/%d 轮 dev 修复循环\n",
			st.CodeIteration, o.maxCodeLoops)
		return state.StageDev
	}

	// 静态边
	if next, ok := o.edges[cur]; ok {
		return next
	}
	return state.StageDone
}

// shouldAsk 是否需要在当前节点前询问用户.
// 规则:
//   - auto     : 永远不问
//   - step     : 永远问
//   - interactive:
//   - 循环回跳的节点(PlanIteration>0 且 stage∈{plan,plan-review};
//     CodeIteration>0 且 stage∈{dev,review,test}): 不问
//   - key 节点 (spec/plan/dev): 询问
//   - 其它 : 不问
func (o *Orchestrator) shouldAsk(st *state.State, stage state.Stage) bool {
	switch o.mode {
	case config.ModeAuto:
		return false
	case config.ModeStep:
		return true
	case config.ModeInteractive:
		// 方案循环中不再问
		if st.PlanIteration > 0 && (stage == state.StagePlan || stage == state.StagePlanReview) {
			return false
		}
		// 代码循环中不再问
		if st.CodeIteration > 0 && (stage == state.StageDev || stage == state.StageReview || stage == state.StageTest) {
			return false
		}
		return o.keyNodes[stage]
	}
	return false
}

// iterTag 渲染 [plan iter=n/N] 或 [code iter=n/N] 标签.
func (o *Orchestrator) iterTag(st *state.State, stage state.Stage) string {
	switch stage {
	case state.StagePlan, state.StagePlanReview:
		if st.PlanIteration > 0 {
			return fmt.Sprintf(" [plan iter=%d/%d]", st.PlanIteration, o.maxPlanLoops)
		}
	case state.StageDev, state.StageReview, state.StageTest:
		if st.CodeIteration > 0 {
			return fmt.Sprintf(" [code iter=%d/%d]", st.CodeIteration, o.maxCodeLoops)
		}
	}
	return ""
}

func (o *Orchestrator) askBefore(stage state.Stage) (bool, error) {
	prompt := fmt.Sprintf("即将执行节点 [%s], 继续? (y=继续 / s=跳过 / q=退出): ", stage)
	for {
		fmt.Fprint(o.out, prompt)
		line, err := o.in.ReadString('\n')
		if err != nil && err != io.EOF {
			return false, err
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "", "y", "yes":
			return false, nil
		case "s", "skip":
			return true, nil
		case "q", "quit":
			return false, fmt.Errorf("user aborted at stage %s", stage)
		}
		// stdin 关闭 (EOF) 且没有有效输入 -> 退出, 避免在 CI / 管道场景死循环.
		if err == io.EOF {
			return false, fmt.Errorf("orchestrator askBefore stage=%s 遭遇 EOF (非交互环境? 请用 --mode auto)", stage)
		}
	}
}

// readNodeOutputAt 根据 stage 读 change 目录下对应文件.
func readNodeOutputAt(artifactDir string, s state.Stage, fallbackDirs ...string) string {
	var name string
	switch s {
	case state.StageSpec:
		name = "spec.md"
	case state.StagePlan:
		name = "plan.md"
	case state.StagePlanReview:
		name = "plan-review.md"
	case state.StageDev:
		name = "dev.md"
	case state.StageReview:
		name = "review.md"
	case state.StageTest:
		name = "test.md"
	default:
		return ""
	}
	dirs := append([]string{artifactDir}, fallbackDirs...)
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		data, err := os.ReadFile(dir + string(os.PathSeparator) + name)
		if err == nil {
			return string(data)
		}
	}
	return ""
}

// readPlanWithRescue 是 readNodeOutputAt(StagePlan) 的兜底加强版:
// 优先读 artifactDir/plan.md; 若该文件不含 "# aswe-plan-modules" marker,
// 就到 AI 可能 Write 落盘的几个候选路径(projectDir / changeDir / workspaceDir /
// workspaceDir/<change-id>/) 找一份带 marker 的覆盖回去, 后续校验/解析才能命中.
//
// 任何一个候选成功了, 都会把内容写回 artifactDir/plan.md 加注 rescue 头, 让用户
// 在 artifact 目录就能看到完整内容, 不必再去 projects 目录里翻.
func readPlanWithRescue(artifactDir, projectDir, changeDir, workspaceDir, changeID string, log io.Writer) string {
	const marker = "# aswe-plan-modules"
	primary := readNodeOutputAt(artifactDir, state.StagePlan, changeDir)
	if strings.Contains(primary, marker) {
		return primary
	}
	candidates := []string{
		filepath.Join(projectDir, "plan.md"),
		filepath.Join(changeDir, "plan.md"),
		filepath.Join(workspaceDir, "plan.md"),
	}
	if changeID != "" {
		candidates = append(candidates, filepath.Join(workspaceDir, changeID, "plan.md"))
	}
	for _, p := range candidates {
		if p == "" || p == filepath.Join(artifactDir, "plan.md") {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(data)
		if !strings.Contains(s, marker) {
			continue
		}
		// 写回 artifactDir/plan.md, 加上 rescue 头
		out := filepath.Join(artifactDir, "plan.md")
		header := fmt.Sprintf("<!-- rescued from %s by aswe orchestrator at %s -->\n\n",
			p, time.Now().Format(time.RFC3339))
		_ = os.WriteFile(out, []byte(header+s+"\n"), 0o644)
		if log != nil {
			fmt.Fprintf(log, "🛟 plan.md rescued: artifactDir 缺 YAML, 已从 %s 拷回\n", p)
		}
		return s
	}
	return primary
}
