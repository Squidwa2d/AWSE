// Package state 负责 ASWE 的运行时共享状态读写.
//
// 每个 change 拥有独立的 state.json 与 events.jsonl, 存放在
// <workspace>/.aswe/runs/<change-id>/ 目录下.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Stage 表示编排 DAG 的一个节点.
type Stage string

const (
	StageSpec       Stage = "spec"
	StagePlan       Stage = "plan"        // 方案设计 (不写代码)
	StagePlanReview Stage = "plan-review" // 方案评审
	StageDev        Stage = "dev"
	StageReview     Stage = "review" // 代码审查 (code-review)
	StageTest       Stage = "test"
	StageDone       Stage = "done"
	StageFailed     Stage = "failed" // 循环超限, 人工介入
)

// NodeStatus 节点状态.
type NodeStatus string

const (
	StatusPending NodeStatus = "pending"
	StatusRunning NodeStatus = "running"
	StatusPassed  NodeStatus = "passed"
	StatusFailed  NodeStatus = "failed"
)

// NodeResult 单个节点的执行结果快照.
type NodeResult struct {
	Stage     Stage      `json:"stage"`
	Status    NodeStatus `json:"status"`
	Adapter   string     `json:"adapter,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	StartedAt time.Time  `json:"started_at,omitempty"`
	EndedAt   time.Time  `json:"ended_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// State 一次 run 的完整共享状态. 编排器与各 Agent 共同读写.
type State struct {
	ChangeID     string                `json:"change_id"`
	WorkspaceDir string                `json:"workspace_dir"`
	ProjectDir   string                `json:"project_dir,omitempty"` // 代码落盘目录, 通常 <workspace>/projects/<change-id>
	ProposalPath string                `json:"proposal_path,omitempty"`
	SpecPath     string                `json:"spec_path,omitempty"`
	TasksPath    string                `json:"tasks_path,omitempty"`
	CurrentStage Stage                 `json:"current_stage"`
	Nodes        map[Stage]*NodeResult `json:"nodes"`
	RetryCount   int                   `json:"retry_count"`

	// --- 方案阶段 (plan <-> plan-review) 循环 ---
	// PlanIteration 当前是 plan<->plan-review 的第几轮(从 1 起).
	PlanIteration int `json:"plan_iteration,omitempty"`
	// PlanFeedback 上一轮 plan-review 的改进意见, 下一轮 Plan-Agent 必须参考.
	PlanFeedback string `json:"plan_feedback,omitempty"`

	// --- 代码阶段 (dev <-> review <-> test) 循环 ---
	// CodeIteration dev<->review/test 的第几轮(从 1 起). 仅在**未启用模块化流水线**
	// (即 len(Modules)==0) 时使用, 属于旧版"整仓一把过"的回退路径.
	CodeIteration int `json:"code_iteration,omitempty"`
	// ReviewFeedback 上一轮 code-review 的意见.
	ReviewFeedback string `json:"review_feedback,omitempty"`
	// TestFeedback 上一轮 test 的失败信息.
	TestFeedback string `json:"test_feedback,omitempty"`

	// --- 模块化流水线 (Plan-Agent 拆分后填充) ---
	// Modules 为空时使用旧版线性流水线; 非空时 orchestrator 切换到模块-单元调度.
	Modules []*Module `json:"modules,omitempty"`
	// MaxUnitLoops 每个单元允许的 dev↔review/test 最大循环数; 0 表示使用默认 8.
	MaxUnitLoops int `json:"max_unit_loops,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// New 创建一个全新的 State.
func New(changeID, workspaceDir string) *State {
	return &State{
		ChangeID:     changeID,
		WorkspaceDir: workspaceDir,
		CurrentStage: StageSpec,
		Nodes:        map[Stage]*NodeResult{},
		UpdatedAt:    time.Now(),
	}
}

// Store 负责把 State 落盘并追加事件.
type Store struct {
	mu         sync.Mutex
	stateFile  string
	eventsFile string
	state      *State
}

// Open 打开(或创建) 某个 change 的 store.
func Open(workspaceDir, changeID string) (*Store, error) {
	runDir := filepath.Join(workspaceDir, ".aswe", "runs", changeID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		stateFile:  filepath.Join(runDir, "state.json"),
		eventsFile: filepath.Join(runDir, "events.jsonl"),
	}
	if data, err := os.ReadFile(s.stateFile); err == nil {
		var st State
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("parse state.json: %w", err)
		}
		s.state = &st
	} else if os.IsNotExist(err) {
		s.state = New(changeID, workspaceDir)
	} else {
		return nil, err
	}
	return s, nil
}

// State 当前状态快照(返回指针供上层修改; 修改完要调 Save).
func (s *Store) State() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Save 持久化当前 state 到磁盘.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.stateFile, data, 0o644)
}

// Event 运行时事件, 追加写入 events.jsonl.
type Event struct {
	Time    time.Time `json:"time"`
	Stage   Stage     `json:"stage,omitempty"`
	Type    string    `json:"type"`
	Message string    `json:"message,omitempty"`
}

// Emit 追加一条事件.
func (s *Store) Emit(evt Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if evt.Time.IsZero() {
		evt.Time = time.Now()
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.eventsFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}
