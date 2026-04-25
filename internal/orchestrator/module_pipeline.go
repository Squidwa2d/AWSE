package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aswe/aswe/internal/agents"
	"github.com/aswe/aswe/internal/state"
)

// DefaultUnitLoops 单元默认循环上限.
const DefaultUnitLoops = 8

// unitAgents 单元化 Agent 三件套.
type unitAgents struct {
	dev    *agents.DevUnitAgent
	review *agents.ReviewUnitAgent
	test   *agents.TestUnitAgent
}

// runModulePipeline 在 plan-review 通过后调用.
//   - 解析 plan.md 中的 aswe-plan-modules YAML; 若解析失败, 返回 (false, nil) 让调用方
//     回退到旧的线性 dev↔review↔test 路径.
//   - 否则进入模块化流水线, 执行到 done / failed 并更新 State.CurrentStage.
//
// 返回 (handled, err): handled=true 表示模块化流程已处理, 调用方直接结束本次循环体.
func (o *Orchestrator) runModulePipeline(ctx context.Context, changeDir string, ua *unitAgents) (bool, error) {
	st := o.store.State()

	// 若 state 里还没有模块, 尝试从 plan.md 解析
	if len(st.Modules) == 0 {
		planMD := readNodeOutputAt(changeDir, state.StagePlan)
		mods, err := state.ExtractPlanModules(planMD)
		if err != nil {
			fmt.Fprintf(o.out, "ℹ️  plan.md 未提供机器可读模块拆分 (%v), 回退到线性流水线\n", err)
			return false, nil
		}
		st.Modules = mods
		if st.MaxUnitLoops <= 0 {
			st.MaxUnitLoops = DefaultUnitLoops
		}
		_ = o.store.Save()
		fmt.Fprintf(o.out, "📦 解析到 %d 个模块, 进入模块化流水线 (每单元最多 %d 轮)\n",
			len(mods), st.MaxUnitLoops)
		o.printModuleOverview(st)
	}

	// 进入模块循环, 逐模块完成
	spec := readNodeOutputAt(changeDir, state.StageSpec)
	plan := readNodeOutputAt(changeDir, state.StagePlan)

	for {
		st = o.store.State()
		mod := st.ActiveModule()
		if mod == nil {
			// 所有模块 done
			st.CurrentStage = state.StageDone
			_ = o.store.Save()
			_ = state.WriteTasksMD(changeDir, st)
			fmt.Fprintln(o.out, "🎉 所有模块已完成.")
			return true, nil
		}

		if mod.Status == state.ModulePending {
			mod.Status = state.ModuleRunning
			_ = o.store.Save()
			fmt.Fprintf(o.out, "\n== 进入模块 %s — %s ==\n", mod.ID, mod.Title)
		}

		// 若当前模块已失败 -> 流程暂停
		if mod.HasFailed() {
			mod.Status = state.ModuleFailed
			st.CurrentStage = state.StageFailed
			_ = o.store.Save()
			_ = state.WriteTasksMD(changeDir, st)
			fmt.Fprintf(o.out, "🛑 模块 %s 中存在超限失败单元, 流程暂停, 请人工介入.\n", mod.ID)
			return true, fmt.Errorf("module %s failed: unit loop budget exceeded", mod.ID)
		}

		unit := mod.NextRunnableUnit()
		if unit == nil {
			// 模块内没有需要执行的单元了; 但 mod.IsDone 可能为 true
			if mod.IsDone() {
				mod.Status = state.ModuleDone
				_ = o.store.Save()
				_ = state.WriteTasksMD(changeDir, st)
				fmt.Fprintf(o.out, "✅ 模块 %s 全部单元 done\n", mod.ID)
				continue
			}
			// 不应发生 (既没可跑的 unit 也没全 done), 防御性退出
			st.CurrentStage = state.StageFailed
			_ = o.store.Save()
			return true, fmt.Errorf("module %s stuck: no runnable unit and not done", mod.ID)
		}

		if err := o.runOneUnitStep(ctx, changeDir, st, mod, unit, spec, plan, ua); err != nil {
			return true, err
		}

		// 每步都渲染 tasks.md, 让人可以实时看到进度
		_ = state.WriteTasksMD(changeDir, st)
		_ = o.store.Save()
	}
}

