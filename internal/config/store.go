package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the config file name inside the temail config directory.
const FileName = "config.json"

// dirName is the application directory under os.UserConfigDir().
const dirName = "temail"

// Manager reads and writes the config file in one directory. Tests construct
// it with NewManager(t.TempDir()); the app uses DefaultManager.
type Manager struct {
	dir string
}

// NewManager returns a Manager that stores config.json in dir.
func NewManager(dir string) *Manager {
	return &Manager{dir: dir}
}

// DefaultManager returns a Manager for os.UserConfigDir()/temail.
func DefaultManager() (*Manager, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config directory: %w", err)
	}
	return NewManager(filepath.Join(base, dirName)), nil
}

// Path returns the full path of the config file.
func (m *Manager) Path() string {
	return filepath.Join(m.dir, FileName)
}

// Load reads the config file. A missing file is not an error: Load returns
// Default(). Unknown JSON fields and invalid content are errors.
func (m *Manager) Load() (Config, error) {
	data, err := os.ReadFile(m.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", m.Path(), err)
	}
	applyDefaults(&c)
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save validates c and writes it atomically: a temp file in the same
// directory is written, synced and renamed over config.json. The directory is
// created with 0700 and the file is 0600.
func (m *Manager) Save(c Config) error {
	if err := Validate(c); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	return writeFileAtomic(m.Path(), data, 0o600)
}

// writeFileAtomic writes data to a temp file beside path, then renames it.
// On failure the temp file is removed and path is left untouched.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
