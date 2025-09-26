package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// LastState stores the last executed action and context
type LastState struct {
	Region      string `json:"region"`
	ClusterName string `json:"cluster_name"`
	ServiceName string `json:"service_name"`
	Action      string `json:"action"`
}

func stateFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + string(os.PathSeparator) + ".quick_ecs.state"
}

func loadLastState() (*LastState, error) {
	path := stateFilePath()
	if path == "" {
		return nil, fmt.Errorf("no home dir")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s LastState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveLastState(s *LastState) {
	path := stateFilePath()
	if path == "" {
		return
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0600)
}
