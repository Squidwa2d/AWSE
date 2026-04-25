package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// runCommand 启动一个子进程, 把 prompt 通过 stdin 送入, 返回其 stdout/stderr.
// 这是所有 adapter 的公共工具函数.
func runCommand(ctx context.Context, cwd string, timeoutSec int, prompt string, name string, args ...string) (stdout, stderr string, exitCode int, err error) {
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
	if prompt != "" {
		stdin, pipeErr := cmd.StdinPipe()
		if pipeErr != nil {
			return "", "", -1, fmt.Errorf("create stdin pipe: %w", pipeErr)
		}
		go func() {
			defer stdin.Close()
			_, _ = io.WriteString(stdin, prompt)
		}()
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

// cleanOutput 去掉 ANSI 控制序列与两端空白.
func cleanOutput(s string) string {
	s = stripANSI(s)
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
