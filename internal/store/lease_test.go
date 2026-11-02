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
	b.Close()
	writeLease(leasePath(path, SideDocker)) // b removed it; a's heartbeat restores it
	if _, err := OpenAs(path, SideWindows); err == nil || !strings.Contains(err.Error(), "in use from docker") {
		t.Fatalf("other side: got %v", err)
	}
	a.Close()
	c, err := OpenAs(path, SideWindows)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	c.Close()
}

func TestStaleLeaseExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobhunt.db")
	old := time.Now().Add(-leaseExpiry - time.Second).UnixMilli()
	if err := os.WriteFile(leasePath(path, SideDocker), []byte(strconv.FormatInt(old, 10)), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenAs(path, SideWindows)
	if err != nil {
		t.Fatalf("a crashed process must not block forever: %v", err)
	}
	s.Close()
}
