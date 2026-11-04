// Package config resolves where the database lives and how the server listens.
//
// The config file sits outside the database folder on purpose: it is per-machine
// (a Drive letter differs between computers) while the database is shared.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	EnvConfig = "JOBHUNT_CONFIG" // overrides the config file location
	EnvDB     = "JOBHUNT_DB"     // overrides dbPath without touching the file

	// Set by compose.yaml. dbPath stays a Windows path so it can be edited as
	// the user sees it, and EnvHostDrive says which drive is mounted at DriveMount.
	EnvHostDrive    = "JOBHUNT_HOST_DRIVE"
	EnvDefaultDB    = "JOBHUNT_DEFAULT_DB"    // used until a dbPath is saved
	EnvListen       = "JOBHUNT_LISTEN"        // overrides listen
	EnvRemoteListen = "JOBHUNT_REMOTE_LISTEN" // overrides remoteMcp.listen
	EnvRemoteToken  = "JOBHUNT_REMOTE_TOKEN"  // overrides remoteMcp.token
	EnvPublicURL    = "JOBHUNT_PUBLIC_URL"    // overrides remoteMcp.publicUrl
	EnvAuthPassword = "JOBHUNT_AUTH_PASSWORD" // overrides remoteMcp.password
	EnvAuthSecret   = "JOBHUNT_AUTH_SECRET"   // overrides the generated auth.secret file

	DefaultListen = "127.0.0.1:8787"
	DriveMount    = "/drive"
)

type Config struct {
	// DBPath is the SQLite file, typically under "G:\My Drive\...".
	DBPath string `json:"dbPath"`
	// Listen is the UI, REST API and local MCP address. Keep it on loopback.
	Listen string `json:"listen"`
	// RemoteMCP optionally opens a second listener for tunnels (see README).
	RemoteMCP RemoteMCP `json:"remoteMcp,omitempty"`
}

// RemoteMCP is a listener that serves only MCP and always requires a login or
// the token, so a tunnel or proxy pointed at it can never reach the UI or REST API.
type RemoteMCP struct {
	Listen string `json:"listen,omitempty"`
	// PublicURL and Password turn on OAuth, which Claude and ChatGPT connectors use.
	PublicURL string `json:"publicUrl,omitempty"`
	Password  string `json:"password,omitempty"`
	// Token is a static bearer token for clients that can send a header.
	Token string `json:"token,omitempty"`
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
	if v := os.Getenv(EnvListen); v != "" {
		c.Listen = v
	}
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if v := os.Getenv(EnvRemoteListen); v != "" {
		c.RemoteMCP.Listen = v
	}
	if v := os.Getenv(EnvRemoteToken); v != "" {
		c.RemoteMCP.Token = v
	}
	if v := os.Getenv(EnvPublicURL); v != "" {
		c.RemoteMCP.PublicURL = v
	}
	if v := os.Getenv(EnvAuthPassword); v != "" {
		c.RemoteMCP.Password = v
	}
	if v := os.Getenv(EnvDB); v != "" {
		c.DBPath = v
	}
	if c.DBPath == "" {
		c.DBPath = os.Getenv(EnvDefaultDB)
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

// AuthSecret returns the key that signs OAuth clients and tokens. It's generated
// once and kept beside the config file, so restarts don't log clients out.
// Deleting the file, or changing the password, does.
func AuthSecret() ([]byte, error) {
	if v := os.Getenv(EnvAuthSecret); v != "" {
		return []byte(v), nil
	}
	p, err := Path()
	if err != nil {
		return nil, err
	}
	p = filepath.Join(filepath.Dir(p), "auth.secret")
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) >= 32 {
		return bytes.TrimSpace(b), nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	key := []byte(rand.Text() + rand.Text())
	if err := os.WriteFile(p, append(key, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("save auth secret: %w", err)
	}
	return key, nil
}

// ValidateDBPath normalises a user-supplied path and checks it can be used.
// It fails when the volume is missing, which is what a not-yet-started Google
// Drive looks like, rather than creating a stray folder somewhere else.
func ValidateDBPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if p == "" {
		return "", errors.New("database path is empty")
	}
	if drive := os.Getenv(EnvHostDrive); drive != "" {
		return validateDrivePath(p, drive)
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

// LocalPath turns a validated dbPath into the path this process opens. They
// differ only in a container, where the Windows drive is mounted at DriveMount.
func LocalPath(p string) string {
	drive := os.Getenv(EnvHostDrive)
	if drive == "" {
		return p
	}
	local, err := driveToLocal(p, drive)
	if err != nil {
		return p
	}
	return local
}

// HostPath is the reverse of LocalPath, for showing the user a path they know.
func HostPath(local string) string {
	drive := os.Getenv(EnvHostDrive)
	rest, ok := strings.CutPrefix(local, DriveMount+"/")
	if drive == "" || !ok {
		return local
	}
	return strings.ToUpper(drive[:1]) + `:\` + strings.ReplaceAll(rest, "/", `\`)
}

func driveToLocal(p, drive string) (string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) < 3 || p[1] != ':' || p[2] != '/' {
		return "", fmt.Errorf(`database path must start with %s:\ : %s`, strings.ToUpper(drive[:1]), p)
	}
	if !strings.EqualFold(p[:1], drive[:1]) {
		return "", fmt.Errorf(`only %s: is mounted. To use %s:, run scripts\docker-up.ps1 -Drive %s`,
			strings.ToUpper(drive[:1]), strings.ToUpper(p[:1]), strings.ToUpper(p[:1]))
	}
	local := path.Clean(DriveMount + "/" + p[3:])
	if !strings.HasPrefix(local, DriveMount+"/") {
		return "", fmt.Errorf("database path is outside the drive: %s", p)
	}
	return local, nil
}

// validateDrivePath refuses to run on Docker's fallback disk. When a bind source
// isn't shared, Docker mounts an empty folder from its own VM without any error,
// and a database created there would look fine but never reach Google Drive.
func validateDrivePath(p, drive string) (string, error) {
	local, err := driveToLocal(p, drive)
	if err != nil {
		return "", err
	}
	info, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	if !isSharedDrive(string(info), DriveMount) {
		return "", fmt.Errorf(`drive %s: isn't shared with this container. Run scripts\docker-up.ps1`, strings.ToUpper(drive[:1]))
	}
	if fi, err := os.Stat(local); err == nil && fi.IsDir() {
		return "", fmt.Errorf("database path is a folder, expected a file: %s", HostPath(local))
	}
	return HostPath(local), nil
}

// isSharedDrive reports whether mount is a filesystem passed in from Windows,
// as opposed to a disk inside the Docker VM.
func isSharedDrive(mountinfo, mount string) bool {
	for _, line := range strings.Split(mountinfo, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !ok || len(fields) < 5 || fields[4] != mount {
			continue
		}
		fstype, _, _ := strings.Cut(after, " ")
		return fstype == "9p" || fstype == "virtiofs" || fstype == "drvfs" || strings.HasPrefix(fstype, "fuse")
	}
	return false
}
