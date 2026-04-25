package adapter

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// GenericAdapter 兜底适配器, 根据用户提供的命令模板调用任意 CLI.
// 模板占位符:
//
//	{{PROMPT_FILE}}  一次性写入的 prompt 文件路径
//	{{WORK_DIR}}     工作目录
type GenericAdapter struct {
	CommandTemplate string
}

func NewGenericAdapter(tpl string) *GenericAdapter {
	return &GenericAdapter{CommandTemplate: tpl}
}

func (a *GenericAdapter) Name() string { return "generic" }

func (a *GenericAdapter) IsAvailable() bool {
	tpl := strings.TrimSpace(a.CommandTemplate)
	if tpl == "" {
		return false
	}
	head := strings.Fields(tpl)
	if len(head) == 0 {
		return false
	}
	return commandExists(head[0])
}

func (a *GenericAdapter) Invoke(ctx context.Context, req Request) (*Response, error) {
	if !a.IsAvailable() {
		return nil, &ErrNotAvailable{Adapter: a.Name()}
	}
	// 把 prompt 落到临时文件, 避免过长 argv.
	tmp, err := os.CreateTemp("", "aswe-prompt-*.txt")
	if err != nil {
		return nil, fmt.Errorf("create prompt tmp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(req.Prompt); err != nil {
		return nil, err
	}
	tmp.Close()

	cmd := a.CommandTemplate
	cmd = strings.ReplaceAll(cmd, "{{PROMPT_FILE}}", tmp.Name())
	cmd = strings.ReplaceAll(cmd, "{{WORK_DIR}}", req.WorkDir)

	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty generic command")
	}
	name := fields[0]
	args := fields[1:]

	stdout, stderr, exit, err := runCommand(ctx, req.WorkDir, req.TimeoutSeconds, "", name, args...)
	if err != nil && exit == 0 {
		return nil, fmt.Errorf("generic invoke failed: %w (stderr=%s)", err, stderr)
	}
	return &Response{
		Output:    cleanOutput(stdout),
		ExitCode:  exit,
		RawStdout: stdout,
		RawStderr: stderr,
		Adapter:   a.Name(),
	}, nil
}
