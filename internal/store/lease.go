package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SQLite's file locks don't cross between Windows and a container: each side
// only sees its own locks, so writing from both corrupts the database. Each side
// therefore keeps a timestamped lease file beside the .db and refuses to open
// while the other side's lease is fresh. A crashed process's lease simply expires.
const (
	SideWindows = "windows"
	SideDocker  = "docker"

	leaseRefresh = 10 * time.Second
	leaseExpiry  = 30 * time.Second
)

// OpenAs is Open for a real process, taking this side's lease first.
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

func leasePath(path, side string) string { return path + "." + side + ".lease" }

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
	otherFile, ownFile := leasePath(path, other), leasePath(path, side)
	held := fmt.Errorf("database is in use from %s, and using it from both at once corrupts it. Stop that side or connect through http://127.0.0.1:8787/mcp. A crashed process clears after %s", other, leaseExpiry)

	if leaseFresh(otherFile) {
		return nil, held
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database folder: %w", err)
	}
	if err := writeLease(ownFile); err != nil {
		return nil, fmt.Errorf("write lease: %w", err)
	}
	// Both sides may have passed the check together. Looking again after writing
	// means at least one of them sees the other, and refusing both is safe.
	if leaseFresh(otherFile) {
		os.Remove(ownFile)
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
				writeLease(ownFile)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		os.Remove(ownFile)
	}, nil
}
