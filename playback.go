package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Track struct {
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	Path        string  `json:"path"`
	Rating      int     `json:"rating"`
	DBID        int     `json:"dbid"`
	Genre       string  `json:"genre"`
	Year        int     `json:"year"`
	PlayedCount int     `json:"played_count"`
	TrackNumber int     `json:"track_number"`
	Duration    float64 `json:"duration"`
}

type Library struct {
	Tracks    []Track          `json:"tracks"`
	Playlists map[string][]int `json:"playlists"`
}

type PlaybackManager struct {
	ControlPath  string
	SocketPath   string
	SSHHost      string
	SSHUser      string
	ActiveApp    string
	CurrentTrack *Track
	Queue        []Track
	QueueIdx     int
	Position     float64
	Duration     float64
	IsPaused     bool
	Volume       int
	IsMuted      bool
	PrevVolume   int
	VideoEnabled bool
	CurrentArtwork string
	CurrentLyrics  string
	RepeatMode     string // "off", "one", "all"
	OriginalQueue  []Track
	IsShuffled     bool

	sshCmd    *exec.Cmd
	mpvCmd    *exec.Cmd
	running   bool
	mu        sync.Mutex
	onTrackEnd func()
}

var reusedSSHConnection bool

func getControlPath(cfg *Config) string {
	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	username := os.Getenv("USER")
	if username == "" {
		username = "user"
	}

	// Search for existing active sockets
	pattern := fmt.Sprintf("/tmp/ssh_mux_itunes_pull_%s_*", username)
	matches, err := filepath.Glob(pattern)
	if err == nil && len(matches) > 0 {
		// Sort by modification time, newest first
		sort.Slice(matches, func(i, j int) bool {
			fi, erri := os.Stat(matches[i])
			fj, errj := os.Stat(matches[j])
			if erri == nil && errj == nil {
				return fi.ModTime().After(fj.ModTime())
			}
			return false
		})

		for _, socket := range matches {
			// Check if this socket is active
			checkCmd := exec.Command("ssh", "-O", "check", "-S", socket, target)
			var out bytes.Buffer
			checkCmd.Stdout = &out
			checkCmd.Stderr = &out
			checkCmd.Run()
			if strings.Contains(out.String(), "Master running") {
				reusedSSHConnection = true
				return socket
			}
		}
	}

	// Fallback to a new socket
	return fmt.Sprintf("/tmp/ssh_mux_itunes_pull_%s_%d", username, os.Getpid())
}

func establishSSHConnection(cfg *Config, controlPath string) error {
	if reusedSSHConnection {
		return nil
	}

	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	// Terminate existing master connection just in case
	exec.Command("ssh", "-O", "exit", "-S", controlPath, target).Run()

	cmd := exec.Command("ssh", "-N", "-f", "-M", "-S", controlPath, target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to start SSH multiplex connection: %w", err)
	}

	// Verify connection is active
	checkCmd := exec.Command("ssh", "-O", "check", "-S", controlPath, target)
	var out bytes.Buffer
	checkCmd.Stdout = &out
	checkCmd.Stderr = &out
	checkCmd.Run()
	if !strings.Contains(out.String(), "Master running") {
		return fmt.Errorf("SSH connection is not running: %s", out.String())
	}

	return nil
}

func cleanupSSHConnection(cfg *Config, controlPath string) {
	if reusedSSHConnection {
		return
	}
	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}
	exec.Command("ssh", "-O", "exit", "-S", controlPath, target).Run()
}

func detectActiveApp(cfg *Config, controlPath string) (string, error) {
	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	probeScript := `tell application "System Events" to get name of every process whose background only is false`
	cmd := exec.Command("ssh", "-o", "ControlPath="+controlPath, target, "osascript")
	cmd.Stdin = strings.NewReader(probeScript)
	
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	processes := out.String()
	if strings.Contains(processes, "iTunes") {
		return "iTunes", nil
	} else if strings.Contains(processes, "Music") {
		return "Music", nil
	}

	return "", fmt.Errorf("neither iTunes nor Music is running on remote Mac")
}

