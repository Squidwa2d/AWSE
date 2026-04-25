package adapter

import (
	"fmt"
	"strings"
)

// Factory 根据名称 + 配置参数创建一个具体适配器.
type Factory struct {
	GenericCommand string // 从全局 config.yaml 传入
}

// Build 根据指定名称创建适配器. 对 generic 额外需要命令模板.
func (f *Factory) Build(name, model string) (CLIAdapter, error) {
	switch strings.ToLower(name) {
	case "claude", "claude-code":
		return NewClaudeCodeAdapter(model), nil
	case "codebuddy", "cbc":
		return NewCodeBuddyAdapter(model), nil
	case "codex":
		return NewCodexAdapter(model), nil
	case "generic":
		if strings.TrimSpace(f.GenericCommand) == "" {
			return nil, fmt.Errorf("adapter=generic 需要在 config.yaml 配置 generic_command (如 \"mycli -p {prompt}\")")
		}
		return NewGenericAdapter(f.GenericCommand), nil
	default:
		return nil, fmt.Errorf("unknown adapter %q (支持: claude/codebuddy/codex/generic)", name)
	}
}

// Resolve 按首选 + fallback 顺序选出第一个可用的适配器.
func (f *Factory) Resolve(primary string, fallback []string, model string) (CLIAdapter, error) {
	candidates := append([]string{primary}, fallback...)
	var firstErr error
	var tried []string
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		tried = append(tried, name)
		a, err := f.Build(name, model)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if a.IsAvailable() {
			return a, nil
		}
	}
	// 所有候选都已 Build 成功但全部 IsAvailable=false → 给出最有用的人话.
	if len(tried) > 0 {
		return nil, fmt.Errorf("没有可用的适配器 (尝试过: %s); 请确认对应 CLI 已 PATH 安装, 或在 config.yaml 改 default_adapter / fallback_adapters",
			strings.Join(tried, ", "))
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("没有候选适配器: 请在 config.yaml 配置 default_adapter")
}
