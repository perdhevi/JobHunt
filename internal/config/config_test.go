package config

import (
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
