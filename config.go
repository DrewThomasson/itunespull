package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	SSHHost      string `json:"ssh_host"`
	SSHUser      string `json:"ssh_user"`
	DestDir      string `json:"dest_dir"`
	VideoEnabled bool   `json:"video_enabled"`
}

func getConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "itunespull", "config.json"), nil
}

func loadConfig() (*Config, error) {
	path, err := getConfigPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	path, err := getConfigPath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

func runConfigWizard() (*Config, error) {
	reader := bufio.NewReader(os.Stdin)

	// Load existing configuration as defaults if available
	existing, _ := loadConfig()
	defaultHost := "itunes"
	defaultUser := ""
	defaultDest := "~/Downloads"
	if existing != nil {
		if existing.SSHHost != "" {
			defaultHost = existing.SSHHost
		}
		if existing.SSHUser != "" {
			defaultUser = existing.SSHUser
		}
		if existing.DestDir != "" {
			defaultDest = existing.DestDir
		}
	}

	fmt.Println("🛠️  itunespull Configuration Setup")
	fmt.Println("----------------------------------")

	// 1. Host
	fmt.Printf("1. Enter remote iTunes Mac IP address or SSH hostname (default: %s): ", defaultHost)
	hostInput, _ := reader.ReadString('\n')
	hostInput = strings.TrimSpace(hostInput)
	if hostInput == "" {
		hostInput = defaultHost
	}

	// 2. User
	fmt.Printf("2. Enter remote Mac SSH username (press Enter to use default SSH client user): ")
	userInput, _ := reader.ReadString('\n')
	userInput = strings.TrimSpace(userInput)
	if userInput == "" && defaultUser != "" {
		userInput = defaultUser
	}

	// 3. Destination Directory
	fmt.Printf("3. Enter local folder for downloaded tracks (default: %s): ", defaultDest)
	destInput, _ := reader.ReadString('\n')
	destInput = strings.TrimSpace(destInput)
	if destInput == "" {
		destInput = defaultDest
	}

	// Expand ~/ if necessary
	expandedDest := destInput
	if strings.HasPrefix(destInput, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			expandedDest = filepath.Join(home, destInput[1:])
		}
	}

	// 4. Video Enabled Toggle
	defaultVideo := "n"
	if existing != nil && existing.VideoEnabled {
		defaultVideo = "y"
	}
	fmt.Printf("4. Enable mpv video window for album art? (y/n, default: %s): ", defaultVideo)
	videoInput, _ := reader.ReadString('\n')
	videoInput = strings.TrimSpace(strings.ToLower(videoInput))
	videoEnabled := false
	if videoInput == "" {
		videoEnabled = (defaultVideo == "y")
	} else if videoInput == "y" || videoInput == "yes" {
		videoEnabled = true
	}

	cfg := &Config{
		SSHHost:      hostInput,
		SSHUser:      userInput,
		DestDir:      expandedDest,
		VideoEnabled: videoEnabled,
	}

	if err := saveConfig(cfg); err != nil {
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println("\n✅ Configuration saved successfully!")
	configPath, _ := getConfigPath()
	fmt.Printf("Saved to: %s\n\n", configPath)

	return cfg, nil
}
