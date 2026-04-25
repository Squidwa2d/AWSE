package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aswe/aswe/internal/adapter"
	"github.com/aswe/aswe/internal/agents"
	"github.com/aswe/aswe/internal/config"
	"github.com/aswe/aswe/internal/orchestrator"
	"github.com/aswe/aswe/internal/state"
	"github.com/spf13/cobra"
)

// runFlags 专属于 run/resume 子命令的 flag.
type runFlags struct {
	mode string // --mode
}

// newRunCmd 构造 "aswe run <change-id>" 子命令, 同时注册 "resume" 作为别名.
func newRunCmd() *cobra.Command {
	rf := &runFlags{}
	cmd := &cobra.Command{
		Use:     "run <change-id>",
		Aliases: []string{"resume"},
		Short:   "运行/续跑编排: spec → plan ⇄ plan-review → dev (模块化) → review ⇄ test",
		Long: `根据 change-id 从当前 stage 开始执行全流程编排, 支持断点续跑.

示例:
  aswe run todo-app-0425-120000
  aswe run todo-app-0425-120000 --mode auto
  aswe resume todo-app-0425-120000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd, args[0], rf)
		},
	}
	cmd.Flags().StringVar(&rf.mode, "mode", "",
		"覆盖自动化模式: auto / interactive / step (默认读 config.yaml)")
	return cmd
}

func runRun(cmd *cobra.Command, changeID string, rf *runFlags) error {
	ctx := cmdCtx(cmd)

	cwd, _ := os.Getwd()
	cfg, workspace, _, err := loadConfigAndWorkspace(gflags.cfgPath, cwd)
	if err != nil {
		return err
	}
	if rf.mode != "" {
		cfg.AutomationMode = config.AutomationMode(rf.mode)
	}

	changeDir := filepath.Join(workspace, cfg.OpenSpecDir, "changes", changeID)
	proposalPath := filepath.Join(changeDir, "proposal.md")
	if _, err := os.Stat(proposalPath); err != nil {
		return fmt.Errorf("proposal.md 不存在 (%s). 请先 `aswe new` 创建 change", proposalPath)
	}

	projectDir := filepath.Join(workspace, "projects", changeID)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return fmt.Errorf("创建 project dir 失败: %w", err)
	}

	factory := &adapter.Factory{GenericCommand: cfg.Generic.Command}
	nodes := map[state.Stage]agents.Agent{}
	stageToCfg := map[state.Stage]string{
		state.StageSpec:       "spec",
		state.StagePlan:       "plan",
		state.StagePlanReview: "plan-review",
		state.StageDev:        "dev",
		state.StageReview:     "review",
		state.StageTest:       "test",
	}
	for stage, cfgKey := range stageToCfg {
		agCfg, ok := cfg.Agents[cfgKey]
		if !ok || agCfg.Adapter == "" {
			if stage == state.StagePlan || stage == state.StagePlanReview {
				if r, ok := cfg.Agents["review"]; ok && r.Adapter != "" {
					agCfg = r
				}
			}
			if agCfg.Adapter == "" {
				agCfg = config.AgentConfig{Adapter: cfg.PMAgent.Adapter, Fallback: cfg.PMAgent.Fallback}
			}
		}
		cli, err := factory.Resolve(agCfg.Adapter, agCfg.Fallback, agCfg.Model)
		if err != nil {
			return fmt.Errorf("resolve adapter for %s: %w", stage, err)
		}
		switch stage {
		case state.StageSpec:
			nodes[stage] = agents.NewSpec(cli)
		case state.StagePlan:
			nodes[stage] = agents.NewPlan(cli)
		case state.StagePlanReview:
			nodes[stage] = agents.NewPlanReview(cli)
		case state.StageDev:
			nodes[stage] = agents.NewDev(cli)
		case state.StageReview:
			nodes[stage] = agents.NewReview(cli)
		case state.StageTest:
			nodes[stage] = agents.NewTest(cli)
		}
	}

	store, err := state.Open(workspace, changeID)
	if err != nil {
		return err
	}
	st := store.State()
	if st.ChangeID == "" {
		st.ChangeID = changeID
		st.WorkspaceDir = workspace
		st.CurrentStage = state.StageSpec
	}
	st.ProjectDir = projectDir
	_ = store.Save()

	fmt.Printf("📂 workspace    = %s\n", workspace)
	fmt.Printf("📦 change       = %s\n", changeID)
	fmt.Printf("🧱 project dir  = %s\n", projectDir)
	fmt.Printf("⚙️  mode         = %s\n", cfg.AutomationMode)
	fmt.Printf("▶️  current stage = %s\n", st.CurrentStage)

	resolveForStage := func(stage state.Stage) (adapter.CLIAdapter, error) {
		cfgKey := stageToCfg[stage]
		agCfg := cfg.Agents[cfgKey]
		if agCfg.Adapter == "" {
			agCfg = config.AgentConfig{Adapter: cfg.PMAgent.Adapter, Fallback: cfg.PMAgent.Fallback}
		}
		return factory.Resolve(agCfg.Adapter, agCfg.Fallback, agCfg.Model)
	}
	devCLI, err := resolveForStage(state.StageDev)
	if err != nil {
		return fmt.Errorf("resolve dev unit adapter: %w", err)
	}
	reviewCLI, err := resolveForStage(state.StageReview)
	if err != nil {
		return fmt.Errorf("resolve review unit adapter: %w", err)
	}
	testCLI, err := resolveForStage(state.StageTest)
	if err != nil {
		return fmt.Errorf("resolve test unit adapter: %w", err)
	}

	orc := orchestrator.New(orchestrator.Options{
		Store:        store,
		Nodes:        nodes,
		Mode:         cfg.AutomationMode,
		ProjectDir:   projectDir,
		MaxPlanLoops: cfg.MaxPlanLoops,
		MaxCodeLoops: cfg.MaxCodeLoops,
		DevUnit:      agents.NewDevUnit(devCLI),
		ReviewUnit:   agents.NewReviewUnit(reviewCLI),
		TestUnit:     agents.NewTestUnit(testCLI),
	})
	return orc.Run(ctx, changeDir, proposalPath)
}
