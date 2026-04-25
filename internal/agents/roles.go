package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aswe/aswe/internal/adapter"
	"github.com/aswe/aswe/internal/state"
)

// ---------- 安全边界约束 prompt ----------

// safetyRulesForActor 所有会真正动手写文件/跑命令的 Agent(Dev/Test) 都必须带上.
// 核心原则: 仅在 ProjectDir 内读写; 涉及全局变更必须停止并上报.
const safetyRulesForActor = `【重要安全边界, 必须严格遵守】
1. 你**只能**在工作目录 <PROJECT_DIR> 内部读写文件, 不得修改该目录之外的任何文件.
2. 不得修改系统全局配置 (PATH/环境变量/~/.bashrc/~/.zshrc 等).
3. 需要安装新的系统级依赖、全局 npm/pip 包、sudo 权限、修改防火墙等动作时,
   **禁止自行执行**, 必须在回答中以 "NEEDS_HUMAN_APPROVAL:" 开头的一行列出
   需要人类确认的命令, 然后停止后续动作.
4. 允许使用项目级虚拟环境 (venv / .venv / node_modules / go.mod 内依赖), 这些
   都在 <PROJECT_DIR> 之内, 不算全局变更.
5. 严禁执行删除家目录、rm -rf /、格式化磁盘等破坏性命令.
6. **本轮的"报告 markdown" (dev.md / test.md) 必须通过标准输出 (stdout) 完整返回**,
   不要用 Write/Edit 等工具把这份报告写到磁盘 — aswe 调度器会自己落盘.
   注意: 项目源代码、测试代码等"工程文件"应当照常用 Write/Edit 写到 <PROJECT_DIR> 内,
   这条限制只针对最终的 dev.md / test.md 报告本身.
`

// safetyRulesForReader 只读 Agent (Plan/PlanReview/CodeReview) 的规则较松:
// 只能读不能写; 不允许安装/执行项目外的命令.
//
// 注意: 第 4 条很重要 — 这些 Agent 的产物 (plan.md / plan-review.md / review.md / spec.md / test.md)
// 必须通过**标准输出**返回, 让 aswe 调度器统一落盘. 如果 AI 自己用 Write/Edit/Create 工具
// 把内容写到磁盘, stdout 往往只剩简短摘要, 后续机器校验(比如 plan.md 的 aswe-plan-modules
// YAML 块)会因读到的是缺内容的 stdout 副本而误判失败.
const safetyRulesForReader = `【安全边界】
1. 你是只读角色, 仅允许**读取** <PROJECT_DIR> 下的文件, 不得创建/修改/删除任何文件.
2. 不得安装任何依赖, 不得执行会产生副作用的命令.
3. 所有建议以纯 markdown 文字输出; 真正的代码改动由 Dev-Agent 在下一阶段进行.
4. **本轮的报告 (例如 plan.md / review.md / spec.md) 必须通过标准输出 (stdout) 完整返回**;
   严禁使用 Write / Edit / Create / fs_write 等工具把内容落盘到任何位置.
   aswe 调度器会自己把你的 stdout 内容写入对应 artifact 文件.
`

func withSafety(projectDir, rules, body string) string {
	r := strings.ReplaceAll(rules, "<PROJECT_DIR>", projectDir)
	return r + "\n" + body
}

// ==================================================================
// Spec Agent — 需求 → spec.md
// ==================================================================

type SpecAgent struct{ base }

func NewSpec(cli adapter.CLIAdapter) *SpecAgent {
	return &SpecAgent{base{stage: state.StageSpec, cli: cli, fileOut: "spec.md"}}
}

func (a *SpecAgent) Run(ctx context.Context, in *RunInput) (*RunOutput, error) {
	proposal := readIfExists(in.ProposalPath)
	prompt := fmt.Sprintf(`你是 Spec-Agent. 基于下面的 proposal, 输出一份 OpenSpec 规格文件 spec.md.

proposal.md:
----
%s
----

输出要求 (**完整 markdown 必须通过 stdout 返回, 不要用 Write 工具落盘**):
- 仅输出 markdown, 不要代码围栏.
- 章节: "## ADDED Requirements", 每个 Requirement 使用 "### Requirement:" 小节, 并在下面列出 1~2 个 "#### Scenario:" 描述 WHEN/THEN.
- 末尾单独一行输出: VERDICT: PASS`, proposal)

	out, raw, err := a.invoke(ctx, in, prompt)
	if err != nil {
		return nil, err
	}
	out.Passed = true
	out.Summary = firstLines(raw, 8)
	return out, nil
}

// ==================================================================
// Plan Agent — spec → plan.md (只写方案, 不动代码)
// ==================================================================

