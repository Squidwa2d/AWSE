// aswe — 多智能体研发协作 CLI (cobra 入口).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// 优雅处理用户中断, 不打印堆栈
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "⚠️  已取消")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}
