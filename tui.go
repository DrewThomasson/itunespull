package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	sidebarContentWidth = 19
	sidebarPaneWidth    = sidebarContentWidth + 1 // trailing space before the divider
)

var version = "1.0.2"

type Theme struct {
	Name           string
	HeaderColor    string
	HighlightBG    string
	HighlightFG    string
	InfoColor      string
	EqualizerColor string
	MutedColor     string
	MetaColor      string
	BorderColor    string
	SelectionColor string
}

var themes = []Theme{
	{
		Name:           "Cyberpunk",
		HeaderColor:    "#00f5d4", // Electric Cyan
		HighlightBG:    "#ff007f", // Neon Magenta
		HighlightFG:    "#000000",
		InfoColor:      "#ffb703", // Hot Gold
		EqualizerColor: "#00f5d4",
		MutedColor:     "#ef476f",
		MetaColor:      "#ff007f",
		BorderColor:    "#4a4e69",
		SelectionColor: "#00f5d4",
	},
	{
		Name:           "iTunes",
		HeaderColor:    "#007aff",
		HighlightBG:    "#007aff",
		HighlightFG:    "#ffffff",
		InfoColor:      "#ffffff",
		EqualizerColor: "#007aff",
		MutedColor:     "#ff3b30",
		MetaColor:      "#007aff",
		BorderColor:    "#8e8e93",
		SelectionColor: "#007aff",
	},
	{
		Name:           "Matrix",
		HeaderColor:    "#00ff00",
		HighlightBG:    "#00ff00",
		HighlightFG:    "#000000",
		InfoColor:      "#00ff00",
		EqualizerColor: "#00ff00",
		MutedColor:     "#ff0000",
		MetaColor:      "#00ff00",
		BorderColor:    "#005500",
		SelectionColor: "#00ff00",
	},
	{
		Name:           "Midnight",
		HeaderColor:    "#a3b18a",
		HighlightBG:    "#dad7cd",
		HighlightFG:    "#344e41",
		InfoColor:      "#a3b18a",
		EqualizerColor: "#588157",
		MutedColor:     "#e63946",
		MetaColor:      "#344e41",
		BorderColor:    "#3a5a40",
		SelectionColor: "#a3b18a",
	},
}

type tickMsg struct{}

type model struct {
	cfg         *Config
	controlPath string
	activeApp   string
	library     *Library
	pm          *PlaybackManager

	focus         string // "left" or "right"
	sidebarIdx    int
	playlistsList []string
	artistsList   []string
	albumsList    []string

	rightIdx          int
	rightScrollOffset int
	rightPaneState    string // "playlists", "artists", "albums", "all_songs", "playlist_tracks", "artist_tracks", "album_tracks", "search"

	selectedPlaylist string
	selectedArtist   string
	selectedAlbum    string

	playlistTracks     []Track
	artistTracks       []Track
	albumTracks        []Track
	filteredTracks     []Track
	allSongsTracks     []Track
	sortMode           string // "default", "title", "artist", "rating", "play_count"
	showSortPrompt     bool
	showLyrics         bool
	showVisualizer     bool
	autoScroll         bool
	lyricsScrollOffset int

	searchQuery     string
	isSearching     bool
	rateNotifyTime  time.Time
	themeIdx        int
	themeNotifyTime time.Time
	showInfoCard    bool

	downloadingTrack   string
	downloadStatus     string // "", "downloading", "success", "error"
	downloadNotifyTime time.Time

	width           int
	height          int
	rebuildingCache bool
	lastTrackDBID   int
	err             error
}

func initialModel(cfg *Config, controlPath string, activeApp string, lib *Library) model {
	pm := NewPlaybackManager(cfg, controlPath, activeApp)
	pm.StartMonitorLoop()

	playlists := make([]string, 0, len(lib.Playlists))
	for name := range lib.Playlists {
		playlists = append(playlists, name)
	}
	sort.Strings(playlists)

	artistsSet := make(map[string]bool)
	albumsSet := make(map[string]bool)
	for _, t := range lib.Tracks {
		if t.Artist != "" {
			artistsSet[t.Artist] = true
		}
		if t.Album != "" {
			albumsSet[t.Album] = true
		}
	}

	artists := make([]string, 0, len(artistsSet))
	for a := range artistsSet {
		artists = append(artists, a)
	}
	sort.Strings(artists)

	albums := make([]string, 0, len(albumsSet))
	for al := range albumsSet {
		albums = append(albums, al)
	}
	sort.Strings(albums)

	m := model{
		cfg:            cfg,
		controlPath:    controlPath,
		activeApp:      activeApp,
		library:        lib,
		allSongsTracks: append([]Track(nil), lib.Tracks...),
		sortMode:       "default",
		pm:             pm,
		focus:          "left",
		sidebarIdx:     0,
		playlistsList:  playlists,
		artistsList:    artists,
		albumsList:     albums,
		rightPaneState: "playlists",
		showVisualizer: true,
		autoScroll:     true,
		themeIdx:       0,
	}

	// Set up callback for when track ends to refresh UI state
	pm.onTrackEnd = func() {
		// Just a hook for auto-play tracking if needed
	}

	return m
}

func (m model) Init() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// wheelScrollLines is how many rows one mouse-wheel tick scrolls the list.
const wheelScrollLines = 3

// listHeight is the number of visible rows in the sidebar/right pane.
func (m model) listHeight() int {
	return m.height - 10
}

// clampScroll keeps rightScrollOffset inside the valid range for the active list.
func (m *model) clampScroll() {
	maxOffset := m.getActiveListLength() - m.listHeight()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.rightScrollOffset > maxOffset {
		m.rightScrollOffset = maxOffset
	}
	if m.rightScrollOffset < 0 {
		m.rightScrollOffset = 0
	}
}

// ensureSelectionVisible scrolls the right pane just enough to show rightIdx.
func (m *model) ensureSelectionVisible() {
	h := m.listHeight()
	if h < 1 {
		return
	}
	if m.rightIdx < m.rightScrollOffset {
		m.rightScrollOffset = m.rightIdx
	} else if m.rightIdx >= m.rightScrollOffset+h {
		m.rightScrollOffset = m.rightIdx - h + 1
	}
}

