package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AutomationMode 控制编排器是否需要用户确认.
type AutomationMode string

const (
	ModeAuto        AutomationMode = "auto"
	ModeInteractive AutomationMode = "interactive"
	ModeStep        AutomationMode = "step"
)

// AgentConfig 单个 Agent 的适配器配置.
type AgentConfig struct {
	Adapter  string   `yaml:"adapter"`
	Fallback []string `yaml:"fallback"`
	Model    string   `yaml:"model"`
}

// PMConfig PM-Agent 配置.
type PMConfig struct {
	Adapter  string   `yaml:"adapter"`
	Fallback []string `yaml:"fallback"`
	Model    string   `yaml:"model"`
	MaxTurns int      `yaml:"max_turns"`
	// MinTurns PM-Agent 在进入 proposal 生成前**至少**要完成的澄清轮数,
	// 即便模型认为已经 READY, 只要没到这个数, 仍然会强制再追问. 默认 3.
	MinTurns int `yaml:"min_turns"`
}

// GenericConfig generic 适配器的命令模板.
type GenericConfig struct {
	Command string `yaml:"command"`
}

// Config 全局配置.
type Config struct {
	AutomationMode AutomationMode         `yaml:"automation_mode"`
	PMAgent        PMConfig               `yaml:"pm_agent"`
	Agents         map[string]AgentConfig `yaml:"agents"`
	Generic        GenericConfig          `yaml:"generic"`
	OpenSpecDir    string                 `yaml:"openspec_dir"`
	WorkspaceRoot  string                 `yaml:"workspace_root"`
	// MaxPlanLoops plan<->plan-review 的自动循环上限, 0 使用默认 8.
	MaxPlanLoops int `yaml:"max_plan_loops"`
	// MinPlanLoops plan<->plan-review 至少要完成的循环数(从 1 起).
	// 即便第一轮 Plan-Review 判 PASS, 未达此数也会被强制降级为 FAIL 再跑一轮,
	// 防止 AI "一眼通过" 草率放行. 0 使用默认 2.
	MinPlanLoops int `yaml:"min_plan_loops"`
	// MaxCodeLoops dev<->review<->test 共享的自动循环上限, 0 使用默认 8.
	MaxCodeLoops int `yaml:"max_code_loops"`
}

// Default 返回默认配置.
func Default() *Config {
	return &Config{
		AutomationMode: ModeInteractive,
		PMAgent: PMConfig{
			Adapter:  "codebuddy",
			Fallback: []string{"claude-code", "codex"},
			MaxTurns: 8,
			MinTurns: 3,
		},
		Agents: map[string]AgentConfig{
			"spec":        {Adapter: "codebuddy", Fallback: []string{"claude-code", "codex"}},
			"plan":        {Adapter: "codebuddy", Fallback: []string{"claude-code", "codex"}},
			"plan-review": {Adapter: "codebuddy", Fallback: []string{"claude-code", "codex"}},
			"dev":         {Adapter: "codebuddy", Fallback: []string{"claude-code", "codex"}},
			"review":      {Adapter: "codebuddy", Fallback: []string{"claude-code", "codex"}},
			"test":        {Adapter: "codebuddy", Fallback: []string{"claude-code", "codex"}},
		},
		OpenSpecDir:  "openspec",
		MaxPlanLoops: 8,
		MinPlanLoops: 2,
		MaxCodeLoops: 8,
	}
}

// Load 从给定路径读取配置, 未找到则返回默认值并以默认值创建文件.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.AutomationMode == "" {
		cfg.AutomationMode = ModeInteractive
	}
	if cfg.PMAgent.MaxTurns <= 0 {
		cfg.PMAgent.MaxTurns = 8
	}
	if cfg.PMAgent.MinTurns <= 0 {
		cfg.PMAgent.MinTurns = 3
	}
	if cfg.PMAgent.MinTurns > cfg.PMAgent.MaxTurns {
		cfg.PMAgent.MinTurns = cfg.PMAgent.MaxTurns
	}
	if cfg.OpenSpecDir == "" {
		cfg.OpenSpecDir = "openspec"
	}
	if cfg.MaxPlanLoops <= 0 {
		cfg.MaxPlanLoops = 8
	}
	if cfg.MinPlanLoops <= 0 {
		cfg.MinPlanLoops = 2
	}
	if cfg.MinPlanLoops > cfg.MaxPlanLoops {
		cfg.MinPlanLoops = cfg.MaxPlanLoops
	}
	if cfg.MaxCodeLoops <= 0 {
		cfg.MaxCodeLoops = 8
	}
	return cfg, nil
}

// ResolveWorkspace 确定 workspace 根目录.
// 优先顺序: 显式配置 > 当前目录及其祖先中包含 openspec/ 的目录 > 当前目录.
func (c *Config) ResolveWorkspace(startDir string) (string, error) {
	if c.WorkspaceRoot != "" {
		abs, err := filepath.Abs(c.WorkspaceRoot)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		if st, err := os.Stat(filepath.Join(dir, c.OpenSpecDir)); err == nil && st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return startDir, nil
		}
		dir = parent
	}
}
