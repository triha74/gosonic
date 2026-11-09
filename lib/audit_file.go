package lib

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileStore implements AuditStore using the local filesystem
type FileStore struct {
	Directory string // Directory where logs will be stored
	Verbose   bool   // Enable verbose logging
}

// NewFileStore creates a new FileStore with the given directory
func NewFileStore(directory string, verbose bool) *FileStore {
	return &FileStore{
		Directory: directory,
		Verbose:   verbose,
	}
}

// Store implements AuditStore for FileStore
func (fs *FileStore) Store(log AuditLog) error {
	if err := signLog(&log); err != nil {
		return fmt.Errorf("signing audit log: %w", err)
	}
	data, err := log.marshalLog()
	if err != nil {
		return fmt.Errorf("marshaling audit log: %w", err)
	}

	// Create logs directory if it doesn't exist
	if err := os.MkdirAll(fs.Directory, 0755); err != nil {
		return fmt.Errorf("creating logs directory: %w", err)
	}

	logPath := filepath.Join(fs.Directory, log.generateFilename())

	if err := os.WriteFile(logPath, data, 0600); err != nil {
		return fmt.Errorf("writing audit log: %w", err)
	}

	if fs.Verbose {
		fmt.Printf("Successfully stored audit log to file: %s\n", logPath)
	}

	return nil
}

// LoadLogs implements AuditStore for FileStore
func (fs *FileStore) LoadLogs(project, gitRevision string) ([]AuditLog, error) {
	var logs []AuditLog

	if fs.Verbose {
		fmt.Printf("Loading audit logs from directory '%s' for project '%s' and revision '%s'\n",
			fs.Directory, project, gitRevision)
	}

	// Read all files in the log directory
	entries, err := os.ReadDir(fs.Directory)
	if err != nil {
		if os.IsNotExist(err) {
			return logs, nil // Return empty slice if directory doesn't exist
		}
		return nil, fmt.Errorf("reading logs directory: %w", err)
	}

	// Filter and parse log files
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), project+"-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(fs.Directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading log file %s: %w", entry.Name(), err)
		}

		var log AuditLog
		if err := json.Unmarshal(data, &log); err != nil {
			return nil, fmt.Errorf("parsing log file %s: %w", entry.Name(), err)
		}
		trusted, err := logTrusted(log)
		if err != nil {
			return nil, err
		}
		if !trusted {
			continue
		}

		// Only include logs for the specified git revision
		if log.GitRevision == gitRevision {
			logs = append(logs, log)
		}
	}

	if fs.Verbose {
		fmt.Printf("Loaded %d audit logs\n", len(logs))
	}

	return logs, nil
}

// ListLogs reads every audit file for a project, across git revisions.
// A missing directory is an empty result.
func (fs *FileStore) ListLogs(project string) ([]AuditLog, error) {
	entries, err := os.ReadDir(fs.Directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading logs directory: %w", err)
	}

	var logs []AuditLog
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), project+"-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fs.Directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading log file %s: %w", entry.Name(), err)
		}
		var log AuditLog
		if err := json.Unmarshal(data, &log); err != nil {
			return nil, fmt.Errorf("parsing log file %s: %w", entry.Name(), err)
		}
		trusted, err := logTrusted(log)
		if err != nil {
			return nil, err
		}
		if trusted && log.Project == project {
			logs = append(logs, log)
		}
	}
	return logs, nil
}