// Update wraps update so the scroll position is kept in the model (View has a
// value receiver, so anything it changes is lost). Keyboard navigation that
// moves the selection scrolls it into view; mouse-wheel scrolling moves only
// the viewport and leaves the selection where it is.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	prevIdx, prevState := m.rightIdx, m.rightPaneState
	newModel, cmd := m.update(msg)
	nm, ok := newModel.(model)
	if !ok {
		return newModel, cmd
	}
	if nm.rightIdx != prevIdx || nm.rightPaneState != prevState {
		nm.ensureSelectionVisible()
	}
	nm.clampScroll()
	return nm, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case downloadResultMsg:
		m.downloadNotifyTime = time.Now()
		if msg.err != nil {
			m.downloadStatus = "error"
			m.err = fmt.Errorf("Download failed: %w", msg.err)
		} else {
			m.downloadStatus = "success"
		}
		return m, nil

	case tickMsg:
		// Periodic tick to refresh playbar and progress bar
		m.pm.mu.Lock()
		var currentDBID int
		if m.pm.CurrentTrack != nil {
			currentDBID = m.pm.CurrentTrack.DBID
		}
		isPlaying := m.pm.CurrentTrack != nil && m.pm.mpvCmd != nil && !m.pm.IsPaused
		posVal := m.pm.Position
		durVal := m.pm.Duration
		m.pm.mu.Unlock()
		if currentDBID != m.lastTrackDBID {
			m.lastTrackDBID = currentDBID
			m.lyricsScrollOffset = 0
			m.autoScroll = true
		}

		if m.autoScroll && m.showLyrics && durVal > 0 {
			m.pm.mu.Lock()
			lyricsText := m.pm.CurrentLyrics
			m.pm.mu.Unlock()
			lines := strings.Split(lyricsText, "\n")
			paneHeight := m.height - 10
			maxOffset := len(lines) - paneHeight + 3
			if maxOffset > 0 {
				pct := posVal / durVal
				m.lyricsScrollOffset = int(pct * float64(maxOffset))
				if m.lyricsScrollOffset > maxOffset {
					m.lyricsScrollOffset = maxOffset
				}
				if m.lyricsScrollOffset < 0 {
					m.lyricsScrollOffset = 0
				}
			}
		}

		tickDuration := 500 * time.Millisecond
		if m.showVisualizer && isPlaying {
			tickDuration = 120 * time.Millisecond
		}
		return m, tea.Tick(tickDuration, func(t time.Time) tea.Msg {
			return tickMsg{}
		})

	case tea.MouseMsg:
		if m.showInfoCard {
			if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
				m.showInfoCard = false
			}
			return m, nil
		}
		m.err = nil // clear error on action

		if msg.Button == tea.MouseButtonWheelUp {
			if msg.X <= 20 {
				if m.sidebarIdx > 0 {
					m.sidebarIdx--
					m.updateLivePreview()
				}
			} else {
				if m.showLyrics {
					m.autoScroll = false
					if m.lyricsScrollOffset > 0 {
						m.lyricsScrollOffset--
					}
				} else {
					m.rightScrollOffset -= wheelScrollLines
					m.clampScroll()
				}
			}
			return m, nil
		}

		if msg.Button == tea.MouseButtonWheelDown {
			if msg.X <= 20 {
				if m.sidebarIdx < 4 {
					m.sidebarIdx++
					m.updateLivePreview()
				}
			} else {
				if m.showLyrics {
					m.autoScroll = false
					m.pm.mu.Lock()
					lyricsText := m.pm.CurrentLyrics
					m.pm.mu.Unlock()
					lines := strings.Split(lyricsText, "\n")
					paneHeight := m.height - 10
					if m.lyricsScrollOffset < len(lines)-paneHeight+3 {
						m.lyricsScrollOffset++
					}
				} else {
					m.rightScrollOffset += wheelScrollLines
					m.clampScroll()
				}
			}
			return m, nil
		}

		if msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft {
			y := msg.Y
			x := msg.X

			// Check click on playbar rating stars (4th row from bottom)
			if y == m.height-4 && m.pm.CurrentTrack != nil {
				if x >= m.width-7 && x <= m.width-3 {
					clickedRating := x - (m.width - 7) + 1
					m.pm.RateCurrentTrack(clickedRating)
					m.updateTrackRatingInLibrary(m.pm.CurrentTrack.Path, clickedRating*20)
					m.rateNotifyTime = time.Now()
					return m, nil
				}
			}

			// Check click on playbar progress bar (3rd row from bottom)
			if y == m.height-3 && !m.showLyrics && !m.showSortPrompt && m.pm.CurrentTrack != nil {
				volume := m.pm.Volume
				isMuted := m.pm.IsMuted
				volBar := ""
				if isMuted {
					volBar = "░░░░░░░░░░"
				} else {
					filledVol := volume / 10
					if filledVol > 10 {
						filledVol = 10
					}
					if filledVol < 0 {
						filledVol = 0
					}
					volBar = strings.Repeat("█", filledVol) + strings.Repeat("░", 10-filledVol)
				}
				var volText string
				if isMuted {
					volText = "Vol: [░░░░░░░░░░] " + "MUTED"
				} else {
					volText = fmt.Sprintf("Vol: [%s] %d%%", volBar, volume)
				}
				volVisualWidth := lipgloss.Width(volText)
				barWidth := m.width - 24 - volVisualWidth
				if barWidth < 5 {
					barWidth = 5
				}

				// Seek click
				if m.pm.Duration > 0 && x >= 3 && x < 3+barWidth {
					clickPct := float64(x-3) / float64(barWidth)
					targetTime := clickPct * m.pm.Duration
					m.pm.SeekAbsolute(targetTime)
					return m, nil
				}

				// Volume slider click
				startX := 26 + barWidth
				if x >= startX && x < startX+10 {
					clickIdx := x - startX
					newVol := (clickIdx + 1) * 10
					if newVol > 100 {
						newVol = 100
					}
					m.pm.SetVolume(newVol)
					return m, nil
				}
			}

			listHeight := m.height - 10
			if y >= 4 && y < 4+listHeight {
				clickedRow := y - 4
				if x < 21 {
					// Sidebar click
					if clickedRow < 5 {
						m.showLyrics = false
						// Exit search mode if clicked elsewhere
						if m.isSearching {
							m.isSearching = false
							m.searchQuery = ""
						}
						m.focus = "left"
						m.sidebarIdx = clickedRow
						if clickedRow == 4 { // Refresh Library
							m.rebuildingCache = true
							return m, func() tea.Msg {
								lib, err := loadLibraryCache(m.cfg, m.controlPath, m.activeApp, true)
								if err != nil {
									return err
								}
								return lib
							}
						} else {
							m.updateLivePreview()
						}
					}
				} else if x >= 22 {
					// Right pane click
					clickIdx := m.rightScrollOffset + clickedRow
					listLen := m.getActiveListLength()
					if clickIdx < listLen {
						m.focus = "right"
						if m.rightIdx == clickIdx {
							// Double-click/confirm selection
							m.handleRightPaneSelection()
						} else {
							m.rightIdx = clickIdx
						}
					}
				}
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.showInfoCard {
			switch msg.String() {
			case "q", "ctrl+c":
				m.pm.Close()
				return m, tea.Quit
			case "i", "I", "esc":
				m.showInfoCard = false
			}
			return m, nil
		}

		if m.showSortPrompt {
			switch msg.String() {
			case "q", "ctrl+c":
				m.pm.Close()
				return m, tea.Quit
			case "esc":
				m.showSortPrompt = false
			case "1":
				m.sortMode = "default"
				m.applySort()
				m.showSortPrompt = false
			case "2":
				m.sortMode = "title"
				m.applySort()
				m.showSortPrompt = false
			case "3":
				m.sortMode = "artist"
				m.applySort()
				m.showSortPrompt = false
			case "4":
				m.sortMode = "rating"
				m.applySort()
				m.showSortPrompt = false
			case "5":
				m.sortMode = "play_count"
				m.applySort()
				m.showSortPrompt = false
			}
			return m, nil
		}

		if m.showLyrics {
			switch msg.String() {
			case "q", "ctrl+c":
				m.pm.Close()
				return m, tea.Quit
			case "l", "L", "esc":
				m.showLyrics = false
				return m, nil
			case "up":
				m.autoScroll = false
				if m.lyricsScrollOffset > 0 {
					m.lyricsScrollOffset--
				}
				return m, nil
			case "down":
				m.autoScroll = false
				m.pm.mu.Lock()
				lyricsText := m.pm.CurrentLyrics
				m.pm.mu.Unlock()
				lines := strings.Split(lyricsText, "\n")
				paneHeight := m.height - 10
				if m.lyricsScrollOffset < len(lines)-paneHeight+3 {
					m.lyricsScrollOffset++
				}
				return m, nil
			case "a", "A":
				m.autoScroll = !m.autoScroll
				return m, nil
			case " ":
				m.pm.PauseToggle()
				return m, nil
			case "+", "=":
				m.pm.SetVolume(m.pm.Volume + 5)
				return m, nil
			case "-", "_":
				m.pm.SetVolume(m.pm.Volume - 5)
				return m, nil
			case "m", "M":
				m.pm.MuteToggle()
				return m, nil
			case "shift+right", "]":
				m.pm.Seek(5)
				return m, nil
			case "shift+left", "[":
				m.pm.Seek(-5)
				return m, nil
			case "n", "N":
				m.pm.Next()
				m.syncSelectedWithActiveTrack()
				m.lyricsScrollOffset = 0
				return m, nil
			case "p", "P":
				m.pm.Prev()
				m.syncSelectedWithActiveTrack()
				m.lyricsScrollOffset = 0
				return m, nil
			case "d", "D":
				return m.downloadActiveTrackCmd()
			}
		}

		if m.isSearching {
			switch msg.String() {
			case "enter":
				m.isSearching = false
				m.focus = "right"
			case "esc":
				m.isSearching = false
				m.searchQuery = ""
				m.updateLivePreview()
			case "backspace":
				if len(m.searchQuery) > 0 {
					m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
					m.performSearch()
				}
			case "down":
				listLen := m.getActiveListLength()
				if m.rightIdx < listLen-1 {
					m.rightIdx++
				}
			case "up":
				if m.rightIdx > 0 {
					m.rightIdx--
				}
			default:
				if len(msg.String()) == 1 {
					m.searchQuery += msg.String()
					m.performSearch()
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.pm.Close()
			return m, tea.Quit

		case "i", "I":
			if m.focus == "right" {
				switch m.rightPaneState {
				case "all_songs", "playlist_tracks", "artist_tracks", "album_tracks", "search":
					m.showInfoCard = true
				}
			}

		case "l", "L":
			if m.pm.CurrentTrack != nil {
				m.showLyrics = !m.showLyrics
				m.lyricsScrollOffset = 0
			}

		case "v", "V":
			m.showVisualizer = !m.showVisualizer

		case "t", "T":
			m.themeIdx = (m.themeIdx + 1) % len(themes)
			m.themeNotifyTime = time.Now()

		case "a", "A":
			if m.showLyrics {
				m.autoScroll = !m.autoScroll
			}

		case "r", "R":
			switch m.pm.RepeatMode {
			case "off":
				m.pm.RepeatMode = "one"
			case "one":
				m.pm.RepeatMode = "all"
			default:
				m.pm.RepeatMode = "off"
			}

		case "z", "Z":
			m.pm.ToggleShuffle()

		case "d", "D":
			return m.downloadActiveTrackCmd()

		case "s", "S":
			m.pm.Stop()

		case " ":
			m.pm.PauseToggle()

		case "n", "N":
			m.pm.Next()
			m.syncSelectedWithActiveTrack()

		case "p", "P":
			m.pm.Prev()
			m.syncSelectedWithActiveTrack()

		case "+", "=":
			m.pm.SetVolume(m.pm.Volume + 5)

		case "-", "_":
			m.pm.SetVolume(m.pm.Volume - 5)

		case "m", "M":
			m.pm.MuteToggle()

		case "/":
			m.isSearching = true
			m.searchQuery = ""
			m.rightPaneState = "search"
			m.filteredTracks = append([]Track(nil), m.library.Tracks...)
			m.rightIdx = 0
			m.rightScrollOffset = 0

		case "o", "O":
			if m.focus == "right" && m.getActiveTrackSlice() != nil {
				m.showSortPrompt = true
			}

		case "1", "2", "3", "4", "5":
			stars := int(msg.String()[0] - '0')
			m.pm.RateCurrentTrack(stars)
			if m.pm.CurrentTrack != nil {
				m.updateTrackRatingInLibrary(m.pm.CurrentTrack.Path, stars*20)
			}
			m.rateNotifyTime = time.Now()

		case "down":
			if m.focus == "left" {
				if m.sidebarIdx < 4 {
					m.sidebarIdx++
					m.updateLivePreview()
				}
			} else {
				listLen := m.getActiveListLength()
				if m.rightIdx < listLen-1 {
					m.rightIdx++
				}
			}

		case "up":
			if m.focus == "left" {
				if m.sidebarIdx > 0 {
					m.sidebarIdx--
					m.updateLivePreview()
				}
			} else {
				if m.rightIdx > 0 {
					m.rightIdx--
				}
			}

		case "right":
			if m.focus == "left" && m.sidebarIdx != 4 {
				m.focus = "right"
				m.rightIdx = 0
				m.rightScrollOffset = 0
			} else if m.focus == "right" {
				m.handleRightPaneSelection()
			}

		case "left":
			if m.focus == "right" {
				m.goBackOneLevel()
			}

		case "shift+right", "]":
			m.pm.Seek(5)

		case "shift+left", "[":
			m.pm.Seek(-5)

		case "esc":
			m.pm.Stop()
			if m.focus == "right" {
				m.goBackOneLevel()
			} else {
				m.focus = "left"
			}

		case "enter":
			if m.focus == "left" {
				if m.sidebarIdx == 4 { // Refresh Library
					m.rebuildingCache = true
					return m, func() tea.Msg {
						lib, err := loadLibraryCache(m.cfg, m.controlPath, m.activeApp, true)
						if err != nil {
							return err
						}
						return lib
					}
				} else {
					m.focus = "right"
					m.rightIdx = 0
					m.rightScrollOffset = 0
				}
			} else {
				m.handleRightPaneSelection()
			}
		}

	case *Library:
		// Rebuilding cache completed
		m.library = msg
		m.allSongsTracks = append([]Track(nil), msg.Tracks...)
		m.rebuildingCache = false
		m.rebuildLists()
		m.sidebarIdx = 0
		m.sortMode = "default"
		m.updateLivePreview()

	case error:
		m.err = msg
		m.rebuildingCache = false
	}

	return m, nil
}

func (m *model) performSearch() {
	q := strings.ToLower(m.searchQuery)
	m.filteredTracks = nil
	for _, t := range m.library.Tracks {
		if strings.Contains(strings.ToLower(t.Title), q) ||
			strings.Contains(strings.ToLower(t.Artist), q) ||
			strings.Contains(strings.ToLower(t.Album), q) {
			m.filteredTracks = append(m.filteredTracks, t)
		}
	}
	if m.sortMode != "default" {
		sortTracks(m.filteredTracks, m.sortMode)
	}
	m.rightIdx = 0
	m.rightScrollOffset = 0
}

func (m *model) updateLivePreview() {
	m.rightIdx = 0
	m.rightScrollOffset = 0
	m.sortMode = "default"
	m.showLyrics = false
	switch m.sidebarIdx {
	case 0:
		m.rightPaneState = "playlists"
	case 1:
		m.rightPaneState = "artists"
	case 2:
		m.rightPaneState = "albums"
	case 3:
		m.rightPaneState = "all_songs"
		m.allSongsTracks = append([]Track(nil), m.library.Tracks...)
	}
}

func (m *model) goBackOneLevel() {
	switch m.rightPaneState {
	case "playlist_tracks":
		m.rightPaneState = "playlists"
		m.rightIdx = 0
		for i, name := range m.playlistsList {
			if name == m.selectedPlaylist {
				m.rightIdx = i
				break
			}
		}
	case "artist_tracks":
		m.rightPaneState = "artists"
		m.rightIdx = 0
		for i, name := range m.artistsList {
			if name == m.selectedArtist {
				m.rightIdx = i
				break
			}
		}
	case "album_tracks":
		m.rightPaneState = "albums"
		m.rightIdx = 0
		for i, name := range m.albumsList {
			if name == m.selectedAlbum {
				m.rightIdx = i
				break
			}
		}
	default:
		m.focus = "left"
	}
	m.rightScrollOffset = 0
}

func (m *model) handleRightPaneSelection() {
	switch m.rightPaneState {
	case "playlists":
		if m.rightIdx < len(m.playlistsList) {
			m.selectedPlaylist = m.playlistsList[m.rightIdx]
			indices := m.library.Playlists[m.selectedPlaylist]
			m.playlistTracks = nil
			for _, idx := range indices {
				if idx < len(m.library.Tracks) {
					m.playlistTracks = append(m.playlistTracks, m.library.Tracks[idx])
				}
			}
			m.rightPaneState = "playlist_tracks"
			m.rightIdx = 0
			m.rightScrollOffset = 0
			m.sortMode = "default"
		}
	case "artists":
		if m.rightIdx < len(m.artistsList) {
			m.selectedArtist = m.artistsList[m.rightIdx]
			m.artistTracks = nil
			for _, t := range m.library.Tracks {
				if t.Artist == m.selectedArtist {
					m.artistTracks = append(m.artistTracks, t)
				}
			}
			m.rightPaneState = "artist_tracks"
			m.rightIdx = 0
			m.rightScrollOffset = 0
			m.sortMode = "default"
		}
	case "albums":
		if m.rightIdx < len(m.albumsList) {
			m.selectedAlbum = m.albumsList[m.rightIdx]
			m.albumTracks = nil
			for _, t := range m.library.Tracks {
				if t.Album == m.selectedAlbum {
					m.albumTracks = append(m.albumTracks, t)
				}
			}
			m.rightPaneState = "album_tracks"
			m.rightIdx = 0
			m.rightScrollOffset = 0
			m.sortMode = "default"
		}
	case "all_songs":
		m.pm.SetQueue(m.allSongsTracks, m.rightIdx)
		m.pm.Play(m.allSongsTracks[m.rightIdx])
	case "playlist_tracks":
		m.pm.SetQueue(m.playlistTracks, m.rightIdx)
		m.pm.Play(m.playlistTracks[m.rightIdx])
	case "artist_tracks":
		m.pm.SetQueue(m.artistTracks, m.rightIdx)
		m.pm.Play(m.artistTracks[m.rightIdx])
	case "album_tracks":
		m.pm.SetQueue(m.albumTracks, m.rightIdx)
		m.pm.Play(m.albumTracks[m.rightIdx])
	case "search":
		m.isSearching = false
		m.pm.SetQueue(m.filteredTracks, m.rightIdx)
		m.pm.Play(m.filteredTracks[m.rightIdx])
	}
}

func (m *model) getActiveListLength() int {
	switch m.rightPaneState {
	case "playlists":
		return len(m.playlistsList)
	case "artists":
		return len(m.artistsList)
	case "albums":
		return len(m.albumsList)
	case "all_songs":
		return len(m.allSongsTracks)
	case "playlist_tracks":
		return len(m.playlistTracks)
	case "artist_tracks":
		return len(m.artistTracks)
	case "album_tracks":
		return len(m.albumTracks)
	case "search":
		return len(m.filteredTracks)
	}
	return 0
}

func (m *model) syncSelectedWithActiveTrack() {
	if m.pm.CurrentTrack == nil {
		return
	}
	var targetList []Track
	switch m.rightPaneState {
	case "all_songs":
		targetList = m.allSongsTracks
	case "playlist_tracks":
		targetList = m.playlistTracks
	case "artist_tracks":
		targetList = m.artistTracks
	case "album_tracks":
		targetList = m.albumTracks
	case "search":
		targetList = m.filteredTracks
	default:
		return
	}

	for i, t := range targetList {
		if t.Path == m.pm.CurrentTrack.Path {
			m.rightIdx = i
			break
		}
	}
}

func (m *model) rebuildLists() {
	playlists := make([]string, 0, len(m.library.Playlists))
	for name := range m.library.Playlists {
		playlists = append(playlists, name)
	}
	sort.Strings(playlists)
	m.playlistsList = playlists

	artistsSet := make(map[string]bool)
	albumsSet := make(map[string]bool)
	for _, t := range m.library.Tracks {
		if t.Artist != "" {
			artistsSet[t.Artist] = true
		}
		if t.Album != "" {
			albumsSet[t.Album] = true
		}
	}

	artists := make([]string, 0, len(artistsSet))
	for a := range artistsSet {
		artists = append(artists, a)
	}
	sort.Strings(artists)
	m.artistsList = artists

	albums := make([]string, 0, len(albumsSet))
	for al := range albumsSet {
		albums = append(albums, al)
	}
	sort.Strings(albums)
	m.albumsList = albums
}

func (m model) currentTheme() Theme {
	if m.themeIdx < 0 || m.themeIdx >= len(themes) {
		return themes[0]
	}
	return themes[m.themeIdx]
}

func (m model) View() string {
	theme := m.currentTheme()
	if m.rebuildingCache {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Bold(true).Render(fmt.Sprintf("🔄 Syncing %s Library...", m.activeApp)))
	}
	if m.err != nil {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Foreground(lipgloss.Color(theme.MutedColor)).Render(fmt.Sprintf("❌ Error: %v\nPress Q to quit", m.err)))
	}

	// 1. Header Row
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.HeaderColor)).Bold(true)
	headerText := fmt.Sprintf("itunespull v%s (Go)", version)
	statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor))
	statusText := fmt.Sprintf("🟢 Connected to Mac (%s)", m.activeApp)

	headerPadding := max(0, m.width-lipgloss.Width(headerText)-lipgloss.Width(statusText))
	header := fmt.Sprintf("%s%s%s\n", headerStyle.Render(headerText), strings.Repeat(" ", headerPadding), statusStyle.Render(statusText))
	separator := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render(strings.Repeat("─", m.width)) + "\n"

	// Calculations for list sizing
	listHeight := m.height - 10
	if listHeight < 3 {
		return "Terminal window too small."
	}

	// Scroll position is maintained in Update; just make sure it is in range.
	m.clampScroll()

	// 2. Left Sidebar Layout
	sidebarItems := []string{"📂 Playlists", "👥 Artists", "💿 Albums", "🎵 All Songs", "🔄 Refresh Library"}
	var sidebarLines []string
	for idx, item := range sidebarItems {
		var style lipgloss.Style
		if m.focus == "left" && idx == m.sidebarIdx {
			style = lipgloss.NewStyle().Background(lipgloss.Color(theme.HighlightBG)).Foreground(lipgloss.Color(theme.HighlightFG))
		} else if m.focus == "right" && idx == m.sidebarIdx {
			style = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color(theme.SelectionColor))
		} else {
			style = lipgloss.NewStyle()
		}
		sidebarLines = append(sidebarLines, style.Width(sidebarContentWidth).MaxWidth(sidebarContentWidth).Render(" "+item))
	}
	// Pad sidebar to fill height
	for len(sidebarLines) < listHeight {
		sidebarLines = append(sidebarLines, strings.Repeat(" ", sidebarContentWidth))
	}

	// 3. Right Pane Layout
	var headerTitle string
	var rightLines []string

	if m.showLyrics {
		m.pm.mu.Lock()
		curTrack := m.pm.CurrentTrack
		artworkRaw := m.pm.CurrentArtwork
		lyricsRaw := m.pm.CurrentLyrics
		m.pm.mu.Unlock()

		if curTrack != nil {
			headerTitle = fmt.Sprintf("🎵 Lyrics: %s - %s", curTrack.Title, curTrack.Artist)

			lyricsLines := strings.Split(lyricsRaw, "\n")
			var artLines []string
			if artworkRaw != "" {
				artLines = strings.Split(artworkRaw, "\n")
			} else {
				// 12-line placeholder box
				artLines = []string{
					" ┌──────────────────────┐",
					" │                      │",
					" │                      │",
					" │                      │",
					" │      NO ARTWORK      │",
					" │                      │",
					" │                      │",
					" │                      │",
					" └──────────────────────┘",
					"",
					"",
					"",
				}
			}

			if m.showVisualizer {
				m.pm.mu.Lock()
				isPaused := m.pm.IsPaused || m.pm.mpvCmd == nil
				m.pm.mu.Unlock()
				heights := getDetailedEqualizerHeights(isPaused)
				artLines = append(artLines,
					"",
					lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render("  [EQUALIZER]"),
					lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Render(getDetailedEqualizerRow(3, heights)),
					lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Render(getDetailedEqualizerRow(2, heights)),
					lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Render(getDetailedEqualizerRow(1, heights)),
					lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Render(getDetailedEqualizerRow(0, heights)),
				)
			}

			rightWidth := m.width - 22
			for i := 0; i < listHeight; i++ {
				var line string
				if rightWidth >= 54 {
					// Side-by-side: Art (25 chars) + Separator (3 chars) + Lyrics
					artLine := "                         " // 25 spaces
					if i < len(artLines) {
						artLine = fmt.Sprintf("%-25s", artLines[i])
					}

					lyricLine := ""
					lyricIdx := i + m.lyricsScrollOffset
					if lyricIdx >= 0 && lyricIdx < len(lyricsLines) {
						lyricLine = lyricsLines[lyricIdx]
					}

					// Trim lyric line if it's too long
					maxLyricWidth := rightWidth - 29
					if maxLyricWidth < 10 {
						maxLyricWidth = 10
					}
					if len(lyricLine) > maxLyricWidth {
						lyricLine = lyricLine[:maxLyricWidth-3] + "..."
					}

					line = fmt.Sprintf("%s │ %s", artLine, lyricLine)
				} else {
					// Lyrics only
					lyricLine := ""
					lyricIdx := i + m.lyricsScrollOffset
					if lyricIdx >= 0 && lyricIdx < len(lyricsLines) {
						lyricLine = lyricsLines[lyricIdx]
					}
					if len(lyricLine) > rightWidth {
						lyricLine = lyricLine[:rightWidth-3] + "..."
					}
					line = lyricLine
				}
				rightLines = append(rightLines, line)
			}
		}
	} else if m.showInfoCard {
		headerTitle = "ℹ️  Track Metadata Details"
		var t *Track
		switch m.rightPaneState {
		case "all_songs":
			if m.rightIdx < len(m.allSongsTracks) {
				t = &m.allSongsTracks[m.rightIdx]
			}
		case "playlist_tracks":
			if m.rightIdx < len(m.playlistTracks) {
				t = &m.playlistTracks[m.rightIdx]
			}
		case "artist_tracks":
			if m.rightIdx < len(m.artistTracks) {
				t = &m.artistTracks[m.rightIdx]
			}
		case "album_tracks":
			if m.rightIdx < len(m.albumTracks) {
				t = &m.albumTracks[m.rightIdx]
			}
		case "search":
			if m.rightIdx < len(m.filteredTracks) {
				t = &m.filteredTracks[m.rightIdx]
			}
		}

		if t != nil {
			filledStars := t.Rating / 20
			stars := strings.Repeat("★", filledStars) + strings.Repeat("☆", 5-filledStars)
			genreStr := t.Genre
			if genreStr == "" {
				genreStr = "Unknown"
			}
			yearStr := strconv.Itoa(t.Year)
			if t.Year == 0 {
				yearStr = "Unknown"
			}
			trackNumStr := strconv.Itoa(t.TrackNumber)
			if t.TrackNumber == 0 {
				trackNumStr = "Unknown"
			}

			details := []string{
				"",
				fmt.Sprintf("  Title:        %s", t.Title),
				fmt.Sprintf("  Artist:       %s", t.Artist),
				fmt.Sprintf("  Album:        %s", t.Album),
				fmt.Sprintf("  Genre:        %s", genreStr),
				fmt.Sprintf("  Year:         %s", yearStr),
				fmt.Sprintf("  Track No:     %s", trackNumStr),
				fmt.Sprintf("  Duration:     %s", formatTime(t.Duration)),
				fmt.Sprintf("  Play Count:   %d", t.PlayedCount),
				fmt.Sprintf("  Rating:       %s", stars),
				fmt.Sprintf("  Database ID:  %d", t.DBID),
				fmt.Sprintf("  File Path:    %s", t.Path),
				"",
				"  [Press 'i' or 'Esc' or click anywhere to close]",
			}

			rightWidth := m.width - 25
			for _, line := range details {
				if len(line) > rightWidth {
					line = line[:rightWidth-4] + "..."
				}
				rightLines = append(rightLines, line)
			}
		}
	} else {
		switch m.rightPaneState {
		case "playlists":
			headerTitle = fmt.Sprintf("📂 Playlists (%d)", len(m.playlistsList))
			for i := m.rightScrollOffset; i < len(m.playlistsList) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderCategoryLine(i, m.playlistsList[i]))
			}
		case "artists":
			headerTitle = fmt.Sprintf("👥 Artists (%d)", len(m.artistsList))
			for i := m.rightScrollOffset; i < len(m.artistsList) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderCategoryLine(i, m.artistsList[i]))
			}
		case "albums":
			headerTitle = fmt.Sprintf("💿 Albums (%d)", len(m.albumsList))
			for i := m.rightScrollOffset; i < len(m.albumsList) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderCategoryLine(i, m.albumsList[i]))
			}
		case "all_songs":
			headerTitle = fmt.Sprintf("🎵 All Songs (%d)", len(m.allSongsTracks))
			for i := m.rightScrollOffset; i < len(m.allSongsTracks) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderTrackLine(i, m.allSongsTracks[i]))
			}
		case "playlist_tracks":
			headerTitle = fmt.Sprintf("📂 Playlist: %s (%d)", m.selectedPlaylist, len(m.playlistTracks))
			for i := m.rightScrollOffset; i < len(m.playlistTracks) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderTrackLine(i, m.playlistTracks[i]))
			}
		case "artist_tracks":
			headerTitle = fmt.Sprintf("👥 Tracks by: %s (%d)", m.selectedArtist, len(m.artistTracks))
			for i := m.rightScrollOffset; i < len(m.artistTracks) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderTrackLine(i, m.artistTracks[i]))
			}
		case "album_tracks":
			headerTitle = fmt.Sprintf("💿 Tracks in: %s (%d)", m.selectedAlbum, len(m.albumTracks))
			for i := m.rightScrollOffset; i < len(m.albumTracks) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderTrackLine(i, m.albumTracks[i]))
			}
		case "search":
			headerTitle = fmt.Sprintf("🔎 Search matches for '%s' (%d)", m.searchQuery, len(m.filteredTracks))
			for i := m.rightScrollOffset; i < len(m.filteredTracks) && i < m.rightScrollOffset+listHeight; i++ {
				rightLines = append(rightLines, m.renderTrackLine(i, m.filteredTracks[i]))
			}
		}
	}

	// Pad right pane to fill list height
	for len(rightLines) < listHeight {
		rightLines = append(rightLines, "")
	}

	// Build the center split pane view
	// Create Right Pane Header
	sortText := ""
	if m.sortMode != "default" && m.getActiveTrackSlice() != nil {
		modeName := ""
		switch m.sortMode {
		case "title":
			modeName = "Title 🔤"
		case "artist":
			modeName = "Artist 👥"
		case "rating":
			modeName = "Rating ⭐"
		case "play_count":
			modeName = "Play Count 🔄"
		}
		sortText = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Render(" | Sorted by: " + modeName)
	}

	rightHeader := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render(headerTitle)
	if sortText != "" {
		rightHeader = rightHeader + sortText
	}
	if time.Since(m.rateNotifyTime) < 3*time.Second {
		rightHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Bold(true).Render(fmt.Sprintf("⚡ Rating synced to %s!", m.activeApp))
	}
	if time.Since(m.themeNotifyTime) < 2*time.Second {
		rightHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Bold(true).Render(fmt.Sprintf("🎨 Theme switched to: %s", theme.Name))
	}
	if time.Since(m.downloadNotifyTime) < 4*time.Second {
		if m.downloadStatus == "downloading" {
			rightHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Bold(true).Render(fmt.Sprintf("📥 Downloading: %s...", m.downloadingTrack))
		} else if m.downloadStatus == "success" {
			rightHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Bold(true).Render(fmt.Sprintf("✅ Downloaded to %s!", m.cfg.DestDir))
		} else if m.downloadStatus == "error" {
			rightHeader = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.MutedColor)).Bold(true).Render("❌ Download failed!")
		}
	}

	var paneRows []string
	rightWidth := m.width - sidebarPaneWidth - 1
	if rightWidth < 0 {
		rightWidth = 0
	}
	paneRows = append(paneRows, strings.Repeat(" ", sidebarPaneWidth)+"│ "+rightHeader)
	borderLine := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render(
		strings.Repeat("─", sidebarPaneWidth) + "┼" + strings.Repeat("─", rightWidth),
	)
	paneRows = append(paneRows, borderLine)

	for i := 0; i < listHeight; i++ {
		sidebarCell := sidebarLines[i]
		rightCell := ""
		if i < len(rightLines) {
			rightCell = rightLines[i]
		}
		paneRows = append(paneRows, sidebarCell+" │ "+rightCell)
	}

	contentView := strings.Join(paneRows, "\n") + "\n"

	// 4. Playbar (Progress & Status)
	playbarSep := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render(strings.Repeat("─", m.width)) + "\n"

	var playbarRow1, playbarRow2 string
	m.pm.mu.Lock()
	track := m.pm.CurrentTrack
	posVal := m.pm.Position
	durVal := m.pm.Duration
	isPaused := m.pm.IsPaused
	volume := m.pm.Volume
	isMuted := m.pm.IsMuted
	m.pm.mu.Unlock()

	if track != nil {
		filledStars := track.Rating / 20
		stars := strings.Repeat("★", filledStars) + strings.Repeat("☆", 5-filledStars)

		trackInfo := fmt.Sprintf("Now Playing: %s - %s", track.Title, track.Artist)

		repeatBadge := ""
		if m.pm.RepeatMode == "one" {
			repeatBadge = " [🔂 One]"
		} else if m.pm.RepeatMode == "all" {
			repeatBadge = " [🔁 All]"
		}
		shuffleBadge := ""
		if m.pm.IsShuffled {
			shuffleBadge = " [🔀 Shuffle]"
		}
		ratingText := fmt.Sprintf("[%s]%s%s", stars, repeatBadge, shuffleBadge)

		maxTrackInfoWidth := m.width - len(ratingText) - 5
		if m.showVisualizer {
			maxTrackInfoWidth -= 8 // space for compact visualizer
		}
		if len(trackInfo) > maxTrackInfoWidth {
			trackInfo = trackInfo[:maxTrackInfoWidth] + "..."
		}

		visualWidth := lipgloss.Width(trackInfo)
		if m.showVisualizer {
			visualWidth += 8 // "  " + 6-char visualizer
		}
		padding := m.width - visualWidth - len(ratingText) - 1
		if padding < 0 {
			padding = 0
		}

		playbarRow1 = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Bold(true).Render(trackInfo)
		if m.showVisualizer {
			playbarRow1 += "  " + m.getCompactEqualizer()
		}
		playbarRow1 += strings.Repeat(" ", padding) +
			lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Render(ratingText)

		posStr := formatTime(posVal)
		durStr := formatTime(durVal)

		volBar := ""
		if isMuted {
			volBar = "░░░░░░░░░░"
		} else {
			filledVol := volume / 10
			if filledVol > 10 {
				filledVol = 10
			}
			if filledVol < 0 {
				filledVol = 0
			}
			volBar = strings.Repeat("█", filledVol) + strings.Repeat("░", 10-filledVol)
		}

		var volText string
		if isMuted {
			volText = "Vol: [░░░░░░░░░░] " + lipgloss.NewStyle().Foreground(lipgloss.Color(theme.MutedColor)).Bold(true).Render("MUTED 🔇")
		} else {
			volText = fmt.Sprintf("Vol: [%s] %d%%", volBar, volume)
		}

		volVisualWidth := lipgloss.Width(volText)
		barWidth := m.width - 24 - volVisualWidth
		if barWidth < 5 {
			barWidth = 5
		}
		filledWidth := 0
		if durVal > 0 {
			filledWidth = int(float64(barWidth) * (posVal / durVal))
		}
		if filledWidth > barWidth {
			filledWidth = barWidth
		}
		if filledWidth < 0 {
			filledWidth = 0
		}

		barChar := "█"
		emptyChar := "░"
		progressBar := strings.Repeat(barChar, filledWidth) + strings.Repeat(emptyChar, barWidth-filledWidth)

		prefix := "▶"
		if isPaused {
			prefix = "⏸"
		}
		if m.showSortPrompt {
			playbarRow2 = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Bold(true).Render("👉 Sort tracks: [1] Default  [2] Title  [3] Artist  [4] Rating  [5] Play Count")
		} else if m.showLyrics {
			scrollStatus := "Off"
			if m.autoScroll {
				scrollStatus = "On"
			}
			playbarRow2 = fmt.Sprintf("Use [↑/↓] to scroll. [a] Auto-Scroll: %s. [l] Close Lyrics. [Space] Play/Pause. [n/p] Skip.", scrollStatus)
		} else {
			playbarRow2 = fmt.Sprintf("%s [%s] %s / %s (%s)", prefix, progressBar, posStr, durStr, volText)
		}
	} else {
		playbarRow1 = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render("⏹️  Playback Stopped")
		if m.showSortPrompt {
			playbarRow2 = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.InfoColor)).Bold(true).Render("👉 Sort tracks: [1] Default  [2] Title  [3] Artist  [4] Rating  [5] Play Count")
		} else {
			playbarRow2 = "Use [←/→] to browse. [/] Search. [o] Sort. [l] Lyrics. [r] Repeat. [z] Shuffle. [Enter] Play."
		}
	}

	// Dynamic metadata preview for highlighted track
	var highlightedTrack *Track
	if m.focus == "right" {
		switch m.rightPaneState {
		case "all_songs":
			if m.rightIdx < len(m.allSongsTracks) {
				highlightedTrack = &m.allSongsTracks[m.rightIdx]
			}
		case "playlist_tracks":
			if m.rightIdx < len(m.playlistTracks) {
				highlightedTrack = &m.playlistTracks[m.rightIdx]
			}
		case "artist_tracks":
			if m.rightIdx < len(m.artistTracks) {
				highlightedTrack = &m.artistTracks[m.rightIdx]
			}
		case "album_tracks":
			if m.rightIdx < len(m.albumTracks) {
				highlightedTrack = &m.albumTracks[m.rightIdx]
			}
		case "search":
			if m.rightIdx < len(m.filteredTracks) {
				highlightedTrack = &m.filteredTracks[m.rightIdx]
			}
		}
	}

	var metaLine string
	if highlightedTrack != nil {
		filledStars := highlightedTrack.Rating / 20
		stars := strings.Repeat("★", filledStars) + strings.Repeat("☆", 5-filledStars)
		albumStr := highlightedTrack.Album
		if albumStr == "" {
			albumStr = "Unknown Album"
		}
		metaText := fmt.Sprintf("🔍 Info: %s - %s (Album: %s)  [%s]", highlightedTrack.Title, highlightedTrack.Artist, albumStr, stars)
		if len(metaText) > m.width {
			metaText = metaText[:m.width-4] + "..."
		}
		metaLine = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.MetaColor)).Render(metaText) + "\n"
	} else {
		metaLine = "\n"
	}

	// 5. Legend footer
	var legend string
	if m.isSearching {
		legend = fmt.Sprintf("🔎 Search: %-25s  [↑/↓] Select Track  [Enter] Confirm & Browse  [Esc] Cancel Search", m.searchQuery)
	} else {
		legend = "[Space] Pause  [S] Stop  [N] Next  [P] Prev  [1-5] Rate  [+/-] Vol  [/] Search  [R] Repeat  [Z] Shuffle  [D] Download  [Q] Quit"
	}
	legendLine := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render(strings.Repeat("─", m.width)) + "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color(theme.BorderColor)).Render(legend)

	return header + separator + contentView + playbarSep + metaLine + playbarRow1 + "\n" + playbarRow2 + "\n" + legendLine
}

