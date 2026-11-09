package lib

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	Signature   string    `json:"signature,omitempty"`
}

const (
	auditKeyEnv     = "GOSONIC_AUDIT_HMAC_KEY"
	auditKeyFileEnv = "GOSONIC_AUDIT_HMAC_KEY_FILE"
)

// ProtectedEnv reports names that must never be copied into a container.
// The HMAC key stays in the gosonic process.
func ProtectedEnv(name string) bool {
	switch name {
	case auditKeyEnv, auditKeyFileEnv:
		return true
	default:
		return false
	}
}

type auditMACPayload struct {
	Project       string  `json:"project"`
	GitRevision   string  `json:"git_revision"`
	Stage         string  `json:"stage"`
	Command       string  `json:"command"`
	StartUnixNano int64   `json:"start_unix_nano"`
	Duration      float64 `json:"duration"`
	Status        string  `json:"status"`
	Error         string  `json:"error"`
}

func (a AuditLog) canonical() ([]byte, error) {
	return json.Marshal(auditMACPayload{
		Project:       a.Project,
		GitRevision:   a.GitRevision,
		Stage:         a.Stage,
		Command:       a.Command,
		StartUnixNano: a.StartTime.UnixNano(),
		Duration:      a.Duration,
		Status:        a.Status,
		Error:         a.Error,
	})
}

func (a *AuditLog) sign(key []byte) error {
	payload, err := a.canonical()
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	a.Signature = hex.EncodeToString(mac.Sum(nil))
	return nil
}

func (a AuditLog) verify(key []byte) bool {
	if a.Signature == "" || len(key) == 0 {
		return false
	}
	payload, err := a.canonical()
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	got, err := hex.DecodeString(a.Signature)
	if err != nil {
		return false
	}
	return hmac.Equal(mac.Sum(nil), got)
}

func signLog(log *AuditLog) error {
	key, _, err := loadAuditKey()
	if err != nil {
		return err
	}
	return log.sign(key)
}

func logTrusted(log AuditLog) (bool, error) {
	key, _, err := loadAuditKey()
	if err != nil {
		return false, err
	}
	return log.verify(key), nil
}

var auditKeyMu sync.Mutex

// loadAuditKey returns the HMAC key and, when the key lives in a file, that path.
// Containers must not mount the path. An env key has an empty path.
func loadAuditKey() ([]byte, string, error) {
	auditKeyMu.Lock()
	defer auditKeyMu.Unlock()

	if raw := os.Getenv(auditKeyEnv); raw != "" {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, "", fmt.Errorf("decoding %s: %w", auditKeyEnv, err)
		}
		if len(key) < 32 {
			return nil, "", fmt.Errorf("%s must decode to at least 32 bytes", auditKeyEnv)
		}
		return key, "", nil
	}

	path := os.Getenv(auditKeyFileEnv)
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, "", fmt.Errorf("finding config directory: %w", err)
		}
		path = filepath.Join(dir, "gosonic", "audit.key")
	}
	if data, err := os.ReadFile(path); err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, "", fmt.Errorf("reading audit key: %w", statErr)
		}
		if info.Mode().Perm()&0077 != 0 {
			return nil, "", fmt.Errorf("audit key %s must not be readable by group or others", path)
		}
		if len(data) < 32 {
			return nil, "", fmt.Errorf("audit key %s is shorter than 32 bytes", path)
		}
		return data, path, nil
	} else if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("reading audit key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, "", fmt.Errorf("creating audit key directory: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, "", fmt.Errorf("generating audit key: %w", err)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, "", fmt.Errorf("writing audit key: %w", err)
	}
	return key, path, nil
}

// generateFilename creates a consistent filename for the audit log
func (a AuditLog) generateFilename() string {
	return fmt.Sprintf("%s-%s-%s.json",
		a.Project,
		a.Stage,
		a.StartTime.Format("20060102-150405.000000000"),
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
