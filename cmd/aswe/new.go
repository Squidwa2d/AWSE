package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aswe/aswe/internal/adapter"
	"github.com/aswe/aswe/internal/config"
	"github.com/aswe/aswe/internal/pm"
	"github.com/spf13/cobra"
)

// newNewCmd 构造 "aswe new <需求描述>" 子命令.
func newNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <需求描述>",
		Short: "启动 PM-Agent, 产出 OpenSpec proposal",
		Long: `启动 PM-Agent, 基于自然语言需求进行多轮对齐, 产出 proposal.md.

示例:
  aswe new "我想做一个支持团队共享的待办小程序"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNew(cmd, args[0])
		},
	}
	return cmd
}

func runNew(cmd *cobra.Command, userReq string) error {
	ctx := cmdCtx(cmd)
	cwd, _ := os.Getwd()
	cfg, workspace, resolvedCfgPath, err := loadConfigAndWorkspace(gflags.cfgPath, cwd)
	if err != nil {
		return err
	}
	fmt.Printf("📂 workspace = %s\n", workspace)
	fmt.Printf("📑 config    = %s\n", resolvedCfgPath)

	factory := &adapter.Factory{GenericCommand: cfg.Generic.Command}
	cli, err := factory.Resolve(cfg.PMAgent.Adapter, cfg.PMAgent.Fallback, cfg.PMAgent.Model)
	if err != nil {
		return fmt.Errorf("resolve PM adapter: %w", err)
	}

	agent := pm.New(cli, workspace, cfg.OpenSpecDir, cfg.PMAgent.MaxTurns,
		pm.WithMinTurns(cfg.PMAgent.MinTurns),
	)
	p, err := agent.Run(ctx, userReq)
	if err != nil {
		return err
	}

	fmt.Printf("\n下一步: 运行 `aswe run %s` 启动全流程编排\n", p.ChangeID)
	return nil
}

// loadConfigAndWorkspace 统一处理配置加载 + workspace 定位.
// 若全局 --workspace 被显式传入, 优先使用它.
func loadConfigAndWorkspace(cfgPathArg, cwd string) (*config.Config, string, string, error) {
	if gflags.workspace != "" {
		cwd = gflags.workspace
	}
	cfgPath := cfgPathArg
	if cfgPath == "" {
		tmp := config.Default()
		wsGuess, _ := tmp.ResolveWorkspace(cwd)
		cfgPath = filepath.Join(wsGuess, ".aswe", "config.yaml")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, "", "", err
	}
	workspace, err := cfg.ResolveWorkspace(cwd)
	if err != nil {
		return nil, "", "", err
	}
	return cfg, workspace, cfgPath, nil
}
