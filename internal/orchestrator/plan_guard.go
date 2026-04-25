// plan_guard.go — Plan-Review 的机器侧硬兜底辅助.
//
// 为什么独立文件? 避免把两块纯粹的工具函数塞进已经较长的 graph.go.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aswe/aswe/internal/state"
)

// validatePlanModules 校验 plan.md 里嵌入的 aswe-plan-modules YAML 是否存在且合法.
// 返回 nil 表示可以放行; 返回 error 时调用方应把 Plan-Review 的结果降级为 FAIL.
//
// 这是对 AI "自评 PASS" 的机器兜底: 即便 Plan-Review-Agent 在 plan-review.md 里
// 写了 VERDICT: PASS, 只要 plan.md 没有合法 YAML, 下游的模块化流水线就跑不起来,
// 那就应该回到 Plan-Agent 重新给. ParsePlanModules 本身已经把"缺标识块/空 module/
// 空 unit/id 重复"等情况视为 error, 我们直接复用.
func validatePlanModules(planMarkdown string) error {
	if _, err := state.ExtractPlanModules(planMarkdown); err != nil {
		return err
	}
	return nil
}

// appendPlanReviewOverride 在 plan-review.md 末尾追加一段"机器改判"说明,
// 让下一轮 Plan-Agent 能从 PlanFeedback 里直接看到机器裁决的原因.
// 失败不阻塞流程: 追加写入只是附加信息, 真正的状态转换由 transition 负责.
func appendPlanReviewOverride(changeDir, reason string) {
	if changeDir == "" || reason == "" {
		return
	}
	path := filepath.Join(changeDir, "plan-review.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f,
		"\n\n---\n**[机器改判 at %s]** 本轮 VERDICT 被调度器改判为 **FAIL**.\n\n原因: %s\n",
		time.Now().Format(time.RFC3339), reason)
}
