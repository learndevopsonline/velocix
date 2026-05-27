package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

type pidInfo struct {
	PID  int      `json:"pid"`
	Args []string `json:"args"`
}

func pidFilePath(dataDir string) string {
	return filepath.Join(dataDir, "velocix.pid")
}

func writePidFile(dataDir string, args []string) {
	if dataDir == "" {
		return
	}
	_ = os.MkdirAll(dataDir, 0o755)
	data, err := json.Marshal(pidInfo{PID: os.Getpid(), Args: args})
	if err != nil {
		return
	}
	_ = os.WriteFile(pidFilePath(dataDir), data, 0o644)
}

func removePidFile(dataDir string) {
	if dataDir == "" {
		return
	}
	_ = os.Remove(pidFilePath(dataDir))
}

func readPidFile(dataDir string) (*pidInfo, error) {
	data, err := os.ReadFile(pidFilePath(dataDir))
	if err != nil {
		return nil, err
	}
	var info pidInfo
	if err := json.Unmarshal(data, &info); err != nil {
		// Fallback: maybe just a plain pid number
		if pid, perr := strconv.Atoi(string(data)); perr == nil {
			return &pidInfo{PID: pid}, nil
		}
		return nil, err
	}
	return &info, nil
}
