// Package config resolves where the database lives and how the server listens.
//
// The config file sits outside the database folder on purpose: it is per-machine
// (a Drive letter differs between computers) while the database is shared.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	EnvConfig = "JOBHUNT_CONFIG" // overrides the config file location
	EnvDB     = "JOBHUNT_DB"     // overrides dbPath without touching the file

	DefaultListen = "127.0.0.1:8787"
)

type Config struct {
	// DBPath is the SQLite file, typically under "G:\My Drive\...".
	DBPath string `json:"dbPath"`
	// Listen is the UI, REST API and local MCP address. Keep it on loopback.
	Listen string `json:"listen"`
	// RemoteMCP optionally opens a second listener for tunnels (see README).
	RemoteMCP RemoteMCP `json:"remoteMcp,omitempty"`
}

// RemoteMCP is a listener that serves only MCP and always requires the token,
// so a tunnel pointed at it can never reach the UI or REST API.
type RemoteMCP struct {
	Listen string `json:"listen,omitempty"`
	Token  string `json:"token,omitempty"`
}

// Path returns the config file location.
func Path() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "JobHunt", "config.json"), nil
}

// Load reads the config file, tolerating its absence, and applies defaults and
// the JOBHUNT_DB override.
func Load() (Config, error) {
	var c Config
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", p, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return c, err
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if v := os.Getenv(EnvDB); v != "" {
		c.DBPath = v
	}
	if c.DBPath == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return c, err
		}
		c.DBPath = filepath.Join(dir, "JobHunt", "jobhunt.db")
	}
	return c, nil
}

// Save writes the config file atomically so a crash can't leave it half-written.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ValidateDBPath normalises a user-supplied path and checks it can be used.
// It fails when the volume is missing, which is what a not-yet-started Google
// Drive looks like, rather than creating a stray folder somewhere else.
func ValidateDBPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if p == "" {
		return "", errors.New("database path is empty")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("database path must be absolute: %s", p)
	}
	p = filepath.Clean(p)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return "", fmt.Errorf("database path is a folder, expected a file: %s", p)
	}
	if v := filepath.VolumeName(p); v != "" {
		if _, err := os.Stat(v + string(filepath.Separator)); err != nil {
			return "", fmt.Errorf("drive %s is not available. Is Google Drive running?", v)
		}
	}
	return p, nil
}