func (m model) renderCategoryLine(idx int, category string) string {
	theme := m.currentTheme()
	lineText := fmt.Sprintf("  👉 %s", category)
	rightWidth := m.width - 25
	if len(lineText) > rightWidth {
		lineText = lineText[:rightWidth] + "..."
	}

	if m.focus == "right" && idx == m.rightIdx {
		return lipgloss.NewStyle().Background(lipgloss.Color(theme.HighlightBG)).Foreground(lipgloss.Color(theme.HighlightFG)).Render(fmt.Sprintf("%-*s", m.width-23, lineText))
	}
	return lineText
}

func (m model) renderTrackLine(idx int, track Track) string {
	m.pm.mu.Lock()
	isPlaying := m.pm.CurrentTrack != nil && m.pm.CurrentTrack.Path == track.Path
	isPaused := m.pm.IsPaused
	m.pm.mu.Unlock()

	prefix := "  "
	if isPlaying {
		if isPaused {
			prefix = "⏸ "
		} else {
			prefix = "▶ "
		}
	}

	trackInfo := fmt.Sprintf("%s%2d. %s - %s", prefix, idx+1, track.Title, track.Artist)
	rightWidth := m.width - 25
	if rightWidth < 15 {
		rightWidth = 15
	}

	filledStars := track.Rating / 20
	var ratingText string
	if filledStars > 0 {
		ratingText = strings.Repeat("★", filledStars) + strings.Repeat("☆", 5-filledStars)
	}

	var lineText string
	if ratingText != "" {
		maxInfoWidth := rightWidth - len(ratingText) - 2
		if len(trackInfo) > maxInfoWidth {
			if maxInfoWidth > 5 {
				trackInfo = trackInfo[:maxInfoWidth-3] + "..."
			} else {
				trackInfo = trackInfo[:maxInfoWidth]
			}
		}
		padding := rightWidth - len(trackInfo) - len(ratingText)
		if padding < 0 {
			padding = 0
		}
		lineText = trackInfo + strings.Repeat(" ", padding) + ratingText
	} else {
		if len(trackInfo) > rightWidth {
			trackInfo = trackInfo[:rightWidth-3] + "..."
		}
		lineText = trackInfo
	}

	if m.focus == "right" && idx == m.rightIdx {
		theme := m.currentTheme()
		return lipgloss.NewStyle().Background(lipgloss.Color(theme.HighlightBG)).Foreground(lipgloss.Color(theme.HighlightFG)).Render(fmt.Sprintf("%-*s", m.width-23, lineText))
	}
	return lineText
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func formatTime(seconds float64) string {
	t := time.Duration(seconds) * time.Second
	h := t / time.Hour
	t -= h * time.Hour
	m := t / time.Minute
	t -= m * time.Minute
	s := t / time.Second
	return fmt.Sprintf("%02d:%02d", m, s)
}

func (m *model) updateTrackRatingInLibrary(path string, rating int) {
	if m.library == nil {
		return
	}
	for i := range m.library.Tracks {
		if m.library.Tracks[i].Path == path {
			m.library.Tracks[i].Rating = rating
		}
	}
	for i := range m.allSongsTracks {
		if m.allSongsTracks[i].Path == path {
			m.allSongsTracks[i].Rating = rating
		}
	}
	for i := range m.playlistTracks {
		if m.playlistTracks[i].Path == path {
			m.playlistTracks[i].Rating = rating
		}
	}
	for i := range m.artistTracks {
		if m.artistTracks[i].Path == path {
			m.artistTracks[i].Rating = rating
		}
	}
	for i := range m.albumTracks {
		if m.albumTracks[i].Path == path {
			m.albumTracks[i].Rating = rating
		}
	}
	for i := range m.filteredTracks {
		if m.filteredTracks[i].Path == path {
			m.filteredTracks[i].Rating = rating
		}
	}
}

func (m *model) getActiveTrackSlice() *[]Track {
	switch m.rightPaneState {
	case "all_songs":
		return &m.allSongsTracks
	case "playlist_tracks":
		return &m.playlistTracks
	case "artist_tracks":
		return &m.artistTracks
	case "album_tracks":
		return &m.albumTracks
	case "search":
		return &m.filteredTracks
	default:
		return nil
	}
}

func (m *model) applySort() {
	slicePtr := m.getActiveTrackSlice()
	if slicePtr == nil {
		return
	}

	// 1. Capture currently highlighted track to restore position
	var selectedPath string
	if m.rightIdx < len(*slicePtr) {
		selectedPath = (*slicePtr)[m.rightIdx].Path
	}

	// 2. First, restore to default order
	switch m.rightPaneState {
	case "all_songs":
		m.allSongsTracks = append([]Track(nil), m.library.Tracks...)
	case "playlist_tracks":
		indices := m.library.Playlists[m.selectedPlaylist]
		m.playlistTracks = nil
		for _, idx := range indices {
			if idx < len(m.library.Tracks) {
				m.playlistTracks = append(m.playlistTracks, m.library.Tracks[idx])
			}
		}
	case "artist_tracks":
		m.artistTracks = nil
		for _, t := range m.library.Tracks {
			if t.Artist == m.selectedArtist {
				m.artistTracks = append(m.artistTracks, t)
			}
		}
	case "album_tracks":
		m.albumTracks = nil
		for _, t := range m.library.Tracks {
			if t.Album == m.selectedAlbum {
				m.albumTracks = append(m.albumTracks, t)
			}
		}
	case "search":
		m.performSearch()
	}

	// 3. Sort if a non-default mode is selected
	if m.sortMode != "default" {
		sortTracks(*slicePtr, m.sortMode)
	}

	// 4. Restore cursor position to follow the selected track
	if selectedPath != "" {
		for i, t := range *slicePtr {
			if t.Path == selectedPath {
				m.rightIdx = i
				break
			}
		}
	}
}

func sortTracks(tracks []Track, mode string) {
	switch mode {
	case "title":
		sort.Slice(tracks, func(i, j int) bool {
			return strings.ToLower(tracks[i].Title) < strings.ToLower(tracks[j].Title)
		})
	case "artist":
		sort.Slice(tracks, func(i, j int) bool {
			a1, a2 := strings.ToLower(tracks[i].Artist), strings.ToLower(tracks[j].Artist)
			if a1 != a2 {
				return a1 < a2
			}
			al1, al2 := strings.ToLower(tracks[i].Album), strings.ToLower(tracks[j].Album)
			if al1 != al2 {
				return al1 < al2
			}
			return tracks[i].TrackNumber < tracks[j].TrackNumber
		})
	case "rating":
		sort.Slice(tracks, func(i, j int) bool {
			if tracks[i].Rating != tracks[j].Rating {
				return tracks[i].Rating > tracks[j].Rating // descending
			}
			return strings.ToLower(tracks[i].Title) < strings.ToLower(tracks[j].Title)
		})
	case "play_count":
		sort.Slice(tracks, func(i, j int) bool {
			if tracks[i].PlayedCount != tracks[j].PlayedCount {
				return tracks[i].PlayedCount > tracks[j].PlayedCount // descending
			}
			return strings.ToLower(tracks[i].Title) < strings.ToLower(tracks[j].Title)
		})
	}
}

func getEqualizerBar(barIdx int, isPaused bool) string {
	segments := []string{" ", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	if isPaused {
		return "▃"
	}
	t := time.Now().UnixNano() / int64(time.Millisecond*100)
	val := (t + int64(barIdx*3)) % 8
	return segments[val]
}

func (m model) getCompactEqualizer() string {
	m.pm.mu.Lock()
	isPaused := m.pm.IsPaused || m.pm.mpvCmd == nil
	m.pm.mu.Unlock()

	var sb strings.Builder
	for i := 0; i < 6; i++ {
		sb.WriteString(getEqualizerBar(i, isPaused))
	}
	theme := m.currentTheme()
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.EqualizerColor)).Render(sb.String())
}

