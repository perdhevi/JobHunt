package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"

	"jobhunt/internal/config"
)

// Listen opens the board's listener, a TCP address or unix:/path.
//
// The loopback checks in Handler stop browsers, not programs: anything that
// can connect may send "Host: 127.0.0.1". On a shared server the board
// therefore listens on a socket only its owner and root can open, rather than
// on a port that other containers and every process on the host could reach.
func Listen(addr string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, config.UnixPrefix)
	if !ok {
		return net.Listen("tcp", addr)
	}
	// A crash leaves the socket file behind, and binding would then fail.
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Connecting needs write permission on the socket. Windows has no such
	// bits, and the socket is for Linux servers anyway.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			ln.Close()
			return nil, fmt.Errorf("restrict %s: %w", path, err)
		}
	}
	return ln, nil
}

// CheckHealth asks the board listener at addr whether the database answers.
// "jobhunt health" runs it as the container health check, so the image needs
// no wget or curl, and it works when the board is on a socket.
func CheckHealth(ctx context.Context, addr string) error {
	network, target := "tcp", addr
	if path, ok := strings.CutPrefix(addr, config.UnixPrefix); ok {
		network, target = "unix", path
	} else if host, port, err := net.SplitHostPort(addr); err == nil && (host == "" || net.ParseIP(host).IsUnspecified()) {
		target = net.JoinHostPort("127.0.0.1", port)
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/healthz", nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("health: %s %s", res.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