func checkIfRemoteLibraryNewer(cfg *Config, controlPath string, localModTime int64) bool {
	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	// Query modification time of remote Music Library / iTunes database.
	// Evaluate multiple standard database and folder paths.
	cmdStr := `stat -f %m "$HOME/Music/Music/Music Library.musiclibrary/Music Library.musicdb" 2>/dev/null || ` +
		`stat -f %m "$HOME/Music/Music/Music Library.musiclibrary/Library.musicdb" 2>/dev/null || ` +
		`stat -f %m "$HOME/Music/iTunes/iTunes Library.itl" 2>/dev/null || ` +
		`stat -f %m "$HOME/Music/Music/Music Library.musiclibrary" 2>/dev/null || ` +
		`echo 0`

	cmd := exec.Command("ssh", "-o", "ControlPath="+controlPath, target, cmdStr)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false
	}

	trimmed := strings.TrimSpace(out.String())
	var remoteTime int64
	_, err := fmt.Sscanf(trimmed, "%d", &remoteTime)
	if err != nil || remoteTime == 0 {
		return false
	}

	return remoteTime > localModTime
}

func loadLibraryCache(cfg *Config, controlPath string, activeApp string, forceRefresh bool) (*Library, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cacheDir := filepath.Join(home, ".cache", "itunespull")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, err
	}
	cachePath := filepath.Join(cacheDir, "library.json")

	if !forceRefresh {
		if info, err := os.Stat(cachePath); err == nil {
			localModTime := info.ModTime().Unix()
			if !checkIfRemoteLibraryNewer(cfg, controlPath, localModTime) {
				data, err := os.ReadFile(cachePath)
				if err == nil {
					var lib Library
					if err := json.Unmarshal(data, &lib); err == nil {
						return &lib, nil
					}
				}
			}
		}
	}

	jxaScript := fmt.Sprintf(`
    var app = Application("%s");
    var fileTracks = app.fileTracks;
    var names = fileTracks.name();
    var artists = fileTracks.artist();
    var albums = fileTracks.album();
    var locations = fileTracks.location();
    var ratings = fileTracks.rating();
    var databaseIDs = fileTracks.databaseID();
    var genres = fileTracks.genre();
    var years = fileTracks.year();
    var playedCounts = fileTracks.playedCount();
    var trackNumbers = fileTracks.trackNumber();
    var durations = fileTracks.duration();
    var count = names.length;

    var tracks = [];
    var pathToIndex = {};

    for (var i = 0; i < count; i++) {
        var loc = locations[i];
        if (loc) {
            var path = loc.toString();
            tracks.push({
                title: names[i] || "",
                artist: artists[i] || "",
                album: albums[i] || "",
                path: path,
                rating: ratings[i] || 0,
                dbid: databaseIDs[i] || 0,
                genre: genres[i] || "",
                year: years[i] || 0,
                played_count: playedCounts[i] || 0,
                track_number: trackNumbers[i] || 0,
                duration: durations[i] || 0
            });
            pathToIndex[path] = tracks.length - 1;
        }
    }

    var playlists = app.userPlaylists;
    var playlistData = {};
    for (var i = 0; i < playlists.length; i++) {
        var pl = playlists[i];
        var plName = pl.name();
        try {
            var locs = pl.fileTracks.location();
            var idxs = [];
            for (var j = 0; j < locs.length; j++) {
                if (locs[j]) {
                    var path = locs[j].toString();
                    if (path in pathToIndex) {
                        idxs.push(pathToIndex[path]);
                    }
                }
            }
            if (idxs.length > 0) {
                playlistData[plName] = idxs;
            }
        } catch(e) {}
    }

    JSON.stringify({tracks: tracks, playlists: playlistData});
	`, activeApp)

	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	cmd := exec.Command("ssh", "-o", "ControlPath="+controlPath, target, "osascript", "-l", "JavaScript")
	cmd.Stdin = strings.NewReader(jxaScript)

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("remote library query failed: %s", stderr.String())
	}

	var lib Library
	if err := json.Unmarshal(out.Bytes(), &lib); err != nil {
		return nil, fmt.Errorf("failed to parse library JSON: %w", err)
	}

	// Write to cache file
	fileData, err := json.Marshal(lib)
	if err == nil {
		os.WriteFile(cachePath, fileData, 0644)
	}

	return &lib, nil
}

