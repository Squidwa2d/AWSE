package adapter

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestRunCommand_StdinPipesPayload 校验 runCommand 把 stdinPayload 注入到子进程 stdin.
// 通过系统自带的 cat 把 stdin 透传到 stdout 来验证.
func TestRunCommand_StdinPipesPayload(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("环境里没有 cat, 跳过")
	}
	want := strings.Repeat("hello-aswe-stdin\n", 100)
	stdout, _, exit, err := runCommand(context.Background(), "", 5, want, "cat")
	if err != nil || exit != 0 {
		t.Fatalf("runCommand 失败: err=%v exit=%d", err, exit)
	}
	if stdout != want {
		t.Fatalf("stdout 与 stdin 不一致: got len=%d want len=%d", len(stdout), len(want))
	}
}

// TestRunCommand_NoStdinDoesNotHang 不传 stdinPayload 时, 即便子进程读 stdin 也应立刻 EOF, 不挂起.
func TestRunCommand_NoStdinDoesNotHang(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("环境里没有 cat, 跳过")
	}
	stdout, _, _, err := runCommand(context.Background(), "", 5, "", "cat")
	if err != nil {
		t.Fatalf("runCommand 不应该失败: %v", err)
	}
	if stdout != "" {
		t.Fatalf("空 stdin 时 cat 应输出空, got %q", stdout)
	}
}
