package adapter

import (
	"context"
	"fmt"
	"strings"
)

// CodexAdapter 适配 OpenAI Codex CLI.
// 典型非交互调用: codex exec "PROMPT"
type CodexAdapter struct {
	Binary string
	Model  string
}

func NewCodexAdapter(model string) *CodexAdapter {
	return &CodexAdapter{Binary: "codex", Model: model}
}

func (a *CodexAdapter) Name() string { return "codex" }

func (a *CodexAdapter) IsAvailable() bool { return commandExists(a.Binary) }

func (a *CodexAdapter) Invoke(ctx context.Context, req Request) (*Response, error) {
	if !a.IsAvailable() {
		return nil, &ErrNotAvailable{Adapter: a.Name()}
	}
	args := []string{"exec", "--skip-git-repo-check"}
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
	// 大 prompt 切 stdin, 防止超 ARG_MAX.
	stdinPayload := ""
	if len(req.Prompt) > maxArgvPromptBytes {
		stdinPayload = req.Prompt
		// codex exec 需要至少一个 prompt 占位, 不带任何位置参数会进交互模式;
		// 用 "-" 约定 "从 stdin 读" 是大多数 *nix 工具的通用语义, codex 也兼容.
		args = append(args, "-")
	} else {
		args = append(args, req.Prompt)
	}

	stdout, stderr, exit, err := runCommand(ctx, req.WorkDir, req.TimeoutSeconds, stdinPayload, a.Binary, args...)
	if err != nil && exit == 0 {
		return nil, fmt.Errorf("codex invoke failed: %w (stderr=%s)", err, stderr)
	}
	combined := stdout + "\n" + stderr
	if msg := detectAuthError(combined); msg != "" {
		return nil, fmt.Errorf("codex 未登录或鉴权失败: %s\n请先运行 `codex login` 完成登录", msg)
	}
	cleaned := cleanOutput(stdout)
	if cleaned == "" {
		return nil, fmt.Errorf("codex 返回空输出 (exit=%d, stderr=%q)", exit, strings.TrimSpace(stderr))
	}
	return &Response{
		Output:    cleaned,
		ExitCode:  exit,
		RawStdout: stdout,
		RawStderr: stderr,
		Adapter:   a.Name(),
	}, nil
}
