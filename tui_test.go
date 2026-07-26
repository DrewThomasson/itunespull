package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSidebarLabelsUseExactDisplayWidth(t *testing.T) {
	labels := []string{"📂 Playlists", "👥 Artists", "💿 Albums", "🎵 All Songs", "🔄 Refresh Library"}

	for _, label := range labels {
		rendered := lipgloss.NewStyle().
			Width(sidebarContentWidth).
			MaxWidth(sidebarContentWidth).
			Render(" " + label)

		if got := lipgloss.Width(rendered); got != sidebarContentWidth {
			t.Errorf("%q rendered at width %d; want %d", label, got, sidebarContentWidth)
		}
		if label == "🔄 Refresh Library" && !strings.Contains(rendered, "Refresh Library") {
			t.Errorf("refresh label was unexpectedly truncated: %q", rendered)
		}
	}
}

func TestPaneDividerAndJunctionAlign(t *testing.T) {
	contentRow := strings.Repeat(" ", sidebarContentWidth) + " │ content"
	borderRow := strings.Repeat("─", sidebarPaneWidth) + "┼" + strings.Repeat("─", 10)

	contentPrefix := strings.SplitN(contentRow, "│", 2)[0]
	borderPrefix := strings.SplitN(borderRow, "┼", 2)[0]
	if got, want := lipgloss.Width(contentPrefix), lipgloss.Width(borderPrefix); got != want {
		t.Fatalf("divider is at display column %d, junction is at display column %d", got, want)
	}
}