func searchiTunes(cfg *Config, controlPath string, activeApp string, query string) ([]Track, error) {
	escapedQuery := strings.ReplaceAll(strings.ReplaceAll(query, "\\", "\\\\"), "\"", "\\\"")
	applescript := fmt.Sprintf(`
    set queryText to "%s"
    set output to ""
    tell application "%s"
        try
            set matchedTracks to (every track whose name contains queryText or artist contains queryText or album contains queryText)
            repeat with t in matchedTracks
                try
                    set tLoc to location of t
                    if tLoc is not missing value then
                        set tPath to POSIX path of tLoc
                        set output to output & (name of t) & "|||" & (artist of t) & "|||" & (album of t) & "|||" & tPath & "\n"
                    end if
                end try
            end repeat
        end try
    end tell
    return output
	`, escapedQuery, activeApp)

	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	cmd := exec.Command("ssh", "-o", "ControlPath="+controlPath, target, "osascript")
	cmd.Stdin = strings.NewReader(applescript)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("osascript error: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	raw := out.String()
	if raw == "" {
		if stderr.Len() > 0 {
			fmt.Printf("[DEBUG] osascript empty stdout. Stderr: %q\n", stderr.String())
		}
		return nil, nil
	}

	var tracks []Track
	seenPaths := make(map[string]bool)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|||")
		if len(parts) == 4 {
			path := parts[3]
			if !seenPaths[path] {
				seenPaths[path] = true
				tracks = append(tracks, Track{
					Title:  parts[0],
					Artist: parts[1],
					Album:  parts[2],
					Path:   path,
				})
			}
		}
	}
	return tracks, nil
}

func NewPlaybackManager(cfg *Config, controlPath string, activeApp string) *PlaybackManager {
	return &PlaybackManager{
		ControlPath:  controlPath,
		SocketPath:   "/tmp/itunes-mpv.sock",
		SSHHost:      cfg.SSHHost,
		SSHUser:      cfg.SSHUser,
		ActiveApp:    activeApp,
		Volume:       80,
		running:      true,
		VideoEnabled: cfg.VideoEnabled,
		RepeatMode:   "off",
	}
}

