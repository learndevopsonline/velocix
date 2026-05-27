package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/skalluru/velocix/internal/config"
	"github.com/spf13/cobra"
)

const releasesAPI = "https://api.github.com/repos/millwrights/velocix/releases/latest"

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade Velocix to the latest GitHub release",
	RunE:  runUpgrade,
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	fmt.Println("Checking for latest release...")
	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("fetching latest release: %w", err)
	}

	current := Version
	fmt.Printf("Current: %s\n", current)
	fmt.Printf("Latest:  %s\n", rel.TagName)

	if rel.TagName == current {
		fmt.Println("Already on the latest version.")
		return nil
	}

	assetName := fmt.Sprintf("velocix-%s-%s", runtime.GOOS, runtime.GOARCH)
	var downloadURL string
	for _, a := range rel.Assets {
		if a.Name == assetName {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no binary found for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, rel.TagName)
	}

	fmt.Printf("Downloading %s...\n", assetName)
	tmpPath, err := downloadToTemp(downloadURL)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	defer os.Remove(tmpPath)

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Verify the new binary works
	if err := exec.Command(tmpPath, "--version").Run(); err != nil {
		return fmt.Errorf("downloaded binary failed verification: %w", err)
	}

	// Find currently running velocix
	cfg, _ := config.Load()
	dataDir := config.DefaultConfigDir()
	if cfg != nil && cfg.DataDir != "" {
		dataDir = cfg.DataDir
	}
	info, _ := readPidFile(dataDir)
	wasRunning := info != nil && processAlive(info.PID)

	if wasRunning {
		fmt.Printf("Stopping running velocix (PID %d)...\n", info.PID)
		if err := stopProcess(info.PID); err != nil {
			fmt.Printf("Warning: failed to stop process: %v\n", err)
		}
		// Wait briefly for shutdown
		for i := 0; i < 20; i++ {
			if !processAlive(info.PID) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Replace current binary
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}
	execPath, _ = filepath.EvalSymlinks(execPath)

	fmt.Printf("Installing to %s...\n", execPath)
	if err := replaceBinary(tmpPath, execPath); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}

	fmt.Printf("Upgraded to %s\n", rel.TagName)

	if wasRunning && len(info.Args) > 0 {
		fmt.Println("Restarting velocix...")
		if err := restartProcess(execPath, info.Args, dataDir); err != nil {
			fmt.Printf("Warning: failed to restart automatically: %v\n", err)
			fmt.Println("Please run velocix again manually.")
		} else {
			fmt.Println("Velocix restarted.")
		}
	}
	return nil
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func fetchLatestRelease() (*ghRelease, error) {
	req, err := http.NewRequest("GET", releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func downloadToTemp(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	f, err := os.CreateTemp("", "velocix-upgrade-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	return f.Name(), nil
}

func replaceBinary(srcPath, dstPath string) error {
	// Try direct rename first (works if on same filesystem)
	if err := os.Rename(srcPath, dstPath); err == nil {
		return nil
	}
	// Cross-filesystem: copy then move
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	tmpDst := dstPath + ".new"
	dst, err := os.OpenFile(tmpDst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmpDst)
		return err
	}
	dst.Close()

	return os.Rename(tmpDst, dstPath)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 doesn't actually send a signal but checks if the process exists
	return proc.Signal(syscall.Signal(0)) == nil
}

func stopProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}

func restartProcess(execPath string, args []string, dataDir string) error {
	if len(args) == 0 {
		return fmt.Errorf("no original args available")
	}
	// args[0] is the original binary path, skip it
	cmdArgs := args[1:]

	logPath := filepath.Join(dataDir, "velocix.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logFile = nil
	}

	cmd := exec.Command(execPath, cmdArgs...)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	return nil
}
