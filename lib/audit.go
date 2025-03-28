package lib

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Allow mocking exec.Command in tests
var execCommand = exec.Command

// AuditStore defines the interface for audit log persistence
type AuditStore interface {
	// Store persists the audit log
	Store(log AuditLog) error
	// LoadLogs loads all audit logs for a project and git revision
	LoadLogs(project, gitRevision string) ([]AuditLog, error)
}

// AuditLog represents a single execution log entry
type AuditLog struct {
	Project     string    `json:"project"`
	GitRevision string    `json:"git_revision"`
	Stage       string    `json:"stage"`
	Command     string    `json:"command"`
	StartTime   time.Time `json:"start_time"`
	Duration    float64   `json:"duration"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
}

// generateFilename creates a consistent filename for the audit log
func (a AuditLog) generateFilename() string {
	return fmt.Sprintf("%s-%s-%s.json",
		a.Project,
		a.Stage,
		a.StartTime.Format("20060102-150405"),
	)
}

// marshalLog converts the audit log to JSON bytes
func (a AuditLog) marshalLog() ([]byte, error) {
	return json.MarshalIndent(a, "", "  ")
}

// SetError sets the error status and message on the audit log
func (a *AuditLog) SetError(err error) {
	a.Status = "error"
	a.Error = err.Error()
}

// GetGitRevision returns the current git revision
func GetGitRevision() (string, error) {
	cmd := execCommand("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