func (pm *PlaybackManager) StartMonitorLoop() {
	go func() {
		for pm.running {
			pm.mu.Lock()
			mpvActive := pm.mpvCmd != nil
			pm.mu.Unlock()

			if mpvActive {
				pos := pm.getProperty("time-pos")
				dur := pm.getProperty("duration")
				paused := pm.getProperty("pause")
				vol := pm.getProperty("volume")

				pm.mu.Lock()
				if pos != nil {
					pm.Position, _ = pos.(float64)
				}
				if dur != nil {
					pm.Duration, _ = dur.(float64)
				}
				if paused != nil {
					pm.IsPaused, _ = paused.(bool)
				}
				if vol != nil {
					vFloat, ok := vol.(float64)
					if ok {
						val := int(vFloat)
						if !(val == 0 && pm.IsMuted) {
							pm.Volume = val
						}
					}
				}
				pm.mu.Unlock()
			} else {
				pm.mu.Lock()
				pm.Position = 0
				pm.Duration = 0
				pm.mu.Unlock()
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
}

func (pm *PlaybackManager) sendCommand(args []interface{}) (interface{}, error) {
	if _, err := os.Stat(pm.SocketPath); os.IsNotExist(err) {
		return nil, err
	}

	conn, err := net.DialTimeout("unix", pm.SocketPath, 150*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(150 * time.Millisecond))

	payload := map[string]interface{}{"command": args}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	_, err = conn.Write(append(data, '\n'))
	if err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	respLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(respLine), &resp); err != nil {
		return nil, err
	}

	if errVal, exists := resp["error"]; exists && errVal != "success" {
		return nil, fmt.Errorf("mpv error: %v", errVal)
	}

	return resp["data"], nil
}

func (pm *PlaybackManager) getProperty(prop string) interface{} {
	val, err := pm.sendCommand([]interface{}{"get_property", prop})
	if err != nil {
		return nil
	}
	return val
}

func (pm *PlaybackManager) Play(track Track) {
	pm.Stop()

	pm.mu.Lock()
	pm.CurrentTrack = &track
	pm.Position = 0
	pm.Duration = 0
	pm.IsPaused = false

	os.Remove(pm.SocketPath)

	escapedPath := strings.ReplaceAll(strings.ReplaceAll(track.Path, "\\", "\\\\"), "\"", "\\\"")
	target := pm.SSHHost
	if pm.SSHUser != "" {
		target = pm.SSHUser + "@" + pm.SSHHost
	}

	pm.sshCmd = exec.Command("ssh", "-o", "ControlPath="+pm.ControlPath, target, fmt.Sprintf(`cat "%s"`, escapedPath))
	mpvArgs := []string{fmt.Sprintf("--input-ipc-server=%s", pm.SocketPath), "--no-terminal"}
	if !pm.VideoEnabled {
		mpvArgs = append(mpvArgs, "--no-video")
	}
	mpvArgs = append(mpvArgs, "-")
	pm.mpvCmd = exec.Command("mpv", mpvArgs...)

	// Set up pipes
	pr, pw := io.Pipe()
	pm.sshCmd.Stdout = pw
	pm.mpvCmd.Stdin = pr
	pm.mu.Unlock()

	// Start SSH
	if err := pm.sshCmd.Start(); err != nil {
		return
	}

	// Start MPV
	if err := pm.mpvCmd.Start(); err != nil {
		return
	}

	// Fetch artwork and lyrics in background
	go pm.fetchArtworkAndLyrics(track)

	go func(cmd *exec.Cmd, w *io.PipeWriter) {
		cmd.Wait()
		w.Close()
	}(pm.sshCmd, pw)

	go func(cmd *exec.Cmd, currentTrack Track) {
		cmd.Wait()

		pm.mu.Lock()
		if pm.mpvCmd == cmd {
			pm.mpvCmd = nil
			if pm.sshCmd != nil {
				if pm.sshCmd.Process != nil {
					pm.sshCmd.Process.Kill()
				}
				pm.sshCmd = nil
			}
			pm.mu.Unlock()

			// Sync and play next
			pm.syncPlayedCountForTrack(currentTrack)
			pm.Next()
		} else {
			pm.mu.Unlock()
		}
	}(pm.mpvCmd, track)
}

func (pm *PlaybackManager) Stop() {
	pm.mu.Lock()
	ssh := pm.sshCmd
	mpv := pm.mpvCmd
	pm.sshCmd = nil
	pm.mpvCmd = nil
	pm.CurrentTrack = nil
	pm.Position = 0
	pm.Duration = 0
	pm.mu.Unlock()

	if mpv != nil && mpv.Process != nil {
		mpv.Process.Kill()
	}

	if ssh != nil && ssh.Process != nil {
		ssh.Process.Kill()
	}
}

func (pm *PlaybackManager) PauseToggle() {
	pm.sendCommand([]interface{}{"cycle", "pause"})
}

func (pm *PlaybackManager) SetVolume(val int) {
	pm.mu.Lock()
	pm.IsMuted = false
	if val < 0 {
		val = 0
	}
	if val > 100 {
		val = 100
	}
	pm.Volume = val
	pm.mu.Unlock()

	pm.sendCommand([]interface{}{"set_property", "volume", val})
}

func (pm *PlaybackManager) MuteToggle() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.IsMuted {
		pm.IsMuted = false
		pm.Volume = pm.PrevVolume
		pm.sendCommand([]interface{}{"set_property", "volume", pm.Volume})
	} else {
		pm.IsMuted = true
		pm.PrevVolume = pm.Volume
		pm.Volume = 0
		pm.sendCommand([]interface{}{"set_property", "volume", 0})
	}
}

func (pm *PlaybackManager) Seek(seconds float64) {
	pm.sendCommand([]interface{}{"seek", seconds})
}

func (pm *PlaybackManager) SeekAbsolute(seconds float64) {
	pm.sendCommand([]interface{}{"set_property", "time-pos", seconds})
}

func (pm *PlaybackManager) Next() {
	pm.mu.Lock()
	if len(pm.Queue) == 0 {
		pm.mu.Unlock()
		return
	}

	if pm.RepeatMode == "one" {
		track := pm.Queue[pm.QueueIdx]
		pm.mu.Unlock()
		pm.Play(track)
		if pm.onTrackEnd != nil {
			pm.onTrackEnd()
		}
		return
	}

	if pm.QueueIdx < len(pm.Queue)-1 {
		pm.QueueIdx++
		track := pm.Queue[pm.QueueIdx]
		pm.mu.Unlock()
		pm.Play(track)
	} else if pm.RepeatMode == "all" {
		pm.QueueIdx = 0
		track := pm.Queue[pm.QueueIdx]
		pm.mu.Unlock()
		pm.Play(track)
	} else {
		pm.mu.Unlock()
		pm.Stop()
	}

	if pm.onTrackEnd != nil {
		pm.onTrackEnd()
	}
}

func (pm *PlaybackManager) Prev() {
	pm.mu.Lock()
	if len(pm.Queue) == 0 {
		pm.mu.Unlock()
		return
	}

	if pm.QueueIdx > 0 {
		pm.QueueIdx--
		track := pm.Queue[pm.QueueIdx]
		pm.mu.Unlock()
		pm.Play(track)
	} else if pm.RepeatMode == "all" {
		pm.QueueIdx = len(pm.Queue) - 1
		track := pm.Queue[pm.QueueIdx]
		pm.mu.Unlock()
		pm.Play(track)
	} else {
		pm.mu.Unlock()
		pm.Stop()
	}

	if pm.onTrackEnd != nil {
		pm.onTrackEnd()
	}
}

func (pm *PlaybackManager) SetQueue(tracks []Track, startIdx int) {
	pm.mu.Lock()
	pm.Queue = append([]Track(nil), tracks...)
	pm.QueueIdx = startIdx
	pm.OriginalQueue = append([]Track(nil), tracks...)
	
	if pm.IsShuffled {
		pm.shuffleQueueFromCurrent()
	}
	pm.mu.Unlock()
}

func (pm *PlaybackManager) shuffleQueueFromCurrent() {
	if len(pm.Queue) <= 1 {
		return
	}
	currTrack := pm.Queue[pm.QueueIdx]
	
	otherTracks := make([]Track, 0, len(pm.Queue)-1)
	for i, t := range pm.Queue {
		if i != pm.QueueIdx {
			otherTracks = append(otherTracks, t)
		}
	}
	
	t := time.Now().UnixNano()
	for i := len(otherTracks) - 1; i > 0; i-- {
		t = (t*1103515245 + 12345) & 0x7fffffff
		j := int(t % int64(i+1))
		otherTracks[i], otherTracks[j] = otherTracks[j], otherTracks[i]
	}
	
	pm.Queue = append([]Track{currTrack}, otherTracks...)
	pm.QueueIdx = 0
}

func (pm *PlaybackManager) ToggleShuffle() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	
	if len(pm.Queue) == 0 {
		pm.IsShuffled = !pm.IsShuffled
		return
	}
	
	if !pm.IsShuffled {
		pm.IsShuffled = true
		pm.shuffleQueueFromCurrent()
	} else {
		pm.IsShuffled = false
		currTrackPath := ""
		if pm.QueueIdx < len(pm.Queue) {
			currTrackPath = pm.Queue[pm.QueueIdx].Path
		}
		
		pm.Queue = append([]Track(nil), pm.OriginalQueue...)
		
		pm.QueueIdx = 0
		if currTrackPath != "" {
			for idx, t := range pm.Queue {
				if t.Path == currTrackPath {
					pm.QueueIdx = idx
					break
				}
			}
		}
	}
}