// runOneUnitStep 根据 unit 当前状态决定执行 dev / review / test 中的哪一步.
// FIFO 队列由 Module.NextRunnableUnit 负责, 本函数只负责推进"这一个被选中的单元"一步.
func (o *Orchestrator) runOneUnitStep(
	ctx context.Context, changeDir string, st *state.State,
	mod *state.Module, u *state.Unit, spec, plan string, ua *unitAgents,
) error {
	maxLoops := st.MaxUnitLoops
	if maxLoops <= 0 {
		maxLoops = DefaultUnitLoops
	}
	in := &agents.UnitInput{
		ChangeID:     st.ChangeID,
		WorkspaceDir: st.WorkspaceDir,
		ChangeDir:    changeDir,
		ProjectDir:   st.ProjectDir,
		Spec:         spec,
		Plan:         plan,
		Module:       mod,
		Unit:         u,
		Iteration:    u.Iteration,
	}

	switch u.Status {
	case state.UnitPending, state.UnitReviewFailed, state.UnitTestFailed:
		// dev 轮次
		if u.Status == state.UnitReviewFailed || u.Status == state.UnitTestFailed {
			// 到这一步说明要开始"修复 dev"; 判断循环预算
			if u.Iteration >= maxLoops {
				u.Status = state.UnitFailed
				u.LastError = fmt.Sprintf("unit %s exceeded max loops (%d)", u.ID, maxLoops)
				u.UpdatedAt = time.Now()
				fmt.Fprintf(o.out, "❌ 单元 %s 超过 %d 轮仍未通过, 标记失败\n", u.ID, maxLoops)
				return nil
			}
			in.ReviewFeedback = ""
			in.TestFeedback = ""
			// 把反馈透传给 Dev
			if u.Status == state.UnitReviewFailed {
				in.ReviewFeedback = u.LastFeedback
			}
			if u.Status == state.UnitTestFailed {
				in.TestFeedback = u.LastFeedback
			}
		}
		u.Status = state.UnitDevRunning
		if u.StartedAt.IsZero() {
			u.StartedAt = time.Now()
		}
		u.UpdatedAt = time.Now()
		_ = o.store.Save()
		fmt.Fprintf(o.out, "\n▶ [Dev]    module=%s unit=%s iter=%d/%d\n",
			mod.ID, u.ID, u.Iteration, maxLoops)

		out, err := ua.dev.RunUnit(ctx, in)
		if err != nil {
			u.Status = state.UnitFailed
			u.LastError = err.Error()
			u.UpdatedAt = time.Now()
			return fmt.Errorf("dev unit %s: %w", u.ID, err)
		}
		u.DevFile = out.OutputPath
		u.UpdatedAt = time.Now()
		if !out.Passed {
			// Dev 自判阻塞 -> 当作 review 打回, 回到 dev 队列 (iter+1)
			u.Status = state.UnitReviewFailed
			u.LastFeedback = "Dev 自报 VERDICT: FAIL — " + out.Summary
			u.Iteration++
			fmt.Fprintf(o.out, "⚠️  单元 %s Dev 自报失败, 下轮继续修复\n", u.ID)
			return nil
		}
		u.Status = state.UnitDevDone
		fmt.Fprintf(o.out, "✅ 单元 %s Dev 完成 (adapter=%s)\n", u.ID, out.Adapter)
		return nil

	case state.UnitDevDone:
		u.Status = state.UnitReviewRunning
		u.UpdatedAt = time.Now()
		_ = o.store.Save()
		fmt.Fprintf(o.out, "\n▶ [Review] module=%s unit=%s iter=%d/%d\n",
			mod.ID, u.ID, u.Iteration, maxLoops)

		out, err := ua.review.RunUnit(ctx, in)
		if err != nil {
			u.Status = state.UnitFailed
			u.LastError = err.Error()
			u.UpdatedAt = time.Now()
			return fmt.Errorf("review unit %s: %w", u.ID, err)
		}
		u.ReviewFile = out.OutputPath
		u.UpdatedAt = time.Now()
		if !out.Passed {
			if u.Iteration >= maxLoops {
				u.Status = state.UnitFailed
				u.LastError = fmt.Sprintf("unit %s review failed at final loop", u.ID)
				fmt.Fprintf(o.out, "❌ 单元 %s 最后一轮 review 仍未通过, 标记失败\n", u.ID)
				return nil
			}
			u.Status = state.UnitReviewFailed
			u.LastFeedback = readArtifact(changeDir, u.ID, "review.md")
			u.Iteration++
			fmt.Fprintf(o.out, "🔁 单元 %s Review 打回, 回到 dev (下轮 iter=%d)\n",
				u.ID, u.Iteration)
			return nil
		}
		u.Status = state.UnitReviewPassed
		u.LastFeedback = ""
		fmt.Fprintf(o.out, "✅ 单元 %s Review 通过\n", u.ID)
		return nil

	case state.UnitReviewPassed:
		u.Status = state.UnitTestRunning
		u.UpdatedAt = time.Now()
		_ = o.store.Save()
		fmt.Fprintf(o.out, "\n▶ [Test]   module=%s unit=%s iter=%d/%d\n",
			mod.ID, u.ID, u.Iteration, maxLoops)

		out, err := ua.test.RunUnit(ctx, in)
		if err != nil {
			u.Status = state.UnitFailed
			u.LastError = err.Error()
			u.UpdatedAt = time.Now()
			return fmt.Errorf("test unit %s: %w", u.ID, err)
		}
		u.TestFile = out.OutputPath
		u.UpdatedAt = time.Now()
		if !out.Passed {
			if u.Iteration >= maxLoops {
				u.Status = state.UnitFailed
				u.LastError = fmt.Sprintf("unit %s test failed at final loop", u.ID)
				fmt.Fprintf(o.out, "❌ 单元 %s 最后一轮 test 仍未通过, 标记失败\n", u.ID)
				return nil
			}
			u.Status = state.UnitTestFailed
			u.LastFeedback = readArtifact(changeDir, u.ID, "test.md")
			u.Iteration++
			fmt.Fprintf(o.out, "🔁 单元 %s Test 未通过, 回到 dev (下轮 iter=%d)\n",
				u.ID, u.Iteration)
			return nil
		}
		u.Status = state.UnitDone
		u.LastFeedback = ""
		fmt.Fprintf(o.out, "✅ 单元 %s Test 通过, 标记 DONE\n", u.ID)
		return nil
	}

	return nil
}

func readArtifact(changeDir, unitID, name string) string {
	p := filepath.Join(changeDir, "units", unitID, name)
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(data)
}

func (o *Orchestrator) printModuleOverview(st *state.State) {
	for _, m := range st.Modules {
		fmt.Fprintf(o.out, "  • 模块 %s: %s (%d 单元)\n", m.ID, m.Title, len(m.Units))
		for _, u := range m.Units {
			fmt.Fprintf(o.out, "      - %s: %s  [scope=%s]\n", u.ID, u.Title, u.Scope)
		}
	}
}
