package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRescueArtifactByMarker_RescueFromProjectDir
// 模拟"AI 把完整 plan.md 用 Write 写到了 ProjectDir, 而 stdout 只回了简短摘要"的场景:
// rescueArtifactByMarker 应当能把 ProjectDir/plan.md 拽回到 outPath, 让校验通过.
func TestRescueArtifactByMarker_RescueFromProjectDir(t *testing.T) {
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "projects", "x")
	artifactDir := filepath.Join(tmp, ".aswe", "artifacts")
	for _, d := range []string{projectDir, artifactDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	full := "# Plan\n\n## 模块与单元拆分 (机器可读)\n\n```yaml\n# aswe-plan-modules\nmodules:\n  - id: A\n    title: x\n    units:\n      - id: A.1\n        title: y\n        scope: pkg/x\n        deliverable: foo()\n```\n"
	if err := os.WriteFile(filepath.Join(projectDir, "plan.md"), []byte(full), 0o644); err != nil {
		t.Fatalf("write project plan: %v", err)
	}

	outPath := filepath.Join(artifactDir, "plan.md")
	stdoutOnly := "已完成 plan.md, 详见文件."
	if err := os.WriteFile(outPath, []byte(stdoutOnly), 0o644); err != nil {
		t.Fatalf("write artifact plan: %v", err)
	}
	in := &RunInput{
		WorkspaceDir: tmp,
		ProjectDir:   projectDir,
		ArtifactDir:  artifactDir,
	}
	rescued, ok := rescueArtifactByMarker(in, outPath, "plan.md", stdoutOnly, "# aswe-plan-modules")
	if !ok {
		t.Fatalf("应触发 rescue, 实际未触发")
	}
	if !strings.Contains(rescued, "# aswe-plan-modules") {
		t.Fatalf("rescued 内容应当含 marker, got=%q", rescued)
	}
	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("读 outPath 失败: %v", err)
	}
	if !strings.Contains(string(written), "# aswe-plan-modules") {
		t.Fatalf("outPath 应被覆盖为含 marker 的版本, got=%q", string(written))
	}
	if !strings.Contains(string(written), "rescued from") {
		t.Fatalf("outPath 应带 rescue 头注释, got=%q", string(written))
	}
}

// TestRescueArtifactByMarker_NoOpWhenRawHasMarker
// 若 raw 已经含 marker, rescue 应当不做任何事.
func TestRescueArtifactByMarker_NoOpWhenRawHasMarker(t *testing.T) {
	tmp := t.TempDir()
	in := &RunInput{WorkspaceDir: tmp, ProjectDir: tmp, ArtifactDir: tmp}
	raw := "blah\n# aswe-plan-modules\n"
	got, ok := rescueArtifactByMarker(in, filepath.Join(tmp, "plan.md"), "plan.md", raw, "# aswe-plan-modules")
	if ok || got != raw {
		t.Fatalf("有 marker 时应直接 NO-OP, got ok=%v got=%q", ok, got)
	}
}

// TestRescueArtifactByMarker_NoCandidate
// 候选位置都没匹配的文件 -> 不动 outPath.
func TestRescueArtifactByMarker_NoCandidate(t *testing.T) {
	tmp := t.TempDir()
	in := &RunInput{WorkspaceDir: tmp, ProjectDir: tmp, ArtifactDir: tmp}
	out := filepath.Join(tmp, "plan.md")
	if err := os.WriteFile(out, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := rescueArtifactByMarker(in, out, "plan.md", "stub", "# aswe-plan-modules")
	if ok {
		t.Fatalf("没有合法候选时不应触发 rescue")
	}
	if got != "stub" {
		t.Fatalf("没触发时应返回原 raw, got=%q", got)
	}
}