func (pm *PlaybackManager) syncPlayedCountForTrack(track Track) {
	var applescript string
	if track.DBID > 0 {
		applescript = fmt.Sprintf(`tell application "%s"
            try
                set t to (some track whose database ID is %d)
                set c to played count of t
                set played count of (some track whose database ID is %d) to (c + 1)
            end try
        end tell`, pm.ActiveApp, track.DBID, track.DBID)
	} else {
		title := strings.ReplaceAll(strings.ReplaceAll(track.Title, "\\", "\\\\"), "\"", "\\\"")
		artist := strings.ReplaceAll(strings.ReplaceAll(track.Artist, "\\", "\\\\"), "\"", "\\\"")
		applescript = fmt.Sprintf(`tell application "%s"
            try
                set t to (some track whose name is "%s" and artist is "%s")
                set dbID to database ID of t
                set c to played count of t
                set played count of (some track whose database ID is dbID) to (c + 1)
            end try
        end tell`, pm.ActiveApp, title, artist)
	}

	target := pm.SSHHost
	if pm.SSHUser != "" {
		target = pm.SSHUser + "@" + pm.SSHHost
	}

	go func(script string) {
		cmd := exec.Command("ssh", "-o", "ControlPath="+pm.ControlPath, target, "osascript")
		cmd.Stdin = strings.NewReader(script)
		cmd.Run() // run synchronously in the background goroutine to clean up process
	}(applescript)
}

