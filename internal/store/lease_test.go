package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLeaseBlocksOtherSide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobhunt.db")
	a, err := OpenAs(path, SideDocker)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenAs(path, SideDocker)
	if err != nil {
		t.Fatalf("same side must share: %v", err)
	}
	// b exiting, as a "jobhunt mcp" process does, must leave a's lease alone.
	b.Close()
	if _, err := OpenAs(path, SideWindows); err == nil || !strings.Contains(err.Error(), "in use from docker") {
		t.Fatalf("other side after a same-side process exited: got %v", err)
	}
	a.Close()
	c, err := OpenAs(path, SideWindows)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	c.Close()
	if left := leases(path, SideDocker); len(left) != 0 {
		t.Fatalf("lease files left after a clean exit: %v", left)
	}
}

func writeLeaseAt(t *testing.T, file string, at time.Time) {
	t.Helper()
	if err := os.WriteFile(file, []byte(strconv.FormatInt(at.UnixMilli(), 10)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStaleLeaseExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobhunt.db")
	stale := time.Now().Add(-leaseExpiry - time.Second)
	writeLeaseAt(t, newLeasePath(path, SideDocker), stale)
	writeLeaseAt(t, path+".docker.lease", stale) // the shared file of older builds
	s, err := OpenAs(path, SideWindows)
	if err != nil {
		t.Fatalf("a crashed process must not block forever: %v", err)
	}
	s.Close()
}

// A side still running a build from before per-process leases is respected.
func TestOlderBuildLeaseBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobhunt.db")
	writeLeaseAt(t, path+".windows.lease", time.Now())
	if _, err := OpenAs(path, SideDocker); err == nil {
		t.Fatal("opened while an older build on Windows held the database")
	}
}

func TestStaleLeasesOnOwnSideAreRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobhunt.db")
	crashed := newLeasePath(path, SideWindows)
	writeLeaseAt(t, crashed, time.Now().Add(-time.Hour))
	s, err := OpenAs(path, SideWindows)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(crashed); !os.IsNotExist(err) {
		t.Fatalf("a crashed process's lease is still there: %v", err)
	}
}
