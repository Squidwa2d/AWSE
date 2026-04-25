package agents

import "testing"

func TestSanitizeUnitID(t *testing.T) {
	ok := []string{"A", "A.1", "core_db-2", "B.10.x"}
	for _, id := range ok {
		if got, err := sanitizeUnitID(id); err != nil || got != id {
			t.Errorf("sanitizeUnitID(%q) = %q, %v; want %q, nil", id, got, err, id)
		}
	}

	bad := []string{
		"", "  ", ".", "..",
		"../etc", "A/../B", "A/B",
		"A B", "A\\B", "中文",
		"A.\nB", "../../tmp",
	}
	for _, id := range bad {
		if _, err := sanitizeUnitID(id); err == nil {
			t.Errorf("sanitizeUnitID(%q) 应当报错, 实际通过", id)
		}
	}
}
