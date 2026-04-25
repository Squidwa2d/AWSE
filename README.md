# ASWE — 多智能体研发协作 CLI

`aswe` 是一个纯终端的多智能体研发协作工具: 用户只负责说需求, 后台由 6 个角色化 Agent (Spec / Plan / Plan-Review / Dev / Review / Test) 通过子进程方式调用本地的 AI CLI (CodeBuddy / Claude Code / Codex / 自定义) 自动推进研发流程, 并把每一步的产物沉淀到 OpenSpec 目录.

核心特性:
- **PM-Agent 多轮澄清**: 至少 3 轮追问, 把模糊需求落成 `proposal.md`.
- **方案先行 + 双重兵底**: `plan → plan-review` 独立循环至少 2 轮、最多 8 轮, 方案不过关不写代码. Plan-Review 结束后调度器再在机器侧确认 `plan.md` 嵌入的 `aswe-plan-modules` YAML 的存在与合法性, 避免 AI 一眼放行.
- **模块化流水线**: 方案通过后, 按 Plan-Agent 拆分的 Module/Unit 调度 `dev → review → test`; 模块之间串行, 模块内单元 FIFO 流水线, 每个单元独立 8 轮上限, 超限暂停等人工介入.
- **安全边界**: Dev/Test 子进程仅允许在 `projects/<change-id>/` 内读写; 涉及全局安装/sudo 时必须显式声明 `NEEDS_HUMAN_APPROVAL:` 停下来等人确认.
- **真跑测试, 按项目类型分流**: Test-Agent 会根据 `go.mod` / `package.json` / `requirements.txt` / 纯静态前端 等自动切换验证策略, 不会强行给静态站点装 vitest 这类重型框架.
- **可读可机读的任务看板**: 每步自动刷新 `tasks.md` (人类友好) 和 `state.json` (机器真相), 支持 `aswe resume` 断点续跑.
- **三档自动化模式**: `auto` 全自动, `interactive` 关键节点询问, `step` 每步确认.
- **统一 CLI 适配层**: CodeBuddy / Claude Code / Codex / Generic 四种后端无缝切换, 支持首选 + fallback 降级.

---

## 安装

### 方式 A：一键安装到 PATH（推荐，优雅）

```bash
cd aswe
make install                # 装到 $(go env GOPATH)/bin
# 确认 $GOPATH/bin 在你的 PATH 中 (没有就加一句)
# export PATH="$(go env GOPATH)/bin:$PATH"
```

之后在**任何目录**下都可以直接敲:

```bash
aswe new "我想做一个待办小程序"
aswe run todo-app-0425-120000
aswe status todo-app-0425-120000
aswe doctor
aswe --help          # 查看所有子命令
aswe run --help      # 查看 run 的 flag
```

### 方式 B：只构建本地二进制

```bash
make build           # 产出 ./bin/aswe
./bin/aswe --help
```

### 卸载

```bash
make uninstall
```

### 版本号注入

```bash
make build   VERSION=0.2.0
make install VERSION=0.2.0
aswe version         # -> aswe 0.2.0 (commit <hash>) built <date>
```

### Shell 自动补全

cobra 自带补全生成, 敲几个字母再 `<TAB>` 就能补出子命令:

```bash
# zsh
aswe completion zsh > "${fpath[1]}/_aswe"   # 重开终端生效

# bash
aswe completion bash > /etc/bash_completion.d/aswe

# fish
aswe completion fish > ~/.config/fish/completions/aswe.fish
```

要求 Go 1.22+. 至少安装以下任一 CLI: `codebuddy` / `claude-code` / `codex`, 或在 `generic.command` 里配置自定义 AI CLI.

## 配置

首次运行时会读取 `<workspace>/.aswe/config.yaml`, 默认文件已提供:

```yaml
automation_mode: interactive      # auto / interactive / step

pm_agent:
  adapter: codebuddy              # 优先可用: codebuddy / claude-code / codex / generic
  fallback: [claude-code, codex]
  model: ""                       # 留空则使用工具默认模型
  max_turns: 8                    # PM-Agent 最大追问轮数
  min_turns: 3                    # PM-Agent 至少追问轮数 (低于此数强制继续提问)

agents:                           # 每个角色可单独指定 adapter / fallback / model
  spec:        { adapter: codebuddy, fallback: [claude-code, codex] }
  plan:        { adapter: codebuddy, fallback: [claude-code, codex] }
  plan-review: { adapter: codebuddy, fallback: [claude-code, codex] }
  dev:         { adapter: codebuddy, fallback: [claude-code, codex] }
  review:      { adapter: codebuddy, fallback: [claude-code, codex] }
  test:        { adapter: codebuddy, fallback: [claude-code, codex] }

# 循环上下限 (plan↔plan-review 独立计数; dev↔review↔test 共享计数)
max_plan_loops: 8
min_plan_loops: 2                 # plan<->plan-review 至少完成几轮 (未达即便 PASS 也强制改判 FAIL)
max_code_loops: 8                 # 未启用模块化流水线时才生效; 启用模块化后每单元也是 8 轮

# 通用适配器, 用来接入未内建支持的 AI CLI. 占位符:
#   {{PROMPT_FILE}}  一次性提示词文件路径
#   {{WORK_DIR}}     工作目录
generic:
  command: ""                     # 例: "my-ai --prompt-file {{PROMPT_FILE}} --cwd {{WORK_DIR}}"

openspec_dir: openspec            # OpenSpec 目录 (相对 workspace_root)
workspace_root: ""                # 留空则从 cwd 向上探测含 openspec/ 的目录
```

