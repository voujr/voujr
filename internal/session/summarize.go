package session

import (
	"fmt"
	"strings"

	"github.com/voujr/voujr/internal/ai"
)

// foldSummary folds evicted turns into the rolling summary. This is a cheap
// deterministic reducer for the scaffold; a production build calls a fast model
// to produce a faithful natural-language summary of the evicted messages and
// appends it to prev.
func foldSummary(prev string, evicted []ai.Message) string {
	var b strings.Builder
	if prev != "" {
		b.WriteString(prev)
		b.WriteString("\n")
	}
	for _, m := range evicted {
		switch m.Role {
		case ai.RoleUser:
			fmt.Fprintf(&b, "user asked: %s\n", oneLine(m.Content, 120))
		case ai.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				var names []string
				for _, tc := range m.ToolCalls {
					names = append(names, tc.Name)
				}
				fmt.Fprintf(&b, "agent called: %s\n", strings.Join(names, ", "))
			} else if m.Content != "" {
				fmt.Fprintf(&b, "agent concluded: %s\n", oneLine(m.Content, 160))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func oneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