func (pm *PlaybackManager) RateCurrentTrack(stars int) {
	pm.mu.Lock()
	track := pm.CurrentTrack
	if track == nil {
		pm.mu.Unlock()
		return
	}

	ratingVal := stars * 20
	track.Rating = ratingVal
	if pm.CurrentTrack != nil {
		pm.CurrentTrack.Rating = ratingVal
	}
	pm.mu.Unlock()

	var applescript string
	if track.DBID > 0 {
		applescript = fmt.Sprintf(`tell application "%s" to set rating of (some track whose database ID is %d) to %d`, pm.ActiveApp, track.DBID, ratingVal)
	} else {
		title := strings.ReplaceAll(strings.ReplaceAll(track.Title, "\\", "\\\\"), "\"", "\\\"")
		artist := strings.ReplaceAll(strings.ReplaceAll(track.Artist, "\\", "\\\\"), "\"", "\\\"")
		applescript = fmt.Sprintf(`tell application "%s" to set rating of (some track whose name is "%s" and artist is "%s") to %d`, pm.ActiveApp, title, artist, ratingVal)
	}

	target := pm.SSHHost
	if pm.SSHUser != "" {
		target = pm.SSHUser + "@" + pm.SSHHost
	}

	go func(script string) {
		cmd := exec.Command("ssh", "-o", "ControlPath="+pm.ControlPath, target, "osascript")
		cmd.Stdin = strings.NewReader(script)
		cmd.Run() // run synchronously in the background goroutine to clean up process
	}(applescript)
}

func (pm *PlaybackManager) Close() {
	pm.running = false
	pm.Stop()
	os.Remove(pm.SocketPath)
}

func downloadTracks(cfg *Config, controlPath string, tracks []Track, destDir string, onProgress func(int, string, bool, string)) {
	target := cfg.SSHHost
	if cfg.SSHUser != "" {
		target = cfg.SSHUser + "@" + cfg.SSHHost
	}

	for idx, track := range tracks {
		filename := filepath.Base(track.Path)
		localDest := filepath.Join(destDir, filename)

		onProgress(idx+1, fmt.Sprintf("%s - %s", track.Artist, track.Title), false, "")

		cmd := exec.Command("scp", "-o", "ControlPath="+controlPath, fmt.Sprintf("%s:%s", target, track.Path), localDest)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()

		if err == nil {
			onProgress(idx+1, fmt.Sprintf("%s - %s", track.Artist, track.Title), true, "")
		} else {
			onProgress(idx+1, fmt.Sprintf("%s - %s", track.Artist, track.Title), false, strings.TrimSpace(stderr.String()))
		}
	}
}

func parseSelections(selectionStr string, maxVal int) []int {
	if strings.ToLower(selectionStr) == "all" {
		var all []int
		for i := 1; i <= maxVal; i++ {
			all = append(all, i)
		}
		return all
	}

	selections := make(map[int]bool)
	parts := strings.Split(strings.ReplaceAll(selectionStr, " ", ""), ",")
	for _, part := range parts {
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			subparts := strings.Split(part, "-")
			if len(subparts) == 2 {
				start, err1 := strconv.Atoi(subparts[0])
				end, err2 := strconv.Atoi(subparts[1])
				if err1 == nil && err2 == nil && start >= 1 && start <= end && end <= maxVal {
					for i := start; i <= end; i++ {
						selections[i] = true
					}
				}
			}
		} else {
			val, err := strconv.Atoi(part)
			if err == nil && val >= 1 && val <= maxVal {
				selections[val] = true
			}
		}
	}

	var result []int
	for k := range selections {
		result = append(result, k)
	}
	// Sort results
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i] > result[j] {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

func resizeImage(img image.Image, width, height int) image.Image {
	srcBounds := img.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			srcX := srcBounds.Min.X + (x * srcW / width)
			srcY := srcBounds.Min.Y + (y * srcH / height)
			dst.Set(x, y, img.At(srcX, srcY))
		}
	}
	return dst
}

