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

// codeBlockRE 匹配 markdown 里的 ```yaml ... ``` 或 ``` ... ``` 代码块.
// 使用 (?s) 让 "." 跨行. 捕获组 1 为代码块内容.
var codeBlockRE = regexp.MustCompile("(?s)```(?:yaml|yml)?\\s*\\n(.*?)```")

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
	if len(matches) == 0 {
		return "", fmt.Errorf("plan.md 中未找到 YAML 代码块")
	}
	for _, m := range matches {
		body := m[1]
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
	return "", fmt.Errorf("plan.md 中未找到以 '# %s' 开头的 YAML 代码块", planYAMLMarker)
}
