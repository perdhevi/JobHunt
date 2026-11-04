package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SQLite's file locks don't cross between Windows and a container: each side
// only sees its own locks, so writing from both corrupts the database. Each side
// therefore keeps timestamped lease files beside the .db and refuses to open
// while the other side has a fresh one. A crashed process's lease simply expires.
//
// Every process has its own lease file. With one file per side, a short-lived
// process such as "jobhunt mcp" removed the lease on exit while "jobhunt serve"
// still held the database, leaving the other side free to open it until the
// next refresh.
const (
	SideWindows = "windows"
	SideDocker  = "docker"

	leaseRefresh = 10 * time.Second
	leaseExpiry  = 30 * time.Second
)

// OpenAs is Open for a real process, taking a lease on this side first.
func OpenAs(path, side string) (*Store, error) {
	release, err := claim(path, side)
	if err != nil {
		return nil, err
	}
	s, err := Open(path)
	if err != nil {
		release()
		return nil, err
	}
	s.release = release
	return s, nil
}

// newLeasePath names this process's lease: <db>.<side>.<random>.lease.
func newLeasePath(path, side string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("%s.%s.%s.lease", path, side, hex.EncodeToString(b))
}

// leases lists one side's lease files, fresh or not. The prefix also matches
// <db>.<side>.lease, the one shared file of older builds, so a side still
// running one of those is seen too.
func leases(path, side string) []string {
	dir, prefix := filepath.Dir(path), filepath.Base(path)+"."+side+"."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".lease") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	return out
}

func sideActive(path, side string) bool {
	for _, f := range leases(path, side) {
		if leaseFresh(f) {
			return true
		}
	}
	return false
}

func writeLease(file string) error {
	return os.WriteFile(file, []byte(strconv.FormatInt(time.Now().UnixMilli(), 10)), 0o644)
}

func leaseFresh(file string) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return err == nil && time.Since(time.UnixMilli(ms)) < leaseExpiry
}

func claim(path, side string) (release func(), err error) {
	other := SideDocker
	if side == SideDocker {
		other = SideWindows
	}
	held := fmt.Errorf("database is in use from %s, and using it from both at once corrupts it. Stop that side or connect through http://127.0.0.1:8787/mcp. A crashed process clears after %s", other, leaseExpiry)

	if sideActive(path, other) {
		return nil, held
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database folder: %w", err)
	}
	// Crashed processes on this side leave expired files behind. A live one
	// that was merely asleep rewrites its file on its next refresh.
	for _, f := range leases(path, side) {
		if !leaseFresh(f) {
			os.Remove(f)
		}
	}
	own := newLeasePath(path, side)
	if err := writeLease(own); err != nil {
		return nil, fmt.Errorf("write lease: %w", err)
	}
	// Both sides may have passed the check together. Looking again after writing
	// means at least one of them sees the other, and refusing both is safe.
	if sideActive(path, other) {
		os.Remove(own)
		return nil, held
	}

	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(leaseRefresh)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				writeLease(own)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		os.Remove(own)
	}, nil
}
