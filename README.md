# itunespull

`itunespull` is a command-line terminal player and downloader that allows you to stream and download music from a remote Mac running iTunes or Apple Music over an SSH connection.

This Go-based edition replaces the legacy Python script with a single compiled binary, featuring an interactive split-pane terminal UI built using the **Bubble Tea** framework.

---

<img width="1541" height="1232" alt="demo" src="https://github.com/user-attachments/assets/10163344-a8bf-47b8-864a-4b4dd77932ad" />


## Features

* **Interactive TUI Player (`-t` / `--tui`):** A modern, smooth, flicker-free terminal interface.
  * Browse your music library by **Playlists**, **Artists**, **Albums**, or **All Songs**.
  * Dynamic, live filtering and searching by pressing `/`.
  * Standard playback controls: Pause/Play, Volume adjustment, Skipping, and Seeking.
  * Real-time bidirectional syncing: Automatically updates **play counts** and lets you update **star ratings** (1-5 keys) directly on the remote Mac's database.
* **Standard CLI Mode:** A search-and-download terminal loop for single or bulk downloads and streams.
* **Setup Wizard:** Automatically prompts for connection configurations on initial startup.
* **Config Editor (`-c` / `--edit-config`):** Easily edit your saved settings at any time.

---

## Installation

To install `itunespull` on macOS or Linux, use **Homebrew**:

```bash
# 1. Tap the repository
brew tap DrewThomasson/homebrew-tap

# 2. Install itunespull
brew install itunespull
```

*Note:* Streaming audio locally requires `mpv` to be installed on your client machine:
```bash
brew install mpv
```

---

## TUI Controls

* **Navigation:**
  * **Up / Down Arrows:** Scroll up and down in lists (sidebar or right pane).
  * **Right Arrow / Enter:** 
    * If on the sidebar: Focus the right pane.
    * If on a Playlist/Artist/Album: Enter the item to view its tracks.
    * If on a Song: Play the song.
  * **Left Arrow:** 
    * Go back one level (e.g., from tracks list back to playlists/artists/albums list, keeping your position).
    * If already at the top-level list: Move focus back to the left sidebar.
* **Playback:**
  * **Spacebar:** Toggle Play/Pause.
  * **`n` / `N`:** Play next song.
  * **`p` / `P`:** Play previous song.
  * **`+` / `=`:** Increase volume.
  * **`-` / `_`:** Decrease volume.
  * **`m` / `M`:** Toggle audio Mute/Unmute.
  * **`v` / `V`:** Toggle simulated bouncing audio equalizer.
  * **`z` / `Z`:** Toggle queue shuffle mode.
  * **`r` / `R`:** Cycle queue repeat modes (Off, One, All).
* **Seeking:**
  * **Shift + Right Arrow** or **`]`**: Seek forward 5 seconds (hold down to scan forward).
  * **Shift + Left Arrow** or **`[`**: Seek backward 5 seconds (hold down to scan backward).
* **Library & Sync:**
  * **`1` to `5`:** Rate the currently playing song (1 to 5 stars) and sync to the remote Mac.
  * **`/`:** Search/Filter tracks.
  * **`o` / `O`:** Open sorting options prompt for the active track list.
  * **`i` / `I`:** View track metadata details overlay (press `i` or `esc` to close).
  * **`l` / `L`:** View/Hide lyrics.
  * **`a` / `A`:** Toggle auto-scrolling lyrics (when lyrics view is open).
  * **`t` / `T`:** Cycle visual color themes (Cyberpunk, iTunes, Matrix, Midnight).
  * **`d` / `D`:** Download the currently playing track in the background.
  * **`q` / `Ctrl+C`:** Quit the application.
* **Mouse Controls:**
  * **Hover & Scroll Wheel:** Hover your cursor over the left sidebar to scroll categories, or hover over the right pane to scroll through track lists.
  * **Click list item:** Select a playlist, artist, album, or track (double-click to enter/play).
  * **Click rating stars:** Click stars at the bottom playbar to rate the current song (1 to 5 stars).
  * **Click progress bar:** Click anywhere along the bottom progress bar to seek to that position.

---

## Prerequisites

### 1. Local Machine (Your Mac or Linux Client)
* **Go 1.21+** (Only needed to build the binary from source. If installing via Homebrew later, this is not needed).
* **`mpv`** media player (required for streaming/playing audio).
  * Mac: `brew install mpv`
  * Linux: `sudo apt install mpv` (Ubuntu/Debian) or `sudo dnf install mpv` (Fedora).
* **SSH Client** (`ssh` and `scp` commands).

### 2. Remote Machine (The Host Mac)
* Must have **Remote Login (SSH)** enabled under System Settings -> Sharing.
* Must have the **iTunes** (v5.0+) or **Music** (any macOS version) app running.
  * *Compatibility Notes:* Dynamic app detection automatically adapts to either player. Older versions of iTunes before v5.0 (e.g. legacy Mac OS 9 or OS X 10.0/10.1) are not supported due to missing AppleScript APIs for lyrics/artwork, lack of database IDs, and lack of built-in SSH/`osascript` tools.

---

## How to Build & Run Locally

1. Navigate to the project directory:
   ```bash
   cd ~/itunespull
   ```

2. Tidy dependencies and build the binary:
   ```bash
   go mod tidy
   go build -o itunespull
   ```

3. Run the executable:
   * **Interactive TUI Mode (Default):**
     ```bash
     ./itunespull
     ```
   * **Standard Download CLI Mode:**
     ```bash
     ./itunespull -d
     ```
   * **Edit Configuration:**
     ```bash
     ./itunespull -c
     ```

---

## Re-releasing New Versions (For Developers)

To release a new version of the app to your Homebrew tap:

1. Commit and push your local modifications to GitHub:
   ```bash
   git add .
   git commit -m "Describe your changes"
   git push origin main
   ```
2. Create and push a new git tag (incrementing the version number, e.g. from `v1.0.0` to `v1.0.1`):
   ```bash
   git tag -a v1.0.1 -m "Version v1.0.1"
   git push origin v1.0.1
   ```
3. Run GoReleaser (ensure `GITHUB_TOKEN` environment variable is exported in your terminal session):
   ```bash
   goreleaser release --clean
   ```
