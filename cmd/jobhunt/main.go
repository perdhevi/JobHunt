// Command jobhunt runs the board server, the MCP stdio server and admin tasks.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"jobhunt/internal/api"
	"jobhunt/internal/config"
	"jobhunt/internal/mcpserver"
	"jobhunt/internal/store"
	"jobhunt/web"
)

const version = "0.1.0"

const usage = `jobhunt %s

  jobhunt serve [--open]        run the board at the configured address
  jobhunt mcp                   run the MCP server over stdio
  jobhunt config                show the configuration
  jobhunt config set-db <path>  point at a database file
  jobhunt import <backup.json> [--files <dir>]
                                load a tracker JSON backup (safe to re-run)
`

func main() {
	log.SetFlags(0)
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "mcp":
		err = runMCP()
	case "config":
		err = configCmd(args)
	case "import":
		err = importCmd(args)
	default:
		fmt.Fprintf(os.Stderr, usage, version)
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("jobhunt: %v", err)
	}
}

func openStore() (*store.Store, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, cfg, err
	}
	p, err := config.ValidateDBPath(cfg.DBPath)
	if err != nil {
		return nil, cfg, err
	}
	s, err := store.Open(p)
	return s, cfg, err
}

func serve(args []string) error {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	open := fl.Bool("open", false, "open the board in the default browser")
	fl.Parse(args)

	s, cfg, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ui, err := web.FS()
	if err != nil {
		return err
	}
	h := mcpserver.HTTPHandler(mcpserver.New(s, version))
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           (&api.Server{Store: s, Config: cfg, UI: ui, MCP: h}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	errs := make(chan error, 2)
	go func() { errs <- srv.ListenAndServe() }()

	var remote *http.Server
	if cfg.RemoteMCP.Listen != "" {
		if len(cfg.RemoteMCP.Token) < 24 {
			return errors.New("remoteMcp.token must be at least 24 characters")
		}
		remote = &http.Server{Addr: cfg.RemoteMCP.Listen, Handler: remoteHandler(h, cfg.RemoteMCP.Token), ReadHeaderTimeout: 10 * time.Second}
		go func() { errs <- remote.ListenAndServe() }()
		log.Printf("remote MCP on http://%s/mcp/<token>", cfg.RemoteMCP.Listen)
	}

	log.Printf("database  %s", s.Path())
	log.Printf("board     http://%s", cfg.Listen)
	log.Printf("local MCP http://%s/mcp", cfg.Listen)
	if *open {
		exec.Command("rundll32", "url.dll,FileProtocolHandler", "http://"+cfg.Listen).Start()
	}

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if remote != nil {
		remote.Shutdown(shutdown)
	}
	return srv.Shutdown(shutdown)
}

// remoteHandler serves only MCP, and only with the token. The token may be in
// the path because ChatGPT's connector has no static-header option; a bearer
// header works for clients that support one.
func remoteHandler(next http.Handler, token string) http.Handler {
	ok := func(got string) bool { return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/mcp" && ok(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")):
		case strings.HasPrefix(r.URL.Path, "/mcp/") && ok(strings.TrimPrefix(r.URL.Path, "/mcp/")):
			r = r.Clone(r.Context())
			r.URL.Path = "/mcp"
		default:
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// runMCP talks to the database directly, so MCP clients work whether or not the
// board is running. SQLite locks and the unique indexes keep processes safe.
// Logs go to stderr because stdout carries the protocol.
func runMCP() error {
	s, _, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = mcpserver.New(s, version).Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func configCmd(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) == 2 && args[0] == "set-db" {
		p, err := config.ValidateDBPath(args[1])
		if err != nil {
			return err
		}
		cfg.DBPath = p
		if err := config.Save(cfg); err != nil {
			return err
		}
	} else if len(args) != 0 {
		return errors.New("usage: jobhunt config [set-db <path>]")
	}
	p, err := config.Path()
	if err != nil {
		return err
	}
	fmt.Printf("config    %s\ndatabase  %s\nlisten    %s\n", p, cfg.DBPath, cfg.Listen)
	return nil
}

func importCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jobhunt import <backup.json> [--files <dir>]")
	}
	fl := flag.NewFlagSet("import", flag.ExitOnError)
	files := fl.String("files", "", "folder holding attachment files named by id")
	fl.Parse(args[1:])

	b, err := store.ReadBackup(args[0])
	if err != nil {
		return err
	}
	s, _, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	r, err := s.Import(context.Background(), b, *files)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d applications, %d documents, %d files\n", r.Applications, r.Documents, r.Files)
	fmt.Printf("skipped  %d already present, %d sample entries\n", r.SkippedExisting, r.SkippedSample)
	for _, c := range r.Conflicts {
		fmt.Println("conflict:", c)
	}
	for _, m := range r.MissingFiles {
		fmt.Println("missing file:", m)
	}
	return nil
}
