package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func printHelp() {
	fmt.Println("Usage: itunespull [flags] [destination_directory]")
	fmt.Println("\nRunning without flags launches the interactive Terminal UI (TUI) player.")
	fmt.Println("\nFlags:")
	fmt.Println("  -d, --download, -s, --search        Launch the standard CLI search/download tool (disables TUI)")
	fmt.Println("  -c, --edit-config                   Setup or update connection configuration")
	fmt.Println("  -v, --video                         Enable mpv video window for album art")
	fmt.Println("  -h, --help                          Show this help message")
	fmt.Println("\nArguments:")
	fmt.Println("  destination_directory               Target folder for downloaded tracks (defaults to ~/Downloads)")
	fmt.Println("\nTUI Controls (Playback Mode):")
	fmt.Println("  [Space]       Play / Pause")
	fmt.Println("  [n] / [p]     Next / Previous Track")
	fmt.Println("  [+/-]         Adjust volume")
	fmt.Println("  [m]           Mute / Unmute")
	fmt.Println("  [l]           Toggle lyrics view")
	fmt.Println("  [a]           Toggle lyrics auto-scroll")
	fmt.Println("  [v]           Toggle bouncing equalizer visualizer")
	fmt.Println("  [t]           Cycle color themes")
	fmt.Println("  [z]           Toggle track shuffle")
	fmt.Println("  [r]           Cycle track repeat modes")
	fmt.Println("  [d]           Download currently playing track")
	fmt.Println("  [1]-[5]       Rate current track")
	fmt.Println("  [q]           Quit the player")
}

func main() {
	// Parse CLI arguments/flags manually because Go's default flag package can be restrictive
	// with combined short flags or arguments.
	isTuiMode := true // Default to TUI mode
	var editConfig bool
	var showHelp bool
	var videoFlag bool
	var destDir string

	args := os.Args[1:]
	var cleanArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			showHelp = true
		} else if arg == "-d" || arg == "--download" || arg == "-s" || arg == "--search" || arg == "--cli" {
			isTuiMode = false
		} else if arg == "-c" || arg == "--edit-config" {
			editConfig = true
		} else if arg == "-v" || arg == "--video" {
			videoFlag = true
		} else {
			cleanArgs = append(cleanArgs, arg)
		}
	}

	if showHelp {
		printHelp()
		return
	}

	// 1. Config Loading or Wizard
	cfg, err := loadConfig()
	if err != nil || editConfig {
		if err != nil && !editConfig {
			fmt.Println("⚠️  Configuration file not found. Let's configure it first.")
		}
		cfg, err = runConfigWizard()
		if err != nil {
			fmt.Printf("❌ Configuration error: %v\n", err)
			os.Exit(1)
		}
		if editConfig {
			return
		}
	}

	cfg.VideoEnabled = cfg.VideoEnabled || videoFlag

	// Check destination folder for download mode
	if len(cleanArgs) > 0 {
		destDir = cleanArgs[0]
	} else {
		destDir = cfg.DestDir
	}

	// Expand ~/ just in case
	if strings.HasPrefix(destDir, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			destDir = filepath.Join(home, destDir[1:])
		}
	}

	if !isTuiMode {
		// Verify standard download dir exists
		if _, err := os.Stat(destDir); os.IsNotExist(err) {
			fmt.Printf("❌ Error: Destination directory '%s' does not exist.\n", destDir)
			os.Exit(1)
		}
	}

	// 2. Check if mpv is installed
	_, mpvErr := exec.LookPath("mpv")
	if isTuiMode && mpvErr != nil {
		fmt.Println("❌ Error: mpv is not installed or not found in your PATH. mpv is required for TUI streaming.")
		os.Exit(1)
	}

	// 3. Establish SSH Connection
	controlPath := getControlPath(cfg)
	fmt.Printf("🔑 Connecting to remote iTunes Mac (%s) via SSH...\n", cfg.SSHHost)
	fmt.Println("Please enter your password if prompted:")

	if err := establishSSHConnection(cfg, controlPath); err != nil {
		fmt.Printf("❌ Error connecting: %v\n", err)
		os.Exit(1)
	}

	// Register clean-up on exit
	defer cleanupSSHConnection(cfg, controlPath)

	// 4. Detect running App (iTunes or Music)
	activeApp, err := detectActiveApp(cfg, controlPath)
	if err != nil {
		fmt.Printf("⚠️  Warning: %v\n", err)
		fmt.Println("Please open iTunes or Music on the remote Mac and try again.")
		return
	}

	if !isTuiMode {
		fmt.Printf("✅ Connected to remote %s! Downloads will save to: %s\n\n", activeApp, destDir)
		runDownloadCLI(cfg, controlPath, activeApp, destDir, mpvErr == nil)
	} else {
		// Run TUI Player Mode
		fmt.Println("⏳ Fetching iTunes Library...")
		lib, err := loadLibraryCache(cfg, controlPath, activeApp, false)
		if err != nil {
			fmt.Printf("❌ Error loading library: %v\n", err)
			return
		}

		p := tea.NewProgram(initialModel(cfg, controlPath, activeApp, lib), tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Printf("❌ Terminal UI error: %v\n", err)
			os.Exit(1)
		}
	}
}

