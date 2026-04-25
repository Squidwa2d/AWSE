package state

import "testing"

func TestExtractPlanModules_OK(t *testing.T) {
	md := "前文任意\n\n```yaml\n# aswe-plan-modules\nmodules:\n  - id: A\n    title: 数据模型\n    goal: 基础\n    units:\n      - id: A.1\n        title: 定义 Todo\n        scope: src/models/*.ts\n        deliverable: 导出 Todo 类型\n      - id: A.2\n        title: 本地持久化\n        scope: src/storage/local.ts\n        deliverable: save/load API\n  - id: B\n    title: UI\n    units:\n      - id: B.1\n        title: 列表视图\n        scope: src/ui/list.tsx\n        deliverable: 可渲染\n```\n\n后文任意"
	mods, err := ExtractPlanModules(md)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(mods) != 2 {
		t.Fatalf("want 2 modules, got %d", len(mods))
	}
	if mods[0].ID != "A" || len(mods[0].Units) != 2 {
		t.Errorf("bad module A: %+v", mods[0])
	}
	if mods[0].Units[1].ID != "A.2" || mods[0].Units[1].Status != UnitPending {
		t.Errorf("bad unit A.2: %+v", mods[0].Units[1])
	}
}

func TestExtractPlanModules_NoMarker(t *testing.T) {
	md := "```yaml\nfoo: bar\n```"
	_, err := ExtractPlanModules(md)
	if err == nil {
		t.Fatal("want error for missing marker")
	}
}

func TestExtractPlanModules_DuplicateUnitID(t *testing.T) {
	md := "```yaml\n# aswe-plan-modules\nmodules:\n  - id: A\n    title: t\n    units:\n      - id: X.1\n        title: a\n      - id: X.1\n        title: b\n```"
	_, err := ExtractPlanModules(md)
	if err == nil {
		t.Fatal("want error for duplicate unit id")
	}
}