type PlanAgent struct{ base }

func NewPlan(cli adapter.CLIAdapter) *PlanAgent {
	return &PlanAgent{base{stage: state.StagePlan, cli: cli, fileOut: "plan.md"}}
}

func (a *PlanAgent) Run(ctx context.Context, in *RunInput) (*RunOutput, error) {
	spec := in.PrevOutputs[state.StageSpec]
	prevPlan := in.PrevOutputs[state.StagePlan] // 上一轮自己写的 plan.md, 第一次为空.

	// 反馈分两块: AI 反馈 + 机器改判 (后者可能写在 plan-review.md 末尾的 "[机器改判]" 块).
	feedbackSection := ""
	if strings.TrimSpace(in.PlanFeedback) != "" {
		feedbackSection = fmt.Sprintf(`
【上一轮 Plan-Review 反馈, 必须逐条解决】
----
%s
----
`, in.PlanFeedback)
	}

	// 上一轮 plan.md 仅作"参考资料"喂给模型, 但 ASWE 的写盘逻辑是**全量覆盖**:
	// 这一轮 stdout = 新 plan.md 的全部内容, 没有任何 diff / patch 合并。
	// 所以这里**不能**说"做最小化修改", 否则模型会只回 changelog, 落盘后 plan.md
	// 只剩几行说明, YAML 块整段消失, 校验立刻失败 (经验事故案例).
	prevPlanSection := ""
	if strings.TrimSpace(prevPlan) != "" {
		prevPlanSection = fmt.Sprintf(`
【上一轮你自己产出的 plan.md (仅作参考, 帮助你"复用已正确部分 + 定位需要改的地方")】
----
%s
----
**关键约束 — 必读**:
- ASWE 写盘是**全量覆盖**: 本轮 stdout 完整内容会原样替换 plan.md, 没有 diff 合并.
- 因此本轮 stdout 必须输出**完整、独立可读**的新 plan.md 全文,
  包含全部章节 (## 总体思路 / ## 技术选型 / ## 模块拆分 / ## 关键接口 /
  ## 实施步骤 / ## 风险与权衡 / ## 需要人类批准的事项), 以及末尾的
  # aswe-plan-modules YAML 块.
- **严禁**只输出 "我改了什么 / 已修复 X" 这种 changelog 风格的总结 ——
  那样会让 plan.md 被你这段总结整体替换, 上一轮的内容全部丢失.
- 沿用上一轮已正确的内容直接复制粘贴即可, 不会被罚, 但**不能省略**.
`, prevPlan)
	}

	body := fmt.Sprintf(`你是 Plan-Agent. 你的任务是**仅输出技术方案**, 现在还不能写代码.
等 Plan-Review 评审通过后, Dev-Agent 才会在下一阶段真正实现.

spec:
----
%s
----
%s%s
目标项目目录(尚未创建代码, 仅作规划锚点): %s

输出要求 (**完整 plan.md 必须通过标准输出 stdout 返回**, 不要使用 Write/Edit/Create 等工具把内容落盘到任何位置 — aswe 调度器会自行接收 stdout 并写入 plan.md). 仅 markdown, 不要额外代码围栏, 唯一允许的围栏是最后的 aswe-plan-modules YAML 块):
- "## 总体思路" : 3-5 句话说明实现思路.
- "## 技术选型" : 语言/框架/关键依赖及理由.
- "## 模块拆分" : 每个模块一行, 说明职责与文件位置(相对项目根).
- "## 关键接口" : 重要的函数/类/数据结构签名(伪代码形式列出即可, 不要实现).
- "## 实施步骤" : 有序列表, 描述 Dev-Agent 将按什么顺序完成. 每步粒度 1-3 个文件.
- "## 风险与权衡" : 潜在风险及如何缓解.
- "## 需要人类批准的事项" : 若涉及安装系统级依赖/全局工具, 在此列出; 否则写"无".
- "## 模块与单元拆分 (机器可读)" : **必填且必须严格符合下述格式**, 供调度器自动调度.
  - 用一个 yaml 代码块, 首行**必须**是 "# aswe-plan-modules" (首行以 # 开头做标识).
  - schema:
    modules:
      - id: A              # 模块 id, 字母或短标识, 全局唯一
        title: 数据模型与存储
        goal: 简述模块目标
        units:
          - id: A.1        # 单元 id, 全局唯一, 建议 <模块>.<序号>
            title: 简短标题
            scope: 本单元负责的文件或目录(相对项目根), 多条用逗号
            deliverable: 外部可验证的交付物 (例如导出的函数/类型/端点)
          - id: A.2
            title: ...
            scope: ...
            deliverable: ...
      - id: B
        ...
  - **拆分约束**:
    1. 每个单元必须**足够小**(一个单元的工作量建议控制在 1~3 个文件, 或一个较小的功能点).
    2. 同一模块内的**单元之间不得有依赖**(dev 可并行/流水线推进); 若有依赖请合并为一个单元.
    3. 模块之间允许依赖, 调度器会按数组顺序串行执行模块.
    4. 每个单元的 scope 应尽量不与其他单元重叠.
- 末尾单独一行: VERDICT: PASS (方案生成本身总是 PASS, 是否可用由 Plan-Review 决定)`,
		spec, prevPlanSection, feedbackSection, in.ProjectDir)

	prompt := withSafety(in.ProjectDir, safetyRulesForReader, body)
	out, raw, err := a.invokeWith(ctx, in, prompt, in.WorkspaceDir, 600)
	if err != nil {
		return nil, err
	}
	// Rescue: 部分 agentic CLI 不听话, 把完整 plan.md (含 aswe-plan-modules YAML)
	// 用 Write 工具落到了 cwd 或 ProjectDir, 而 stdout 只回了简短摘要. 这里若 raw
	// 缺标记, 就到那些候选路径找一份含 marker 的覆盖回 outPath, 后续校验才能命中.
	if rescued, ok := rescueArtifactByMarker(in, out.OutputPath, "plan.md", raw, "# aswe-plan-modules"); ok {
		raw = rescued
	}
	// 双层校验: 先用 looksLikePartialPlan 粗筛"AI 偷懒只回 diff/changelog"的典型反例,
	// 给出比 YAML 解析错更直观的原因; 再用 state.ExtractPlanModules 严格校验 YAML.
	// 任一失败都进入 repair 轮, 重发 prompt 让模型重新输出完整 plan.md.
	var repairReason error
	if reason, ok := looksLikePartialPlan(raw); !ok {
		repairReason = fmt.Errorf("%s", reason)
	} else if _, err := state.ExtractPlanModules(raw); err != nil {
		repairReason = err
	}
	if repairReason != nil {
		repairPrompt := withSafety(in.ProjectDir, safetyRulesForReader, buildPlanRepairPrompt(raw, repairReason))
		out, raw, err = a.invokeWith(ctx, in, repairPrompt, in.WorkspaceDir, 600)
		if err != nil {
			return nil, err
		}
		// 修复轮也跑一次 rescue, 防止 AI 又去用 Write
		if rescued, ok := rescueArtifactByMarker(in, out.OutputPath, "plan.md", raw, "# aswe-plan-modules"); ok {
			raw = rescued
		}
	}
	out.Passed = true // Plan 本身总 PASS, 让 Plan-Review 来裁决
	out.Summary = firstLines(raw, 12)
	return out, nil
}