func getDetailedEqualizerHeights(isPaused bool) []float64 {
	heights := make([]float64, 8)
	if isPaused {
		for i := range heights {
			heights[i] = 1.0
		}
		return heights
	}
	t := float64(time.Now().UnixNano()/int64(time.Millisecond*50)) / 2.0
	for i := 0; i < 8; i++ {
		val := 2.0 + 1.8*math.Sin(t+float64(i)*0.8) + 0.3*math.Cos(t*1.7-float64(i)*1.3)
		if val < 0.1 {
			val = 0.1
		}
		if val > 4.0 {
			val = 4.0
		}
		heights[i] = val
	}
	return heights
}

func getDetailedEqualizerRow(rowY int, barHeights []float64) string {
	segments := []string{" ", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
	var sb strings.Builder
	sb.WriteString("   ")
	for _, h := range barHeights {
		if h >= float64(rowY+1) {
			sb.WriteString("█ ")
		} else if h <= float64(rowY) {
			sb.WriteString("  ")
		} else {
			fraction := h - float64(rowY)
			idx := int(fraction * 8)
			if idx < 0 {
				idx = 0
			}
			if idx > 7 {
				idx = 7
			}
			sb.WriteString(segments[idx] + " ")
		}
	}
	return sb.String()
}

type downloadResultMsg struct {
	err       error
	trackName string
}

func (m model) downloadActiveTrackCmd() (model, tea.Cmd) {
	if m.pm.CurrentTrack == nil {
		return m, nil
	}
	track := *m.pm.CurrentTrack
	m.downloadingTrack = track.Title
	m.downloadStatus = "downloading"
	m.downloadNotifyTime = time.Now()

	cmd := func() tea.Msg {
		m.pm.mu.Lock()
		controlPath := m.pm.ControlPath
		target := m.pm.SSHHost
		if m.pm.SSHUser != "" {
			target = m.pm.SSHUser + "@" + m.pm.SSHHost
		}
		m.pm.mu.Unlock()

		localDest := m.cfg.DestDir
		if strings.HasPrefix(localDest, "~") {
			home, err := os.UserHomeDir()
			if err == nil {
				localDest = filepath.Join(home, localDest[1:])
			}
		}

		os.MkdirAll(localDest, 0755)

		scpCmd := exec.Command("scp", "-o", "ControlPath="+controlPath, fmt.Sprintf("%s:%s", target, track.Path), localDest)
		err := scpCmd.Run()
		return downloadResultMsg{err: err, trackName: track.Title}
	}
	return m, cmd
}
