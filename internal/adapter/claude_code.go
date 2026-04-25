package adapter

import (
	"context"
	"fmt"
	"strings"
)

// ClaudeCodeAdapter 适配 Anthropic 官方 claude CLI (Claude Code).
// 典型非交互调用: claude -p "PROMPT" --output-format text
type ClaudeCodeAdapter struct {
	Binary string
	Model  string
}

func NewClaudeCodeAdapter(model string) *ClaudeCodeAdapter {
	return &ClaudeCodeAdapter{Binary: "claude", Model: model}
}

func (a *ClaudeCodeAdapter) Name() string { return "claude-code" }

func (a *ClaudeCodeAdapter) IsAvailable() bool { return commandExists(a.Binary) }

func (a *ClaudeCodeAdapter) Invoke(ctx context.Context, req Request) (*Response, error) {
	if !a.IsAvailable() {
		return nil, &ErrNotAvailable{Adapter: a.Name()}
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
	args = append(args, req.Prompt)

	stdout, stderr, exit, err := runCommand(ctx, req.WorkDir, req.TimeoutSeconds, "", a.Binary, args...)
	if err != nil && exit == 0 {
		return nil, fmt.Errorf("claude invoke failed: %w (stderr=%s)", err, stderr)
	}
	combined := stdout + "\n" + stderr
	if msg := detectAuthError(combined); msg != "" {
		return nil, fmt.Errorf("claude 未登录或鉴权失败: %s\n请先运行 `claude` 完成登录", msg)
	}
	cleaned := cleanOutput(stdout)
	if cleaned == "" {
		return nil, fmt.Errorf("claude 返回空输出 (exit=%d, stderr=%q)", exit, strings.TrimSpace(stderr))
	}
	return &Response{
		Output:    cleaned,
		ExitCode:  exit,
		RawStdout: stdout,
		RawStderr: stderr,
		Adapter:   a.Name(),
	}, nil
}
