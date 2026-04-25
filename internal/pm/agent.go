// Package pm 实现 PM-Agent (阶段 1): 负责与用户多轮对话, 把模糊的口语化
// 需求澄清成结构化的 OpenSpec proposal.
package pm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aswe/aswe/internal/adapter"
)

// Proposal PM-Agent 最终交付给用户确认的产物.
type Proposal struct {
	ChangeID     string
	Title        string
	Markdown     string // 完整的 proposal.md 内容
	DesignMD     string // (可选) design 章节
	ProposalPath string // 落盘路径
}

// Agent 一个可运行的 PM-Agent 会话.
type Agent struct {
	cli         adapter.CLIAdapter
	maxTurns    int
	minTurns    int // 进入 proposal 之前至少完成的追问轮数
	workspace   string
	openspecDir string
	in          *bufio.Reader
	out         io.Writer
}

// Option 可选配置.
type Option func(*Agent)

func WithIO(in io.Reader, out io.Writer) Option {
	return func(a *Agent) { a.in = bufio.NewReader(in); a.out = out }
}

// WithMinTurns 覆盖最小追问轮数 (默认 3).
func WithMinTurns(n int) Option {
	return func(a *Agent) { a.minTurns = n }
}

// New 创建 Agent.
func New(cli adapter.CLIAdapter, workspace, openspecDir string, maxTurns int, opts ...Option) *Agent {
	a := &Agent{
		cli:         cli,
		maxTurns:    maxTurns,
		minTurns:    3,
		workspace:   workspace,
		openspecDir: openspecDir,
		in:          bufio.NewReader(os.Stdin),
		out:         os.Stdout,
	}
	for _, o := range opts {
		o(a)
	}
	if a.maxTurns <= 0 {
		a.maxTurns = 8
	}
	if a.minTurns < 0 {
		a.minTurns = 0
	}
	if a.minTurns > a.maxTurns {
		a.minTurns = a.maxTurns
	}
	return a
}

