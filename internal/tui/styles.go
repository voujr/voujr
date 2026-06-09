package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/voujr/voujr/internal/tools"
)

var (
	faintStyle = lipgloss.NewStyle().Faint(true)
	warnStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	dangerBox  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("196")).
			Padding(0, 1)
)

func faint(s string) string { return faintStyle.Render(s) }

// approvalView renders the y/N modal for a pending mutation, leading with the
// risk and target so a "wrong-cluster" mistake is hard to make.
func approvalView(req tools.ApprovalRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s on %s\n",
		warnStyle.Render("APPROVE?"),
		strings.ToUpper(req.Risk.String()),
		req.Cluster)
	fmt.Fprintf(&b, "tool: %s   blast radius: %s\n", req.Tool, req.BlastRadius)
	if req.Diff != "" {
		fmt.Fprintf(&b, "\n%s\n", req.Diff)
	}
	b.WriteString("\n[y] apply   [n] reject")
	return dangerBox.Render(b.String())
}