func runDownloadCLI(cfg *Config, controlPath string, activeApp string, destDir string, hasMpv bool) {
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("🔍 Enter search term (song, album, or artist) [or 'q' to quit]: ")
		query, _ := reader.ReadString('\n')
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		if query == "q" || query == "quit" || query == "exit" {
			break
		}

		fmt.Println("Searching...")
		tracks, err := searchiTunes(cfg, controlPath, activeApp, query)
		if err != nil {
			fmt.Printf("❌ Search failed: %v\n\n", err)
			continue
		}

		if len(tracks) == 0 {
			fmt.Println("❌ No matching local files found in remote library.\n")
			continue
		}

		fmt.Printf("\n✨ Found %d matching file(s):\n", len(tracks))
		for idx, t := range tracks {
			fmt.Printf(" [%2d] \x1b[96m%s\x1b[0m by \x1b[93m%s\x1b[0m  (Album: %s)\n", idx+1, t.Title, t.Artist, t.Album)
		}

		fmt.Println("\nSelection Options:")
		fmt.Println(" - Enter numbers to download (e.g., '1', '1,3,7', '1-5')")
		fmt.Println(" - Enter 'all' to download everything listed")
		if hasMpv {
			fmt.Println(" - Prefix with 'p' or 'play' to stream locally (e.g., 'p 1', 'play 1,3', 'p all')")
		} else {
			fmt.Println(" - (Streaming is disabled: 'mpv' not found in your PATH)")
		}
		fmt.Println(" - Enter 'c' to cancel search and try again")

		fmt.Print("\n👉 Select option: ")
		selectionInput, _ := reader.ReadString('\n')
		selectionInput = strings.TrimSpace(selectionInput)
		if selectionInput == "c" {
			fmt.Println()
			continue
		}

		isStream := false
		if strings.HasPrefix(strings.ToLower(selectionInput), "p ") {
			isStream = true
			selectionInput = strings.TrimSpace(selectionInput[2:])
		} else if strings.HasPrefix(strings.ToLower(selectionInput), "play ") {
			isStream = true
			selectionInput = strings.TrimSpace(selectionInput[5:])
		}

		selectedIndices := parseSelections(selectionInput, len(tracks))
		if len(selectedIndices) == 0 {
			fmt.Println("⚠️  Invalid selection. Try again.\n")
			continue
		}

		if isStream {
			if !hasMpv {
				fmt.Println("❌ Error: mpv is not installed or not found in your PATH. Cannot stream.\n")
				continue
			}

			fmt.Printf("\n🎵 Streaming %d song(s) locally...\n", len(selectedIndices))
			fmt.Println("💡 Controls: Press Ctrl+C in terminal to stop streaming / skip song.")

			target := cfg.SSHHost
			if cfg.SSHUser != "" {
				target = cfg.SSHUser + "@" + cfg.SSHHost
			}

			for i, idx := range selectedIndices {
				track := tracks[idx-1]
				fmt.Printf("\n▶️  [%d/%d] Playing: \x1b[92m%s - %s\x1b[0m (Album: %s)\n", i+1, len(selectedIndices), track.Artist, track.Title, track.Album)

				escapedPath := strings.ReplaceAll(strings.ReplaceAll(track.Path, "\\", "\\\\"), "\"", "\\\"")
				
				sshCmd := exec.Command("ssh", "-o", "ControlPath="+controlPath, target, fmt.Sprintf(`cat "%s"`, escapedPath))
				var mpvArgs []string
				if !cfg.VideoEnabled {
					mpvArgs = append(mpvArgs, "--no-video")
				}
				mpvArgs = append(mpvArgs, "-")
				mpvCmd := exec.Command("mpv", mpvArgs...)

				pr, pw := io.Pipe()
				sshCmd.Stdout = pw
				mpvCmd.Stdin = pr

				sshCmd.Start()
				mpvCmd.Start()

				go func() {
					sshCmd.Wait()
					pw.Close()
				}()

				// Wait for mpv to finish playing (or user to Ctrl+C)
				mpvCmd.Wait()
				
				// Kill SSH if mpv stopped first
				if sshCmd.Process != nil {
					sshCmd.Process.Kill()
				}
			}
			fmt.Println("\n🎉 Playlist complete!\n")
		} else {
			fmt.Printf("\n🚀 Downloading %d song(s) to '%s'...\n", len(selectedIndices), destDir)
			
			var selectedTracks []Track
			for _, idx := range selectedIndices {
				selectedTracks = append(selectedTracks, tracks[idx-1])
			}

			downloadTracks(cfg, controlPath, selectedTracks, destDir, func(count int, name string, success bool, errStr string) {
				if success {
					fmt.Printf(" [%d/%d] Downloading: %s ... \x1b[92mDone!\x1b[0m\n", count, len(selectedIndices), name)
				} else if errStr != "" {
					fmt.Printf(" [%d/%d] Downloading: %s ... \x1b[91mFailed!\x1b[0m (Error: %s)\n", count, len(selectedIndices), name, errStr)
				} else {
					// Just logging progress initiation
					fmt.Printf(" [%d/%d] Downloading: %s ... ", count, len(selectedIndices), name)
				}
			})
			fmt.Println("\n🎉 Downloads complete!\n")
		}
	}
}
