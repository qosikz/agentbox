package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Session struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	Task      string    `json:"task"`
	Agent     string    `json:"agent"`
	Status    string    `json:"status"`
}

func New(task, agent string) Session {
	return Session{
		ID:        time.Now().UTC().Format("20060102-150405"),
		StartedAt: time.Now().UTC(),
		Task:      task,
		Agent:     agent,
		Status:    "created",
	}
}

func Save(base string, s Session) error {
	dir := filepath.Join(base, ".agentbox", "sessions", s.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "session.json"), data, 0644)
}
