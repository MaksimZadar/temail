package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "does-not-exist"))
	got, err := m.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Errorf("Load = %+v, want %+v", got, Default())
	}
	if _, err := os.Stat(m.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load must not create the file, stat err = %v", err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	m := NewManager(t.TempDir())
	want := validConfig()
	if err := m.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveRejectsInvalidConfig(t *testing.T) {
	m := NewManager(t.TempDir())
	c := validConfig()
	c.Bridge[0].TLSMode = "bogus"
	if err := m.Save(c); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Save err = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(m.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("invalid Save must not write a file, stat err = %v", err)
	}
}

func TestSaveKeepsExistingFileWhenValidationFails(t *testing.T) {
	m := NewManager(t.TempDir())
	good := validConfig()
	if err := m.Save(good); err != nil {
		t.Fatal(err)
	}
	bad := validConfig()
	bad.Bridge = append(bad.Bridge, bad.Bridge[0]) // duplicate id
	if err := m.Save(bad); err == nil {
		t.Fatal("want error for duplicate id")
	}
	got, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, good) {
		t.Errorf("existing config changed after failed Save")
	}
}

func TestSaveFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "temail")
	m := NewManager(dir)
	if err := m.Save(validConfig()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
}

func TestSaveIsAtomicNoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	for i := 0; i < 3; i++ {
		c := validConfig()
		c.Bridge[0].IMAPPort = 1000 + i
		if err := m.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir contents = %v, want only %s", names, FileName)
	}
	got, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Bridge[0].IMAPPort != 1002 {
		t.Errorf("last write not visible, IMAPPort = %d", got.Bridge[0].IMAPPort)
	}
}

func TestSaveWritesNoSecrets(t *testing.T) {
	m := NewManager(t.TempDir())
	const secret = "hunter2-BRIDGE-SECRET"
	c := validConfig()
	c.Bridge[0].Username = "alice@proton.me"
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("secret leaked into config file")
	}
	if !json.Valid(data) {
		t.Fatal("config file is not valid JSON")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	raw := `{"version":1,"bridge":[{"id":"a","host":"127.0.0.1","imapPort":1143,"smtpPort":1025,"tlsMode":"starttls","username":"u","password":"oops"}],"llm":{"defaultProvider":"","providers":[]}}`
	if err := os.WriteFile(m.Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("Load err = %v, want unknown-field error mentioning password", err)
	}
}

func TestLoadAppliesDefaultsForOmittedBridgeFields(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	raw := `{"version":1,"bridge":[{"id":"a","username":"u"}],"llm":{"providers":[]}}`
	if err := os.WriteFile(m.Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := m.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := DefaultBridgeAccount("a", "u")
	if got.Bridge[0] != want {
		t.Errorf("got %+v, want defaults %+v", got.Bridge[0], want)
	}
}

func TestLoadInvalidContent(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"malformed json", `{"version":`},
		{"invalid semantics", `{"version":1,"bridge":[],"llm":{"defaultProvider":"ghost","providers":[]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			if err := os.WriteFile(m.Path(), []byte(tt.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Load(); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestDefaultManagerUsesUserConfigDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base) // os.UserConfigDir on Linux
	t.Setenv("HOME", base)
	m, err := DefaultManager()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		if want := filepath.Join(base, "temail", "config.json"); m.Path() != want {
			t.Errorf("Path() = %q, want %q", m.Path(), want)
		}
	}
}