// looksLikePartialPlan 在严格 YAML 解析(ExtractPlanModules)之前粗筛 stdout 是否
// 明显不是一份"完整 plan.md", 而只是模型偷懒回的 changelog/diff 摘要.
//
// 触发条件 (任一命中视为可疑):
//  1. 长度 < 600 个 rune (一份合规 plan.md 通常 1500+);
//  2. 缺少 "## 模块拆分" 或 "## 实施步骤" 这两个核心章节;
//  3. 缺少 "# aswe-plan-modules" 机器可读标识行.
//
// 这一层的存在是为了让 repair 轮的 prompt 能针对"你只回了 diff"这种症状下药,
// 而不是单纯报"YAML 解析失败" — 后者会让 AI 误以为只是 YAML 写错, 继续偷懒.
func looksLikePartialPlan(raw string) (string, bool) {
	body := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(body); n < 600 {
		return fmt.Sprintf("plan.md 长度仅 %d 字, 远低于完整方案的最低规模(≥600), 疑似只输出了 changelog/摘要", n), false
	}
	required := []string{"## 模块拆分", "## 实施步骤"}
	var missing []string
	for _, h := range required {
		if !strings.Contains(body, h) {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		return "plan.md 缺少关键章节: " + strings.Join(missing, ", "), false
	}
	if !strings.Contains(body, "# aswe-plan-modules") {
		return "plan.md 缺少 '# aswe-plan-modules' 标识行 (机器可读 YAML 块未输出)", false
	}
	return "", true
}

func buildPlanRepairPrompt(plan string, reason error) string {
	fence := "```"
	planRunes := utf8.RuneCountInString(strings.TrimSpace(plan))
	// 当原文本身只有几百字时, 它大概率就是 changelog/摘要; prompt 里要明确告诉模型
	// "原文不是真正的 plan.md, 不要在它基础上修补, 必须从 spec 重新长出完整方案".
	suspectChangelog := ""
	if planRunes < 600 {
		suspectChangelog = fmt.Sprintf(`

**⚠️ 注意: 上面的"原始 plan.md"长度仅 %d 字, 几乎可以确定它**不是**一份完整方案,
而是上一轮 Plan-Agent 偷懒只回了一段"我改了什么"的 changelog/摘要,
然后被 ASWE 当作 plan.md 整体落盘. 你这一轮**不要**在它基础上"修补",
请回到 spec 重新写一份完整、独立可读的 plan.md.**`, planRunes)
	}
	return fmt.Sprintf(`你是 Plan-Agent. 下面这份 plan.md 未通过机器校验, 原因是:
%v

请输出一份完整修复后的 plan.md, 保留原有技术方案内容, 但必须在末尾补上唯一的机器可读 YAML 代码块.

原始 plan.md:
----
%s
----%s

强制要求:
1. 只输出完整 markdown, 不要解释, 不要在全文外包裹代码围栏.
2. **必须包含全部章节** (## 总体思路 / ## 技术选型 / ## 模块拆分 / ## 关键接口 /
   ## 实施步骤 / ## 风险与权衡 / ## 需要人类批准的事项), 任一章节缺失都会再次被打回.
3. 末尾必须包含 "## 模块与单元拆分 (机器可读)".
4. 该章节下面必须且只能有一个 yaml fenced code block, 格式必须如下:

%syaml
# aswe-plan-modules
modules:
  - id: A
    title: 模块标题
    goal: 模块目标
    units:
      - id: A.1
        title: 单元标题
        scope: 相对项目根的文件或目录
        deliverable: 可验证交付物
%s

5. 每个 module 必须有 id/title/units; 每个 unit 必须有 id/title/scope/deliverable.
6. id 必须全局唯一; 每个模块至少一个 unit.
7. **严禁**只输出 "我改了什么" / "已修复 X" 这种 changelog —— ASWE 写盘是全量覆盖,
   只回 changelog 会让 plan.md 仅剩你这段总结, 整篇方案被抹掉.
8. 最后一行仍然输出: VERDICT: PASS`, reason, plan, suspectChangelog, fence, fence)
}

// ==================================================================
// Plan-Review Agent — plan.md → 评审意见 (决定是否放行 Dev)
// ==================================================================

type PlanReviewAgent struct{ base }

func NewPlanReview(cli adapter.CLIAdapter) *PlanReviewAgent {
	return &PlanReviewAgent{base{stage: state.StagePlanReview, cli: cli, fileOut: "plan-review.md"}}
}

func (a *PlanReviewAgent) Run(ctx context.Context, in *RunInput) (*RunOutput, error) {
	spec := in.PrevOutputs[state.StageSpec]
	plan := in.PrevOutputs[state.StagePlan]

	body := fmt.Sprintf(`你是 Plan-Review-Agent. 对 Plan-Agent 产出的方案做严格评审, 判定是否允许进入 Dev 阶段真正写代码.
不要因为已经评审过若干轮就放行; 只能根据方案质量和可实施性判断 PASS/FAIL.

spec:
----
%s
----

plan.md:
----
%s
----

评审维度(逐条回答是否满足, 不要泛泛):
1. 方案是否**全部覆盖** spec 中每一条 Requirement 的每个 Scenario?
2. 模块拆分是否合理, 没有职责重叠或空心模块?
3. 关键接口签名是否清晰、类型明确, 对 Dev-Agent 可实施?
4. 实施步骤是否按依赖顺序排列, 每步粒度合理?
5. 风险清单是否识别到真实风险并提出缓解?
6. 是否存在**模糊或缺失**的设计点(比如错误处理/并发/持久化策略没说)?
7. **aswe-plan-modules YAML 块是否存在且规范**:
   - 有且仅有一个以 "# aswe-plan-modules" 开头的 yaml 代码块.
   - 每个 module 含 id/title/units, 每个 unit 含 id/title/scope/deliverable, id 全局唯一.
   - 单元粒度合理 (每个单元 1~3 文件或一个小功能点), 过粗/过细都应打回.
   - 同一模块内单元之间不得存在依赖 (Dev 会按 FIFO 并行推进).
   - scope 基本不重叠, 避免多个单元改同一文件打架.

输出要求 (**markdown 必须通过 stdout 返回**, 不要用 Write 等工具落盘 plan-review.md):
- "## 结论" : 一句话说明是否通过方案评审.
- "## 放行门槛" : 用 STATUS: READY 或 STATUS: NEEDS_MORE_WORK 表明方案是否真的可进入 Dev; 若仍有阻塞缺口, 必须列出 MISSING: 清单.
- "## 逐项评估" : 对上面 7 点逐一作答, 形式: "N. 标题 — ✅/❌ 说明".
- "## 问题清单" : 若有问题, 按严重度(阻塞/建议)列出, 每条指向 plan.md 的具体章节或行文.
- "## 给 Plan-Agent 的具体反馈" : 只在 FAIL 时填, 明确告诉下一轮 Plan-Agent 应当补充/修改哪些内容, 越具体越好.
- 末尾单独一行:
	- 若存在任何阻塞级问题或 Requirement 未覆盖 -> VERDICT: FAIL
	- 否则 -> VERDICT: PASS`, spec, plan)

	prompt := withSafety(in.ProjectDir, safetyRulesForReader, body)
	out, raw, err := a.invokeWith(ctx, in, prompt, in.WorkspaceDir, 600)
	if err != nil {
		return nil, err
	}
	out.Passed = parseVerdict(raw) && parsePlanReviewReadiness(raw)
	out.Summary = firstLines(raw, 12)
	return out, nil
}

// ==================================================================
// Dev Agent — 基于已批准的 plan.md 真实写代码
// ==================================================================

type DevAgent struct{ base }

func NewDev(cli adapter.CLIAdapter) *DevAgent {
	return &DevAgent{base{stage: state.StageDev, cli: cli, fileOut: "dev.md"}}
}

func (a *DevAgent) Run(ctx context.Context, in *RunInput) (*RunOutput, error) {
	if in.ProjectDir == "" {
		return nil, fmt.Errorf("DevAgent: ProjectDir 未配置")
	}
	if err := os.MkdirAll(in.ProjectDir, 0o755); err != nil {
		return nil, fmt.Errorf("DevAgent: 创建 ProjectDir 失败: %w", err)
	}

	spec := in.PrevOutputs[state.StageSpec]
	plan := in.PrevOutputs[state.StagePlan] // 关键: Dev 现在基于 plan, 不再裸啃 spec
	existingFiles := listProjectTree(in.ProjectDir, 5)

	iterBadge := ""
	if in.CodeIteration > 0 {
		iterBadge = fmt.Sprintf(" (第 %d 轮代码迭代)", in.CodeIteration)
	}
	var feedbackSection strings.Builder
	if strings.TrimSpace(in.ReviewFeedback) != "" {
		fmt.Fprintf(&feedbackSection, `
【上一轮 Code-Review 反馈, 必须逐条解决】
----
%s
----
`, in.ReviewFeedback)
	}
	if strings.TrimSpace(in.TestFeedback) != "" {
		fmt.Fprintf(&feedbackSection, `
【上一轮 Test 失败信息, 必须修复】
----
%s
----
`, in.TestFeedback)
	}

	body := fmt.Sprintf(`你是 Dev-Agent%s. 你的任务是**严格按照已通过评审的 plan.md, 在工作目录 %s 下真实创建/修改代码**,
产出完整可运行的项目. 不是再写计划, 是直接写代码.

spec (需求真源, 最终验收标准):
----
%s
----

plan.md (已通过 Plan-Review 的实施方案, 应严格遵循):
----
%s
----
%s
当前项目目录已有的文件清单:
----
%s
----

实施要求:
1. 按 plan 的 "实施步骤" 依次实现, 每步一次小而完整的提交(先最小可运行骨架, 再补细节).
2. 如果发现 plan 里有错误或遗漏, **不要擅自大改方向**, 在 dev.md 里记录矛盾项, 并以最小改动推进.
3. 若已存在文件, 按需修改; 不要无谓地重写未变更的文件.
4. 新增依赖必须写进项目级依赖清单 (requirements.txt / package.json / go.mod), 不要 sudo 或全局安装.
5. 所有文件路径必须在 %s 之内.

输出要求 (**这是你给编排系统的答复, 完整 markdown 必须通过 stdout 返回**, 不要把 dev.md 这份"报告本身"用 Write 落盘 — aswe 会自行接收 stdout 并写入 dev.md; 但项目源代码、测试代码等"工程文件"应当照常用 Write 写入 ProjectDir):
- "## 实现摘要" : 简述本轮做了什么.
- "## 变更文件清单" : 列出你实际创建或修改的文件, 格式 "- path/to/file — 说明".
- "## 与 plan 的偏差" : 若有偏离 plan 之处, 逐条说明并给出理由; 没有就写"无".
- "## 如何运行" : 本地准备 + 启动命令.
- "## 需要人类批准" : 若遇到需要 sudo/全局安装的情况, 按 NEEDS_HUMAN_APPROVAL: 开头列出命令.
- 末尾单独一行: 若本轮实现完成且无阻塞 -> VERDICT: PASS; 有阻塞 -> VERDICT: FAIL.`,
		iterBadge, in.ProjectDir, spec, plan, feedbackSection.String(), existingFiles, in.ProjectDir)

	prompt := withSafety(in.ProjectDir, safetyRulesForActor, body)
	out, raw, err := a.invokeWith(ctx, in, prompt, in.ProjectDir, 1200)
	if err != nil {
		return nil, err
	}
	out.Passed = parseVerdict(raw)
	out.Summary = firstLines(raw, 12)
	return out, nil
}

// ==================================================================
// Code-Review Agent — 读代码给出 PASS/FAIL
// ==================================================================

type ReviewAgent struct{ base }

func NewReview(cli adapter.CLIAdapter) *ReviewAgent {
	return &ReviewAgent{base{stage: state.StageReview, cli: cli, fileOut: "review.md"}}
}

func (a *ReviewAgent) Run(ctx context.Context, in *RunInput) (*RunOutput, error) {
	spec := in.PrevOutputs[state.StageSpec]
	plan := in.PrevOutputs[state.StagePlan]
	devReport := in.PrevOutputs[state.StageDev]
	tree := listProjectTree(in.ProjectDir, 6)

	iterBadge := ""
	if in.CodeIteration > 0 {
		iterBadge = fmt.Sprintf(" (第 %d 轮代码评审)", in.CodeIteration)
	}

	body := fmt.Sprintf(`你是 Code-Review-Agent%s. 真实地阅读 %s 下的源代码, 对照 spec 和 plan 给出审查结论.
你可以读取该目录下任意文件. 不要改动任何代码.

spec:
----
%s
----

plan.md (已通过方案评审):
----
%s
----

Dev 交付报告 (dev.md):
----
%s
----

项目目录树:
----
%s
----

输出要求 (**markdown 必须通过 stdout 返回**, 不要用 Write 等工具落盘 review.md):
- "## 结论" : 一句话是否通过.
- "## 对 Spec 的覆盖" : 列出 spec 每个 Requirement, 标注 ✅/❌, 并引用对应代码位置 (文件:行号 或 文件:函数).
- "## 对 Plan 的符合度" : 是否按照 plan 的模块拆分与接口实现; 偏差是否合理.
- "## 问题清单" : 每项编号, 严重度(阻塞/建议)、文件位置、修复建议.
- "## 给 Dev 的具体反馈" : 仅 FAIL 时填, 每条指向文件/函数, 说明该怎么改.
- 末尾单独一行:
	- 有阻塞级问题或 Requirement 未覆盖 -> VERDICT: FAIL
	- 否则 -> VERDICT: PASS`, iterBadge, in.ProjectDir, spec, plan, devReport, tree)

	prompt := withSafety(in.ProjectDir, safetyRulesForReader, body)
	out, raw, err := a.invokeWith(ctx, in, prompt, in.ProjectDir, 600)
	if err != nil {
		return nil, err
	}
	out.Passed = parseVerdict(raw)
	out.Summary = firstLines(raw, 12)
	return out, nil
}

// ==================================================================
// Test Agent — 真跑测试
// ==================================================================

type TestAgent struct{ base }

func NewTest(cli adapter.CLIAdapter) *TestAgent {
	return &TestAgent{base{stage: state.StageTest, cli: cli, fileOut: "test.md"}}
}

func (a *TestAgent) Run(ctx context.Context, in *RunInput) (*RunOutput, error) {
	spec := in.PrevOutputs[state.StageSpec]
	devReport := in.PrevOutputs[state.StageDev]
	tree := listProjectTree(in.ProjectDir, 6)

	// 按项目类型分流: 不同类型用不同的测试策略, 避免在纯静态前端项目里被迫
	// 搭建/安装整套测试框架, 导致 TestAgent 几分钟起步.
	kind := detectProjectKind(in.ProjectDir)

	var strategy string
	timeoutSec := 1200
	switch kind {
	case "go":
		strategy = `【项目类型: Go】
1. 仅使用项目**已有**的测试栈: 只能跑 "go test ./..." (可加 -run/-v); 不得引入新的测试框架.
2. 若无任何 *_test.go, 请**就地生成**最必要的 1-3 个 _test.go 文件覆盖 spec 关键 Scenario, 然后执行.
3. 绝对禁止任何全局安装 / 修改 GOPATH / GOROOT / 代理配置.`
	case "node":
		strategy = `【项目类型: Node.js】
1. 优先使用项目**已声明**的 test 脚本: "npm test" 或 package.json 中已有的 test script.
2. **不得** npm install / yarn add / pnpm add 任何新的测试框架 (jest/vitest/mocha 等都不允许新增).
3. 若项目内没有任何测试且没有现成测试框架, 用 Node 内置能力: 写若干 *.test.mjs 用 node 自带的 node:test + node:assert 执行.
4. 最多允许运行 "npm ci" / "npm install --no-save" 以恢复**已经声明过**的依赖, 且必须带 --no-fund --no-audit 以加速.`
	case "python":
		strategy = `【项目类型: Python】
1. 仅使用项目**已声明**的测试栈 (requirements/pyproject 中已出现的 pytest/unittest).
2. 若没有任何测试框架声明, 使用 Python 标准库 unittest 即可, 不要 pip install 新包.
3. 若必须安装依赖, 仅允许 "pip install -r requirements.txt" 恢复已声明依赖; 禁止单独 pip install 新包.`
	case "static":
		timeoutSec = 300
		strategy = `【项目类型: 纯静态前端 (HTML/CSS/JS, 无 package.json)】
**严禁**执行以下命令 (它们都是无意义的环境搭建, 会把本阶段拖到几分钟):
  - npm init / npm install / npx / yarn / pnpm / vitest / jest / jsdom / playwright / puppeteer / cypress
  - pip install / sudo / apt / brew
只允许做以下**轻量静态自检**:
1. 文件结构检查: 列出 HTML/CSS/JS 文件, 确认 spec 要求的入口文件存在.
2. HTML 合法性自检: 用 grep/awk 检查关键标签 (<canvas>, <script>, <button> 等) 是否存在且配对.
3. JS 语法检查: 对每个 *.js 或内联脚本, 用 "node --check <file>" 检查语法; 若是内联脚本, 可以 grep 抽取到临时文件后再 node --check.
4. 关键函数单测: 若 spec 提到关键纯函数 (如 checkCollision/spawnFood), 可将其逻辑抽取到一个临时 test.mjs, 用 node 内置 node:test + node:assert 做 1-3 个断言; 临时文件放在项目根, 命名 *.test.mjs 即可.
5. 端到端交互**不要求**真跑浏览器, 在 test.md 里以 "手工验证用例" 的形式列出 spec 里每个 Scenario 对应的手动操作步骤即可.

时间预算: 整个静态自检应在 1-2 分钟内完成, 不要尝试搭建浏览器测试环境.`
	default: // unknown
		timeoutSec = 300
		strategy = `【项目类型: 未识别 / 无依赖清单】
回退为轻量静态自检策略:
1. 列出主要文件并确认 spec 要求的产物已经生成.
2. 对可执行脚本做语法检查 (bash -n / node --check / python -m py_compile 等, 仅使用系统自带工具).
3. **不要**执行 npm install / pip install / go mod download 这类会联网的动作.
4. 在 test.md 里为 spec 的每个 Scenario 给出"手工验证步骤"作为补充.
时间预算: 控制在 1-2 分钟.`
	}

	body := fmt.Sprintf(`你是 Test-Agent. 真实地在 %s 下按下面的策略做验证, 把执行结果如实写入 test.md.

%s

spec:
----
%s
----

Dev 交付报告:
----
%s
----

项目目录树:
----
%s
----

通用要求:
1. 所有命令只能在 %s 之内执行, 不得 sudo/全局安装.
2. 把执行的**真实输出片段**贴进 test.md (至少包含命令本身和关键返回).
3. 若因为环境限制无法执行某一步, 如实写明, 不要编造输出.

输出要求 (**markdown 必须通过 stdout 返回**, 不要用 Write 等工具落盘 test.md):
- "## 项目类型识别" : 一句话说明本次判定的项目类型, 以及据此选择的验证策略.
- "## 测试环境准备" : 列出你实际执行的准备命令及其结果 (没有则写"无").
- "## 执行的验证命令" : 每条命令一行.
- "## 结果摘要" : 通过/失败/跳过 的数量, 或静态自检的通过项数.
- "## 失败详情" : 若有失败, 贴出完整输出.
- "## 手工验证用例" : 仅静态/未识别项目填, 对应 spec 每个 Scenario 的手动操作步骤.
- "## 给 Dev 的反馈" : 仅 FAIL 时填, 每条明确指向文件和修复建议.
- 末尾单独一行:
	- 全部通过 -> VERDICT: PASS
	- 任一硬失败 -> VERDICT: FAIL
	- 无法运行验证(环境/权限) -> VERDICT: FAIL, 并在失败详情里说明原因.`,
		in.ProjectDir, strategy, spec, devReport, tree, in.ProjectDir)

	prompt := withSafety(in.ProjectDir, safetyRulesForActor, body)
	out, raw, err := a.invokeWith(ctx, in, prompt, in.ProjectDir, timeoutSec)
	if err != nil {
		return nil, err
	}
	out.Passed = parseVerdict(raw)
	out.Summary = firstLines(raw, 12)
	return out, nil
}

// ==================================================================
// 工具
// ==================================================================

// listProjectTree 返回相对 ProjectDir 的文件树字符串, 仅用于 prompt 上下文.
// maxProjectTreeEntries 每次注入到 prompt 的目录条目上限.
// 1500 条对应大约 60~150KB 的纯文本, 已经能让模型看清结构, 又不会撑爆 prompt.
// 老项目走到这里通常是 node_modules 没忽略干净 / 有大数据集, 截断比塞爆更合适.
const maxProjectTreeEntries = 1500

func listProjectTree(root string, maxDepth int) string {
	if root == "" {
		return "(未配置 ProjectDir)"
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return "(目录尚不存在, 将由 Dev-Agent 创建)"
	}
	type entry struct {
		rel string
		dir bool
	}
	var entries []entry
	truncated := false
	skipDir := map[string]bool{
		"node_modules": true, ".git": true, ".venv": true, "venv": true,
		"__pycache__": true, "dist": true, "build": true, ".next": true, "target": true,
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if depth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		if info.IsDir() && (skipDir[base] || strings.HasPrefix(base, ".")) {
			return filepath.SkipDir
		}
		if len(entries) >= maxProjectTreeEntries {
			truncated = true
			return filepath.SkipDir
		}
		entries = append(entries, entry{rel: rel, dir: info.IsDir()})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	if len(entries) == 0 {
		return "(空目录)"
	}
	var b strings.Builder
	for _, e := range entries {
		suffix := ""
		if e.dir {
			suffix = "/"
		}
		fmt.Fprintf(&b, "%s%s\n", e.rel, suffix)
	}
	if truncated {
		fmt.Fprintf(&b, "\n... (目录过大, 仅展示前 %d 条; 完整结构请用 ls/tree 查看)\n",
			maxProjectTreeEntries)
	}
	return b.String()
}

// detectProjectKind 根据 ProjectDir 根目录(仅第一层) 的标志文件快速判断项目类型.
// 返回值: "go" | "node" | "python" | "static" | "unknown".
// 这里只做根目录一层检测, 避免深度遍历拖慢 TestAgent 启动.
func detectProjectKind(root string) string {
	if root == "" {
		return "unknown"
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return "unknown"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "unknown"
	}
	hasStaticAsset := false
	hasOtherFile := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		switch name {
		case "go.mod":
			return "go"
		case "package.json":
			return "node"
		case "requirements.txt", "pyproject.toml", "setup.py", "setup.cfg", "Pipfile":
			return "python"
		}
		low := strings.ToLower(name)
		switch {
		case strings.HasSuffix(low, ".html"), strings.HasSuffix(low, ".htm"),
			strings.HasSuffix(low, ".css"), strings.HasSuffix(low, ".js"),
			strings.HasSuffix(low, ".mjs"), strings.HasSuffix(low, ".svg"),
			strings.HasSuffix(low, ".png"), strings.HasSuffix(low, ".jpg"),
			strings.HasSuffix(low, ".jpeg"), strings.HasSuffix(low, ".gif"),
			strings.HasSuffix(low, ".ico"), strings.HasSuffix(low, ".webp"):
			hasStaticAsset = true
		case name == "README.md", name == "readme.md", strings.HasPrefix(name, "."):
			// 忽略 README 与隐藏文件, 不影响分类
		default:
			hasOtherFile = true
		}
	}
	if hasStaticAsset && !hasOtherFile {
		return "static"
	}
	return "unknown"
}

func parsePlanReviewReadiness(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		u := strings.ToUpper(strings.TrimSpace(line))
		if !strings.HasPrefix(u, "STATUS:") {
			continue
		}
		switch {
		case strings.Contains(u, "NEEDS_MORE_WORK"), strings.Contains(u, "NEEDS_MORE_INFO"), strings.Contains(u, "FAIL"):
			return false
		case strings.Contains(u, "READY"), strings.Contains(u, "PASS"):
			return true
		}
	}
	// 兼容旧输出: 没有 STATUS 时仍由 VERDICT 决定.
	return true
}

// firstLines 取前 n 行, 给 summary 用.
func firstLines(s string, n int) string {
	var out []string
	count := 0
	for _, l := range splitLines(s) {
		if count >= n {
			break
		}
		out = append(out, l)
		count++
	}
	return joinLines(out)
}
