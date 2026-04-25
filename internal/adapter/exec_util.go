package adapter

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// maxArgvPromptBytes 单个 argv 参数的安全上限.
// Linux ARG_MAX 默认 ~2MB, macOS 256KB; 留出环境变量 + 其它 args 的余量,
// 超过此阈值的 prompt 会被各 adapter 自动切到 stdin 注入, 避免 "argument list too long".
const maxArgvPromptBytes = 64 * 1024

// runCommand 启动一个子进程, 可选地把 stdinPayload 通过 stdin 送入, 返回其 stdout/stderr.
//   - stdinPayload != "" : 用 strings.Reader 作为 cmd.Stdin (无 goroutine, 子进程退出后无资源残留).
//   - stdinPayload == "" : 不设 Stdin (子进程读 stdin 会立刻 EOF).
//
// 这是所有 adapter 的公共工具函数.
func runCommand(ctx context.Context, cwd string, timeoutSec int, stdinPayload string, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if stdinPayload != "" {
		// 直接用 strings.Reader 作为 Stdin, 由 exec 包自己负责 io.Copy + 关闭 pipe;
		// 不再手动起 goroutine, 避免子进程提前退出后写端泄漏.
		cmd.Stdin = strings.NewReader(stdinPayload)
	}
	err = cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode, err
}

// commandExists 检测 PATH 里是否存在某个命令.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// cleanOutput 去掉 ANSI 控制序列与两端空白; 同时把 \r\n / 单独的 \r 归一成 \n,
// 避免后续 markdown 解析(围栏匹配/正则)被 CRLF 干扰.
func cleanOutput(s string) string {
	s = stripANSI(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// stripANSI 简单剥离 ESC[...m 等 ANSI 转义序列.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// 跳到字母结束
			j := i + 2
			for j < len(s) && !isANSIEnd(s[j]) {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
			break
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func isANSIEnd(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
