package pm

import (
	"fmt"
	"regexp"
	"strings"
)

// buildClarifyPrompt 生成澄清追问的提示词.
// completed: 截至当前已完成的澄清轮数; minTurns: 进入 proposal 前**至少**要完成的轮数.
// 在未达到 minTurns 前, 会明确告诉模型"不允许 READY, 必须给出 1~3 个更精细的问题".
func buildClarifyPrompt(qa []qaPair, completed, minTurns int) string {
	var history strings.Builder
	for _, p := range qa {
		history.WriteString(fmt.Sprintf("[%s]\n%s\n\n", strings.ToUpper(p.Role), p.Content))
	}

	progress := fmt.Sprintf("当前是第 %d/%d 轮澄清(最少要完成 %d 轮才能进入 proposal 生成).",
		completed+1, minTurns, minTurns)

	var policy string
	if completed < minTurns {
		policy = `【重要】本轮**不允许**输出 READY, 必须继续追问.
请围绕以下维度**挑选尚未确认的 1~3 个最关键点**提出具体问题, 不要问空话、不要重复已经问过的:
  - 目标用户与使用场景
  - 核心功能与边界场景
  - 数据模型 / 持久化与同步策略
  - 非功能性需求 (性能/并发/隐私/离线/权限)
  - 验收标准与成功指标
  - 技术约束或已有系统集成点

只输出如下 QUESTIONS 块, 不要输出 READY, 不要其它寒暄:
QUESTIONS:
- 第一个问题
- 第二个问题`
	} else {
		policy = `评估当前信息是否足以写出一份完整的 OpenSpec proposal (包含 Why / What Changes / 目标用户 / 验收标准 / 关键非功能需求).
- 如果信息已足够, 只输出一行:
  READY
- 如果还不够, 输出 1~3 个最关键的追问 (问题要具体, 不要问"你还希望怎样"这种空话). 格式:
  QUESTIONS:
  - 第一个问题
  - 第二个问题

只输出 READY 或上述 QUESTIONS 块, 不要输出任何其它内容.`
	}

	return fmt.Sprintf(`你是一名资深产品经理, 任务是把用户的模糊需求澄清成可执行的软件需求.
%s

对话历史如下:
%s
请严格按以下规则输出, 不要有多余寒暄:

%s`, progress, history.String(), policy)
}

// defaultFallbackQuestions 当模型在"必须追问"的阶段却没给出任何问题时, 用这组默认问题兜底,
// 保证用户真的被问够 minTurns 轮. 依据当前已完成的轮数错位出题, 避免重复.
func defaultFallbackQuestions(completed int) []string {
	banks := [][]string{
		{
			"目标用户是谁?他们在什么场景下使用这个产品, 每天/每周会用多少次?",
			"最核心的 1~2 个使用流程能不能用'用户先做 X, 再做 Y, 最后看到 Z'的方式描述一遍?",
			"有没有必须接入的已有系统或账号体系(例如公司 SSO、微信、已有数据库)?",
		},
		{
			"核心数据是什么形状?需要持久化吗?多人之间怎么同步(实时/手动刷新/离线可用)?",
			"有没有关键的边界场景需要明确处理(例如并发编辑冲突、网络断开、权限不足)?",
			"对性能/容量有具体预期吗(例如单团队上限人数、单天操作量、响应时间)?",
		},
		{
			"这个产品什么样就算'做成了'?能给出 2~3 条可验证的验收标准吗?",
			"隐私与权限上有没有红线(谁能看/改/删什么数据, 是否需要审计日志)?",
			"上线形态是什么(Web/小程序/桌面/CLI)?是否要考虑国际化或多端一致?",
		},
	}
	idx := completed
	if idx < 0 {
		idx = 0
	}
	if idx >= len(banks) {
		idx = len(banks) - 1
	}
	return banks[idx]
}

// parseClarifyResponse 解析模型输出, 返回追问列表; 若已 ready 则第二个返回值为 true.
// 空输入既不视为 ready, 也不产生问题, 让上层再次尝试或报错.
func parseClarifyResponse(s string) ([]string, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, false
	}
	// 仅当一整行就是 READY(或 READY 开头且无其它有效问题) 时才视为就绪
	upper := strings.ToUpper(trimmed)
	if upper == "READY" || strings.HasPrefix(upper, "READY\n") {
		return nil, true
	}
	// 提取每一行以 "-" 或 "*" 或 数字. 开头的条目
	re := regexp.MustCompile(`(?m)^\s*(?:[-*]|\d+[\.)])\s+(.+)$`)
	matches := re.FindAllStringSubmatch(trimmed, -1)
	var qs []string
	for _, m := range matches {
		q := strings.TrimSpace(m[1])
		if q != "" {
			qs = append(qs, q)
		}
	}
	// 如果没匹配到列表但有内容, 退化成把非空行都当作问题
	if len(qs) == 0 {
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(strings.ToUpper(line), "QUESTIONS") {
				continue
			}
			qs = append(qs, line)
		}
	}
	return qs, false
}

// buildProposalPrompt 生成 proposal.md 的最终提示词.
func buildProposalPrompt(qa []qaPair) string {
	var history strings.Builder
	for _, p := range qa {
		history.WriteString(fmt.Sprintf("[%s]\n%s\n\n", strings.ToUpper(p.Role), p.Content))
	}
	return fmt.Sprintf(`基于下面的需求澄清对话, 输出一份符合 OpenSpec 规范的 proposal.md.

对话上下文:
%s
要求:
- 仅输出 markdown 内容, 首行是一个 H1 标题.
- 章节顺序: "## Why", "## What Changes", "## Target Users", "## Acceptance Criteria", "## Non-Functional Requirements", "## Impact".
- "## What Changes" 用无序列表列出拟新增的能力.
- 中文写作, 语言精炼, 切忌空话.
- 不要在 markdown 外添加解释或代码围栏.`, history.String())
}
