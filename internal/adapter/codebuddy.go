package adapter

import (
	"context"
	"fmt"
	"strings"
)

// CodeBuddyAdapter 适配 codebuddy (同时兼容其别名 cbc) CLI.
// 调用格式: codebuddy -p --output-format text --dangerously-skip-permissions "PROMPT"
// 参考其 --help 输出.
type CodeBuddyAdapter struct {
	Binary string // 默认 "codebuddy"
	Model  string
}

func NewCodeBuddyAdapter(model string) *CodeBuddyAdapter {
	return &CodeBuddyAdapter{Binary: "codebuddy", Model: model}
}

func (a *CodeBuddyAdapter) Name() string { return "codebuddy" }

func (a *CodeBuddyAdapter) IsAvailable() bool {
	return commandExists(a.Binary) || commandExists("cbc")
}

func (a *CodeBuddyAdapter) Invoke(ctx context.Context, req Request) (*Response, error) {
	if !a.IsAvailable() {
		return nil, &ErrNotAvailable{Adapter: a.Name()}
	}
	bin := a.Binary
	if !commandExists(bin) {
		bin = "cbc"
	}
	args := []string{"-p", "--output-format", "text", "--dangerously-skip-permissions"}
	model := req.Model
	if model == "" {
		model = a.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if len(req.ExtraArgs) > 0 {
		args = append(args, req.ExtraArgs...)
	}
	// prompt 通过位置参数传入(其 CLI 约定): codebuddy ... "PROMPT"
	args = append(args, req.Prompt)

	stdout, stderr, exit, err := runCommand(ctx, req.WorkDir, req.TimeoutSeconds, "", bin, args...)
	if err != nil && exit == 0 {
		return nil, fmt.Errorf("codebuddy invoke failed: %w (stderr=%s)", err, stderr)
	}

	combined := stdout + "\n" + stderr
	if msg := detectAuthError(combined); msg != "" {
		return nil, fmt.Errorf("codebuddy 未登录或鉴权失败: %s\n请先在终端运行 `codebuddy` 并使用 /login 完成登录, 然后再试", msg)
	}

	cleaned := cleanOutput(stdout)
	if cleaned == "" {
		return nil, fmt.Errorf("codebuddy 返回空输出 (exit=%d, stderr=%q). 可能是鉴权失败、网络问题或 prompt 被拒绝", exit, strings.TrimSpace(stderr))
	}
	return &Response{
		Output:    cleaned,
		ExitCode:  exit,
		RawStdout: stdout,
		RawStderr: stderr,
		Adapter:   a.Name(),
	}, nil
}

// detectAuthError 命中常见鉴权错误关键词时返回原文, 否则返回空串.
func detectAuthError(s string) string {
	low := strings.ToLower(s)
	markers := []string{
		"authentication required",
		"please use /login",
		"not logged in",
		"unauthorized",
		"401 unauthorized",
		"please sign in",
		"api key",
		"login first",
	}
	for _, m := range markers {
		if strings.Contains(low, m) {
			// 回传原句(去掉前后空白, 截断到一行)
			for _, line := range strings.Split(s, "\n") {
				if strings.Contains(strings.ToLower(line), m) {
					return strings.TrimSpace(line)
				}
			}
			return strings.TrimSpace(s)
		}
	}
	return ""
}
