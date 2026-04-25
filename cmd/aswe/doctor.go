package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aswe/aswe/internal/adapter"
	"github.com/spf13/cobra"
)

// newDoctorCmd 构造 "aswe doctor" 子命令.
func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "检测所有 CLI 适配器是否可用 (含鉴权冒烟)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd)
		},
	}
	return cmd
}

func runDoctor(cmd *cobra.Command) error {
	ctx := cmdCtx(cmd)
	cwd, _ := os.Getwd()
	cfg, workspace, resolvedCfg, err := loadConfigAndWorkspace(gflags.cfgPath, cwd)
	if err != nil {
		return err
	}
	fmt.Printf("📂 workspace = %s\n", workspace)
	fmt.Printf("📑 config    = %s\n\n", resolvedCfg)

	factory := &adapter.Factory{GenericCommand: cfg.Generic.Command}
	names := []string{"codebuddy", "claude-code", "codex"}
	if cfg.Generic.Command != "" {
		names = append(names, "generic")
	}

	allOK := true
	for _, name := range names {
		ad, err := factory.Build(name, "")
		if err != nil {
			fmt.Printf("❌ %-12s build: %v\n", name, err)
			allOK = false
			continue
		}
		if !ad.IsAvailable() {
			fmt.Printf("⚠️  %-12s 未安装\n", name)
			continue
		}
		fmt.Printf("🔧 %-12s 已安装, 执行冒烟 ...\n", name)
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		resp, err := ad.Invoke(tctx, adapter.Request{
			Prompt:         "用一句话介绍你自己",
			WorkDir:        workspace,
			Mode:           adapter.ModeChat,
			TimeoutSeconds: 90,
		})
		cancel()
		if err != nil {
			fmt.Printf("   ❌ %v\n", err)
			allOK = false
			continue
		}
		snippet := resp.Output
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		fmt.Printf("   ✅ 回复: %s\n", snippet)
	}
	fmt.Println()
	if !allOK {
		return fmt.Errorf("部分适配器未就绪, 请先解决上面的错误")
	}
	fmt.Println("🎉 所有适配器就绪")
	return nil
}
