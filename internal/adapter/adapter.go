// Package adapter 提供统一的 CLI 适配层, 把 claude-code / codebuddy / codex /
// generic 等不同的终端 AI 工具抽象成一致的 Invoke 接口, 便于上层编排器以同
// 样的方式调用任何受支持的 AI 工具.
package adapter

import (
	"context"
	"fmt"
)

// InvokeMode 描述这次调用期望的交互模式.
type InvokeMode string

const (
	// ModeChat 单轮问答, 通常用于 PM-Agent 的澄清追问.
	ModeChat InvokeMode = "chat"
	// ModeHeadless 无人值守执行一段任务, 可能会读写文件.
	ModeHeadless InvokeMode = "headless"
)

// Request 给适配器的一次调用请求.
type Request struct {
	// Prompt 系统/用户提示词, 会通过命令行或 stdin 送入目标 CLI.
	Prompt string
	// WorkDir 子进程工作目录. 空则继承当前进程.
	WorkDir string
	// Mode 交互模式.
	Mode InvokeMode
	// Model 显式指定模型. 空则使用适配器默认.
	Model string
	// ExtraArgs 允许上层附加任意特定参数(保留扩展).
	ExtraArgs []string
	// Timeout 秒. 0 表示不限时.
	TimeoutSeconds int
}

// Response 适配器返回的结果.
type Response struct {
	// Output 主要的文本输出(已去除首尾空白).
	Output string
	// ExitCode 子进程退出码.
	ExitCode int
	// RawStdout / RawStderr 原始输出, 用于调试.
	RawStdout string
	RawStderr string
	// Adapter 本次实际使用的适配器名称(可能是 fallback).
	Adapter string
}

// CLIAdapter 适配器通用接口.
type CLIAdapter interface {
	// Name 适配器名称, 如 "claude-code".
	Name() string
	// IsAvailable 当前环境是否安装了对应的 CLI.
	IsAvailable() bool
	// Invoke 执行一次调用.
	Invoke(ctx context.Context, req Request) (*Response, error)
}

// ErrNotAvailable 表示指定适配器在当前环境不可用.
type ErrNotAvailable struct {
	Adapter string
}

func (e *ErrNotAvailable) Error() string {
	return fmt.Sprintf("cli adapter %q is not available on PATH", e.Adapter)
}