可以为每个 Agent 选不同的 AI 工具; 首选不可用会按 `fallback` 顺序降级, 仍失败时交给 `generic`.

## 使用

```bash
# 1. 启动 PM-Agent, 把模糊需求澄清并落盘成 proposal
aswe new "我想做一个支持团队共享的待办小程序"

# 2. 得到 change-id 后, 启动编排器自动执行 spec → plan ⇄ plan-review → dev (模块化) → review ⇄ test
aswe run todo-app-0425-120000

# 3. 中途 Ctrl-C 后可断点续跑 (run 的 alias, state.json 自动记录进度)
aswe resume todo-app-0425-120000

# 4. 查看进度 (含模块/单元状态)
aswe status todo-app-0425-120000

# 5. 排查 AI CLI 适配器是否就绪
aswe doctor

# 全局 flag:
aswe --config /path/to/config.yaml run <change-id>
aswe --workspace /abs/path run <change-id>
aswe run <change-id> --mode auto        # 覆盖 automation_mode
```

> 💡 如果暂时没 `make install`, 可以用 `./bin/aswe ...` 替代, 行为一致.

## 运行时产物

```
<workspace>/
├── openspec/changes/<change-id>/
│   ├── proposal.md          # PM-Agent 产出 (澄清对话结果)
│   ├── spec.md              # Spec-Agent (OpenSpec 规格: Requirement / Scenario)
│   ├── plan.md              # Plan-Agent (技术方案 + 嵌入 aswe-plan-modules YAML)
│   ├── plan-review.md       # Plan-Review-Agent (方案评审, 含 VERDICT: PASS/FAIL)
│   ├── tasks.md             # 模块/单元进度看板 (人可读, 每步实时刷新)
│   ├── dev.md / review.md / test.md  # 未启用模块化时的整体产物 (回退路径)
│   └── units/<unit-id>/     # 启用模块化后, 每个单元独立产物目录
│       ├── dev.md           #   Dev-Agent 的本轮实现摘要
│       ├── review.md        #   Code-Review 的本轮意见
│       └── test.md          #   Test-Agent 的本轮验证结果
├── projects/<change-id>/    # Dev-Agent 真正落盘代码的位置 (安全边界)
└── .aswe/runs/<change-id>/
    ├── state.json           # 编排状态快照 (含 Modules / Units / Iteration, 支持断点续跑)
    └── events.jsonl         # 事件流日志 (每个 stage 的 start/end/error)
```

## 架构

整体流程:

```
用户需求
   │
   ▼
PM-Agent ──(至少 3 轮追问)──▶ proposal.md
   │
   ▼
Spec-Agent ──▶ spec.md (Requirement / Scenario)
   │
   ▼
┌──────────────── 方案阶段 (最多 8 轮) ────────────────┐
│  Plan-Agent ─────▶ plan.md (含 aswe-plan-modules YAML) │
│       ▲                         │                      │
│       │ FAIL (带反馈)           ▼                      │
│       └────── Plan-Review-Agent ──PASS──▶ 解析 YAML     │
└────────────────────────────────────────────────────────┘
   │
   ▼
┌──────── 模块化流水线 (Module 串行 / Unit FIFO) ─────────┐
│   for each Module:                                       │
│     while 模块未完成:                                    │
│       NextRunnableUnit() ──▶ 按状态推进一步               │
│         pending            ─▶ Dev-Unit                    │
│         dev_done           ─▶ Review-Unit                 │
│         review_passed      ─▶ Test-Unit                   │
│         review_failed / test_failed (iter+1) ─▶ Dev-Unit │
│       每个 Unit 最多 8 轮; 超限 ─▶ 模块 failed 并暂停    │
│   每步刷新 tasks.md 与 state.json                        │
└──────────────────────────────────────────────────────────┘
   │
   ▼
Done ✅  或  Failed 🛑 (等待人工介入, 可 aswe resume 续跑)
```

适配器层 (所有 Agent 共享):

```
          spec  plan  plan-review  dev  review  test
           │     │         │        │     │      │
           └─────┴─────┬───┴────────┴─────┴──────┘
                       ▼
                  CLIAdapter
          ┌────────┬─────────┬────────┬──────────┐
          ▼        ▼         ▼        ▼
      codebuddy  claude-code  codex  generic(自定义命令)
```

- 每个 Agent 都能独立配置适配器; 首选不可用按 `fallback` 降级.
- `dev` / `test` 子进程工作目录 = `projects/<change-id>/`, 安全边界由 prompt + 项目类型分流共同保证.
- 模块化调度见 [`internal/orchestrator/module_pipeline.go`](internal/orchestrator/module_pipeline.go), 单元产物约定见 [`internal/agents/unit_roles.go`](internal/agents/unit_roles.go).