func imageToANSI(img image.Image, targetWidth, targetHeight int) string {
	resized := resizeImage(img, targetWidth, targetHeight)
	var sb strings.Builder
	for y := 0; y < targetHeight; y++ {
		for x := 0; x < targetWidth; x++ {
			r, g, b, _ := resized.At(x, y).RGBA()
			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)
			sb.WriteString(fmt.Sprintf("\x1b[38;2;%d;%d;%dm█\x1b[0m", r8, g8, b8))
		}
		if y < targetHeight-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (pm *PlaybackManager) fetchArtworkAndLyrics(track Track) {
	pm.mu.Lock()
	pm.CurrentArtwork = ""
	pm.CurrentLyrics = "Loading lyrics..."
	pm.mu.Unlock()

	target := pm.SSHHost
	if pm.SSHUser != "" {
		target = pm.SSHUser + "@" + pm.SSHHost
	}

	// 1. Run AppleScript to get lyrics and export artwork raw data to /tmp/itunespull-cover.raw
	applescript := fmt.Sprintf(`
		tell application "%s"
			try
				set t to (some file track of library playlist 1 whose database ID is %d)
				set lyr to lyrics of t
			on error
				set lyr to ""
			end try
			set hasArt to "no"
			set rawData to missing value
			try
				if (count of artworks of t) > 0 then
					set rawData to raw data of artwork 1 of t
					set hasArt to "yes"
				end if
			on error
				set hasArt to "no"
			end try
		end tell

		if hasArt is "yes" then
			try
				set fp to "/tmp/itunespull-cover.raw"
				set fileRef to (open for access POSIX file fp with write permission)
				set eof fileRef to 0
				write rawData to fileRef
				close access fileRef
			on error
				set hasArt to "no"
			end try
		end if

		return lyr & "|||" & hasArt
	`, pm.ActiveApp, track.DBID)

	if track.DBID == 0 {
		title := strings.ReplaceAll(strings.ReplaceAll(track.Title, "\\", "\\\\"), "\"", "\\\"")
		artist := strings.ReplaceAll(strings.ReplaceAll(track.Artist, "\\", "\\\\"), "\"", "\\\"")
		applescript = fmt.Sprintf(`
			tell application "%s"
				try
					set t to (some file track of library playlist 1 whose name is "%s" and artist is "%s")
					set lyr to lyrics of t
				on error
					set lyr to ""
				end try
				set hasArt to "no"
				set rawData to missing value
				try
					if (count of artworks of t) > 0 then
						set rawData to raw data of artwork 1 of t
						set hasArt to "yes"
					end if
				on error
					set hasArt to "no"
				end try
			end tell

			if hasArt is "yes" then
				try
					set fp to "/tmp/itunespull-cover.raw"
					set fileRef to (open for access POSIX file fp with write permission)
					set eof fileRef to 0
					write rawData to fileRef
					close access fileRef
				on error
					set hasArt to "no"
				end try
			end if

			return lyr & "|||" & hasArt
		`, pm.ActiveApp, title, artist)
	}

	cmd := exec.Command("ssh", "-o", "ControlPath="+pm.ControlPath, target, "osascript")
	cmd.Stdin = strings.NewReader(applescript)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		pm.mu.Lock()
		pm.CurrentLyrics = fmt.Sprintf("Error fetching lyrics: %v\n%s", err, stderr.String())
		pm.mu.Unlock()
		return
	}

	parts := strings.Split(out.String(), "|||")
	lyricsText := strings.TrimSpace(parts[0])
	lyricsText = strings.ReplaceAll(lyricsText, "\r\n", "\n")
	lyricsText = strings.ReplaceAll(lyricsText, "\r", "\n")
	if lyricsText == "" {
		lyricsText = "Lyrics not available for this track."
	}

	hasArt := false
	if len(parts) > 1 && strings.Contains(parts[1], "yes") {
		hasArt = true
	}

	var ansiArt string
	if hasArt {
		// Copy file locally using scp over multiplex connection
		localRawPath := filepath.Join(os.TempDir(), fmt.Sprintf("itunespull-cover-%d.raw", track.DBID))
		scpCmd := exec.Command("scp", "-o", "ControlPath="+pm.ControlPath, fmt.Sprintf("%s:/tmp/itunespull-cover.raw", target), localRawPath)
		if err := scpCmd.Run(); err == nil {
			f, err := os.Open(localRawPath)
			if err == nil {
				img, _, err := image.Decode(f)
				if err == nil {
					ansiArt = imageToANSI(img, 25, 12)
				}
				f.Close()
			}
			os.Remove(localRawPath)
		}
	}

	pm.mu.Lock()
	pm.CurrentLyrics = lyricsText
	pm.CurrentArtwork = ansiArt
	pm.mu.Unlock()
}
