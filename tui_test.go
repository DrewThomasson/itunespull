package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

func newScrollTestModel(n int) model {
	tracks := make([]Track, n)
	for i := range tracks {
		tracks[i] = Track{Title: "Song", Artist: "Artist", Path: "/t/" + string(rune('a'+i%26)) + strings.Repeat("x", i)}
	}
	return model{
		library:        &Library{Tracks: tracks},
		allSongsTracks: tracks,
		pm:             &PlaybackManager{},
		focus:          "right",
		rightPaneState: "all_songs",
		width:          100,
		height:         20, // listHeight = 10
	}
}

func step(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	nm, _ := m.Update(msg)
	return nm.(model)
}

func TestWheelScrollsViewportNotSelection(t *testing.T) {
	m := newScrollTestModel(50)
	for i := 0; i < 5; i++ {
		m = step(t, m, tea.MouseMsg{X: 40, Y: 6, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	}
	if m.rightScrollOffset != 15 {
		t.Fatalf("scroll offset = %d; want 15", m.rightScrollOffset)
	}
	if m.rightIdx != 0 {
		t.Fatalf("wheel moved selection to %d; want it to stay at 0", m.rightIdx)
	}
	for i := 0; i < 50; i++ {
		m = step(t, m, tea.MouseMsg{X: 40, Y: 6, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	}
	if m.rightScrollOffset != 40 {
		t.Fatalf("scroll offset = %d; want clamp at 40", m.rightScrollOffset)
	}
}

func TestClickAfterScrollSelectsVisibleRow(t *testing.T) {
	m := newScrollTestModel(50)
	for i := 0; i < 4; i++ {
		m = step(t, m, tea.MouseMsg{X: 40, Y: 6, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	}
	// Third visible row (y = 4 + 2) should be index offset + 2.
	m = step(t, m, tea.MouseMsg{X: 40, Y: 6, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if m.rightIdx != 14 {
		t.Fatalf("clicked row selected index %d; want 14", m.rightIdx)
	}
	if m.rightScrollOffset != 12 {
		t.Fatalf("click jumped scroll to %d; want it to stay at 12", m.rightScrollOffset)
	}
}

func TestKeyboardNavigationKeepsSelectionVisible(t *testing.T) {
	m := newScrollTestModel(50)
	for i := 0; i < 15; i++ {
		m = step(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.rightIdx != 15 || m.rightScrollOffset != 6 {
		t.Fatalf("idx=%d offset=%d; want idx=15 offset=6", m.rightIdx, m.rightScrollOffset)
	}
	for i := 0; i < 10; i++ {
		m = step(t, m, tea.KeyMsg{Type: tea.KeyUp})
	}
	if m.rightIdx != 5 || m.rightScrollOffset != 5 {
		t.Fatalf("idx=%d offset=%d; want idx=5 offset=5", m.rightIdx, m.rightScrollOffset)
	}
}