// Run 主循环. 与用户多轮问答后产出 proposal 并落盘.
func (a *Agent) Run(ctx context.Context, userRequest string) (*Proposal, error) {
	fmt.Fprintf(a.out, "\n🤖 PM-Agent 已启动 (adapter=%s)\n", a.cli.Name())
	fmt.Fprintf(a.out, "📝 初始需求: %s\n\n", userRequest)

	qa := []qaPair{{Role: "user", Content: userRequest}}

	// --- 追问阶段 ---
	for turn := 0; turn < a.maxTurns; turn++ {
		completed := turn // 已完成的有效轮数
		belowMin := completed < a.minTurns
		prompt := buildClarifyPrompt(qa, completed, a.minTurns)
		resp, err := a.cli.Invoke(ctx, adapter.Request{
			Prompt:         prompt,
			WorkDir:        a.workspace,
			Mode:           adapter.ModeChat,
			TimeoutSeconds: 180,
		})
		if err != nil {
			return nil, fmt.Errorf("pm-agent invoke: %w", err)
		}
		questions, done := parseClarifyResponse(resp.Output)

		// 最小轮数硬门槛: 即使模型说 READY, 或者一个问题都没给,
		// 只要还没达到 minTurns, 都强制继续, 用兜底追问保证有东西可答.
		if belowMin {
			if done || len(questions) == 0 {
				questions = defaultFallbackQuestions(completed)
				done = false
				fmt.Fprintf(a.out, "ℹ️  当前仅完成 %d/%d 轮澄清, 强制继续追问.\n", completed, a.minTurns)
			}
		} else {
			if done {
				fmt.Fprintln(a.out, "✅ PM-Agent 认为信息已足够, 进入 proposal 生成阶段.")
				break
			}
			if len(questions) == 0 {
				// 已满足最小轮数但模型没给问题, 视为完成.
				fmt.Fprintln(a.out, "⚠️  PM-Agent 本轮未返回有效问题, 进入 proposal 生成阶段.")
				break
			}
		}

		fmt.Fprintf(a.out, "\n🤖 第 %d 轮澄清 (至少 %d 轮):\n", turn+1, a.minTurns)
		for i, q := range questions {
			fmt.Fprintf(a.out, "  Q%d: %s\n", i+1, q)
		}
		fmt.Fprintln(a.out, "\n请一并回答(可以用自然语言, 多个问题用换行分隔, 输入 /done 跳过剩余轮次):")
		answer, err := a.readMultiline()
		if err != nil {
			return nil, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			if belowMin {
				fmt.Fprintf(a.out, "⚠️  本轮未输入内容, 但尚未达到最小轮数 %d, 请至少写一句回答, 或输入 /done 跳过.\n", a.minTurns)
				// 回退一轮, 让用户重答
				turn--
				continue
			}
			fmt.Fprintln(a.out, "⚠️  未输入任何内容, 提前结束澄清.")
			break
		}
		if answer == "/done" {
			if belowMin {
				fmt.Fprintf(a.out, "ℹ️  /done 被忽略: 尚未达到最小轮数 %d.\n", a.minTurns)
				turn--
				continue
			}
			break
		}
		// 记录问答(把这一轮的问题+回答都喂回下一轮上下文)
		qa = append(qa, qaPair{Role: "assistant", Content: strings.Join(questions, "\n")})
		qa = append(qa, qaPair{Role: "user", Content: answer})
	}

	// --- 生成 proposal ---
	fmt.Fprintln(a.out, "\n🧠 正在生成 proposal.md ...")
	genPrompt := buildProposalPrompt(qa)
	genResp, err := a.cli.Invoke(ctx, adapter.Request{
		Prompt:         genPrompt,
		WorkDir:        a.workspace,
		Mode:           adapter.ModeHeadless,
		TimeoutSeconds: 300,
	})
	if err != nil {
		return nil, fmt.Errorf("pm-agent generate proposal: %w", err)
	}
	md := extractMarkdown(genResp.Output)
	if md == "" {
		md = genResp.Output
	}

	title := extractTitle(userRequest, md)
	changeID := makeChangeID(title)

	// --- 用户确认 ---
	fmt.Fprintf(a.out, "\n=============== 生成的 proposal (change-id=%s) ===============\n%s\n================================================================\n\n", changeID, md)

	confirmed, err := a.confirm("是否接受此 proposal? (y=接受 / n=重来 / e=手动编辑后接受): ")
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return a.Run(ctx, userRequest) // 重来
	}

	// --- 落盘 ---
	changeDir := filepath.Join(a.workspace, a.openspecDir, "changes", changeID)
	if err := os.MkdirAll(changeDir, 0o755); err != nil {
		return nil, err
	}
	proposalPath := filepath.Join(changeDir, "proposal.md")
	if err := os.WriteFile(proposalPath, []byte(md), 0o644); err != nil {
		return nil, err
	}

	fmt.Fprintf(a.out, "✅ 已保存: %s\n", proposalPath)
	return &Proposal{
		ChangeID:     changeID,
		Title:        title,
		Markdown:     md,
		ProposalPath: proposalPath,
	}, nil
}

// --- 辅助 ---

type qaPair struct {
	Role    string // user / assistant
	Content string
}

func (a *Agent) readMultiline() (string, error) {
	var sb strings.Builder
	fmt.Fprint(a.out, "> ")
	for {
		line, err := a.in.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		sb.WriteString(line)
		// 简化: 以单行输入为主. 空行结束多行模式.
		if err == io.EOF || !strings.HasSuffix(strings.TrimRight(line, "\r\n"), "\\") {
			break
		}
		fmt.Fprint(a.out, "… ")
	}
	return sb.String(), nil
}

func (a *Agent) confirm(prompt string) (bool, error) {
	for {
		fmt.Fprint(a.out, prompt)
		line, err := a.in.ReadString('\n')
		if err != nil && err != io.EOF {
			return false, err
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		switch ans {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "e", "edit":
			fmt.Fprintln(a.out, "(手动编辑暂未实现, 默认接受)")
			return true, nil
		}
	}
}

// makeChangeID 根据标题生成 kebab-case id.
func makeChangeID(title string) string {
	t := strings.ToLower(title)
	re := regexp.MustCompile(`[^a-z0-9\p{Han}]+`)
	t = re.ReplaceAllString(t, "-")
	t = strings.Trim(t, "-")
	if t == "" {
		t = "proposal"
	}
	if len(t) > 60 {
		t = t[:60]
	}
	return fmt.Sprintf("%s-%s", t, time.Now().Format("0102-150405"))
}

// extractTitle 尝试从 markdown 里抓 H1, 抓不到就用原始需求.
func extractTitle(fallback, md string) string {
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	if len(fallback) > 50 {
		return fallback[:50]
	}
	return fallback
}

// extractMarkdown 如果模型返回里包了 ```markdown ... ```, 抽出里面的内容.
func extractMarkdown(s string) string {
	re := regexp.MustCompile("(?s)```(?:markdown|md)?\\n(.*?)```")
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(s)
}
