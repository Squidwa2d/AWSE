package agents

import "strings"

func splitLines(s string) []string { return strings.Split(s, "\n") }
func joinLines(v []string) string  { return strings.Join(v, "\n") }
