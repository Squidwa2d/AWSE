// root.go — cobra 根命令入口, 为所有子命令提供全局 flag 和上下文.
package main

import (
	"context"

	"github.com/spf13/cobra"
)

// globalFlags 全局 flag, 所有子命令可见.
type globalFlags struct {
	cfgPath   string // --config
	workspace string // --workspace, 预留, 可用来显式指定 workspace 根
}

var gflags = &globalFlags{}

// rootCmd 应用根命令.
var rootCmd = &cobra.Command{
	Use:   "aswe",
	Short: "ASWE — 多智能体研发协作 CLI",
	Long: `ASWE — 多智能体研发协作 CLI

编排流程:
  Stage 1  spec          基于 proposal 产出需求规格
  Stage 2  plan          产出技术方案 (不写代码)
  Stage 3  plan-review   严格评审方案, 不通过则回到 plan (上限 8 轮)
  Stage 4  dev           批准后真实创建/修改代码 (模块化流水线)
  Stage 5  review        代码审查, 不通过回到 dev
  Stage 6  test          真实执行测试, 不通过回到 dev
`,
	SilenceUsage:  true, // 子命令返回 error 时不打印整段 usage
	SilenceErrors: true, // 交由 main 统一打印错误
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&gflags.cfgPath, "config", "", "指定配置文件路径 (默认 <workspace>/.aswe/config.yaml)")
	pf.StringVar(&gflags.workspace, "workspace", "", "显式指定 workspace 根目录 (默认自动探测)")

	// 注册所有子命令.
	rootCmd.AddCommand(newNewCmd())
	rootCmd.AddCommand(newRunCmd()) // 也会注册 alias "resume"
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newDoctorCmd())
	rootCmd.AddCommand(newVersionCmd())
}

// cmdCtx 从 cobra 上下文中取出 context.
func cmdCtx(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
