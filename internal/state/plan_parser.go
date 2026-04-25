package state

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// planYAMLMarker 标识 Plan-Agent 在 plan.md 中嵌入的机器可读代码块.
// Plan-Agent 必须输出以 "# aswe-plan-modules" 开头的 YAML 代码块.
const planYAMLMarker = "aswe-plan-modules"

// planYAML YAML 代码块的结构.
type planYAML struct {
	Modules []planYAMLModule `yaml:"modules"`
}

type planYAMLModule struct {
	ID    string         `yaml:"id"`
	Title string         `yaml:"title"`
	Goal  string         `yaml:"goal"`
	Units []planYAMLUnit `yaml:"units"`
}

type planYAMLUnit struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Scope       string `yaml:"scope"`
	Deliverable string `yaml:"deliverable"`
}

// codeBlockRE 匹配 markdown fenced code block.
// 这里不强制 info string 必须精确等于 yaml, 因为不同模型常输出 ``` yaml、
// ```YAML 或带属性的代码块; 真正的识别依据是代码块首个非空行的 marker.
// 捕获组 2 为代码块内容.
var codeBlockRE = regexp.MustCompile("(?ms)^[ \\t]{0,3}```[ \\t]*([^`\\n]*)\\n(.*?)^[ \\t]{0,3}```[ \\t]*$")

// ExtractPlanModules 从 plan.md 原始内容中抽出嵌入的模块/单元 YAML, 并转成 []*Module.
// 解析失败或未找到合法块时返回 (nil, error); 调用方可据此决定是否 fail plan-review.
func ExtractPlanModules(planMarkdown string) ([]*Module, error) {
	block, err := findPlanYAMLBlock(planMarkdown)
	if err != nil {
		return nil, err
	}
	var py planYAML
	if err := yaml.Unmarshal([]byte(block), &py); err != nil {
		return nil, fmt.Errorf("解析 aswe-plan-modules YAML 失败: %w", err)
	}
	if len(py.Modules) == 0 {
		return nil, fmt.Errorf("aswe-plan-modules YAML 至少要包含一个 module")
	}

	seenMod := map[string]bool{}
	seenUnit := map[string]bool{}
	var mods []*Module
	for mi, m := range py.Modules {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return nil, fmt.Errorf("module[%d] 缺少 id", mi)
		}
		if seenMod[id] {
			return nil, fmt.Errorf("module id %q 重复", id)
		}
		seenMod[id] = true
		if len(m.Units) == 0 {
			return nil, fmt.Errorf("module %s 至少要包含一个 unit", id)
		}
		mod := &Module{
			ID:     id,
			Title:  strings.TrimSpace(m.Title),
			Goal:   strings.TrimSpace(m.Goal),
			Status: ModulePending,
		}
		for ui, u := range m.Units {
			uid := strings.TrimSpace(u.ID)
			if uid == "" {
				return nil, fmt.Errorf("module %s unit[%d] 缺少 id", id, ui)
			}
			if seenUnit[uid] {
				return nil, fmt.Errorf("unit id %q 全局重复", uid)
			}
			seenUnit[uid] = true
			mod.Units = append(mod.Units, &Unit{
				ID:          uid,
				Title:       strings.TrimSpace(u.Title),
				Scope:       strings.TrimSpace(u.Scope),
				Deliverable: strings.TrimSpace(u.Deliverable),
				Status:      UnitPending,
				UpdatedAt:   time.Now(),
			})
		}
		mods = append(mods, mod)
	}
	return mods, nil
}

// findPlanYAMLBlock 在 markdown 里寻找内容以 "# aswe-plan-modules" 开头的代码块,
// 返回去除标识行后的纯 YAML 文本.
func findPlanYAMLBlock(md string) (string, error) {
	matches := codeBlockRE.FindAllStringSubmatch(md, -1)
	for _, m := range matches {
		body := m[2]
		trim := strings.TrimLeft(body, " \t\r\n")
		// 首个非空行必须是 "# aswe-plan-modules"
		firstLineEnd := strings.IndexByte(trim, '\n')
		if firstLineEnd < 0 {
			continue
		}
		firstLine := strings.TrimSpace(trim[:firstLineEnd])
		if strings.HasPrefix(firstLine, "#") &&
			strings.Contains(firstLine, planYAMLMarker) {
			return trim[firstLineEnd+1:], nil
		}
	}
	if block := findLoosePlanYAMLBlock(md); block != "" {
		return block, nil
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("plan.md 中未找到 YAML 代码块")
	}
	return "", fmt.Errorf("plan.md 中未找到以 '# %s' 开头的 YAML 代码块", planYAMLMarker)
}

// findLoosePlanYAMLBlock 兼容模型漏写 fenced code block、但保留了 marker 的输出。
// 从 marker 下一行开始收集 YAML, 遇到下一个 markdown 标题或 VERDICT 即停止。
func findLoosePlanYAMLBlock(md string) string {
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") || !strings.Contains(trimmed, planYAMLMarker) {
			continue
		}
		var block []string
		for _, next := range lines[i+1:] {
			t := strings.TrimSpace(next)
			upper := strings.ToUpper(t)
			if strings.HasPrefix(t, "## ") || strings.HasPrefix(upper, "VERDICT:") {
				break
			}
			block = append(block, next)
		}
		return strings.TrimSpace(strings.Join(block, "\n"))
	}
	return ""
}
