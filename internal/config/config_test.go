package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDrivePathRoundTrip(t *testing.T) {
	t.Setenv(EnvHostDrive, "G")
	const host = `G:\My Drive\JobHunt\jobhunt.db`
	local := LocalPath(host)
	if local != "/drive/My Drive/JobHunt/jobhunt.db" {
		t.Fatalf("local = %q", local)
	}
	if got := HostPath(local); got != host {
		t.Fatalf("host = %q", got)
	}
}

func TestDriveToLocalRefusals(t *testing.T) {
	for p, want := range map[string]string{
		`H:\My Drive\x.db`: "-Drive H",
		`relative\x.db`:    "must start with G:",
		`/etc/passwd`:      "must start with G:",
		`G:\..\etc\x.db`:   "outside the drive",
		`G:\`:              "outside the drive",
	} {
		if _, err := driveToLocal(p, "G"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", p, err, want)
		}
	}
}

// Compose passes every variable it names, so an emptied .env line arrives as
// "" and must switch the setting off rather than fall back to the file.
func TestEmptyEnvironmentOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfig, filepath.Join(dir, "config.json"))
	file := Config{RemoteMCP: RemoteMCP{Listen: "127.0.0.1:8788", Token: "old-token-0123456789abcdef", Board: true, RedirectHosts: []string{"claude.ai"}}}
	if err := Save(file); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRemoteToken, "")
	t.Setenv(EnvRemoteBoard, "")
	t.Setenv(EnvRedirectHosts, " chatgpt.com, ,claude.ai ")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RemoteMCP.Token != "" || c.RemoteMCP.Board {
		t.Errorf("emptied variables ignored: %+v", c.RemoteMCP)
	}
	if c.RemoteMCP.Listen != "127.0.0.1:8788" {
		t.Errorf("unset variable overrode the file: %q", c.RemoteMCP.Listen)
	}
	if got := strings.Join(c.RemoteMCP.RedirectHosts, ","); got != "chatgpt.com,claude.ai" {
		t.Errorf("redirect hosts %q", got)
	}

	// LoadFile is what gets saved back, and it never holds environment values.
	t.Setenv(EnvAuthPassword, "from-the-environment")
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if f.RemoteMCP.Password != "" || f.RemoteMCP.Token != "old-token-0123456789abcdef" {
		t.Errorf("LoadFile applied the environment: %+v", f.RemoteMCP)
	}
}

func TestIsSharedDrive(t *testing.T) {
	const fallback = "1 2 8:64 /docker/x /drive rw,relatime - ext4 /dev/sde rw\n"
	const shared = "1 2 0:60 / /drive rw,relatime - 9p G: rw,aname=drvfs\n"
	if isSharedDrive(fallback, "/drive") {
		t.Error("the Docker VM's own disk must not count as the drive")
	}
	if !isSharedDrive(shared, "/drive") {
		t.Error("a 9p mount should count as the drive")
	}
	if isSharedDrive(shared, "/other") {
		t.Error("a missing mount should not count")
	}
}
