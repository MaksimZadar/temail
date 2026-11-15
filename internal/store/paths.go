package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultPath returns the location of cache.db: $XDG_DATA_HOME/temail/cache.db
// on Linux (XDG_DATA_HOME defaults to ~/.local/share) and
// %LOCALAPPDATA%\temail\cache.db on Windows. This is the data directory, not
// the config directory. The audit log will use a separate file.
func DefaultPath() (string, error) {
	base, err := dataBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "temail", "cache.db"), nil
}

// dataBase returns the per-user data root for the current OS.
func dataBase() (string, error) {
	if runtime.GOOS == "windows" {
		v := os.Getenv("LOCALAPPDATA")
		if v == "" {
			return "", errors.New("LOCALAPPDATA is not set")
		}
		return v, nil
	}
	// The XDG spec says a relative XDG_DATA_HOME must be ignored.
	if v := os.Getenv("XDG_DATA_HOME"); v != "" && filepath.IsAbs(v) {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
