package adapter

import "testing"

// TestAdapterDetection 烟测: 确认 factory 对当前环境能选出可用适配器.
func TestAdapterDetection(t *testing.T) {
	f := &Factory{}
	candidates := []string{"codebuddy", "claude-code", "codex"}
	found := 0
	for _, name := range candidates {
		a, err := f.Build(name, "")
		if err != nil {
			t.Errorf("build %s: %v", name, err)
			continue
		}
		if a.IsAvailable() {
			t.Logf("✅ %s 已安装", name)
			found++
		} else {
			t.Logf("⚠️  %s 未安装", name)
		}
	}
	if found == 0 {
		t.Skip("当前环境没有可用的 CLI, 跳过集成相关断言")
	}
}

func TestResolveFallback(t *testing.T) {
	f := &Factory{}
	// 首选一个肯定不存在的, fallback 中包含当前环境里的 codebuddy
	a, err := f.Resolve("definitely-not-exists", []string{"codebuddy", "claude-code", "codex"}, "")
	if err != nil {
		t.Skipf("环境无任何 CLI: %v", err)
	}
	if !a.IsAvailable() {
		t.Fatalf("Resolve 返回了不可用的 adapter %s", a.Name())
	}
	t.Logf("✅ fallback 选中: %s", a.Name())
}
