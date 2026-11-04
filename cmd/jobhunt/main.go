// Command jobhunt runs the board server, the MCP stdio server and admin tasks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"jobhunt/internal/api"
	"jobhunt/internal/config"
	"jobhunt/internal/mcpserver"
	"jobhunt/internal/remote"
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
  jobhunt auth signout-all      sign out every browser and connected app
                                of the remote listener, keeping the password
  jobhunt backup [file.tar]     write the database and attachments as a tar
                                archive, to stdout by default, while serving
  jobhunt health                ask the running server whether it's healthy
`

// Timeouts for both listeners. Uploads are at most 20 MB and MCP replies are
// JSON, not streams, so a minute or two is plenty and stalled clients let go.
const (
	readTimeout  = time.Minute
	writeTimeout = 2 * time.Minute
	idleTimeout  = 2 * time.Minute
)

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
	case "auth":
		err = authCmd(args)
	case "backup":
		err = backupCmd(args)
	case "health":
		err = healthCmd()
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
	side := store.SideWindows
	if os.Getenv(config.EnvHostDrive) != "" {
		side = store.SideDocker
	}
	s, err := store.OpenAs(config.LocalPath(p), side)
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
		Handler:           (&api.Server{Store: s, Config: cfg, UI: ui, MCP: h}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	ln, err := api.Listen(cfg.Listen)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // docker stop sends SIGTERM
	defer stop()
	errs := make(chan error, 2)
	go func() { errs <- srv.Serve(ln) }()

	var remoteSrv *http.Server
	if rc := cfg.RemoteMCP; rc.Listen != "" {
		opts := remote.Options{PublicURL: rc.PublicURL, Password: rc.Password, Token: rc.Token,
			RedirectHosts: rc.RedirectHosts, State: s}
		if rc.Board {
			opts.Board = (&api.Server{Store: s, Config: cfg, UI: ui}).Routes()
		}
		if opts.OAuth() {
			if opts.Secret, err = config.AuthSecret(); err != nil {
				return err
			}
		}
		if err := opts.Validate(); err != nil {
			return err
		}
		remoteSrv = &http.Server{Addr: rc.Listen, Handler: remote.Handler(h, opts), ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout}
		go func() { errs <- remoteSrv.ListenAndServe() }()
		if opts.OAuth() {
			log.Printf("remote MCP %s/mcp (OAuth, listening on %s)", strings.TrimSuffix(rc.PublicURL, "/"), rc.Listen)
			if rc.Board {
				log.Printf("remote board %s (password sign-in)", strings.TrimSuffix(rc.PublicURL, "/"))
			}
		} else {
			log.Printf("remote MCP on http://%s/mcp/<token>", rc.Listen)
		}
	}

	log.Printf("database  %s", s.Path())
	if sock, ok := strings.CutPrefix(cfg.Listen, config.UnixPrefix); ok {
		log.Printf("board     socket %s, reached through an SSH tunnel (DEPLOYMENT.md)", sock)
	} else {
		log.Printf("board     http://%s", cfg.Listen)
		log.Printf("local MCP http://%s/mcp", cfg.Listen)
		if *open {
			exec.Command("rundll32", "url.dll,FileProtocolHandler", "http://"+cfg.Listen).Start()
		}
	}

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if remoteSrv != nil {
		remoteSrv.Shutdown(shutdown)
	}
	return srv.Shutdown(shutdown)
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
	if len(args) == 2 && args[0] == "set-db" {
		p, err := config.ValidateDBPath(args[1])
		if err != nil {
			return err
		}
		// The file alone, so values from the environment aren't saved into it.
		file, err := config.LoadFile()
		if err != nil {
			return err
		}
		file.DBPath = p
		if err := config.Save(file); err != nil {
			return err
		}
	} else if len(args) != 0 {
		return errors.New("usage: jobhunt config [set-db <path>]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, err := config.Path()
	if err != nil {
		return err
	}
	fmt.Printf("config    %s\ndatabase  %s\nlisten    %s\n", p, cfg.DBPath, cfg.Listen)
	return nil
}

// authCmd works while the server runs: the server reads revocations from the
// database on every request.
func authCmd(args []string) error {
	if len(args) != 1 || args[0] != "signout-all" {
		return errors.New("usage: jobhunt auth signout-all")
	}
	s, _, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.RevokeAll(context.Background()); err != nil {
		return err
	}
	fmt.Println("Signed out every browser and connected app. Each signs in again with the same password.")
	return nil
}

// backupCmd works while the server runs, so a backup needs no downtime:
//
//	docker compose exec -T jobhunt jobhunt backup > jobhunt.tar
func backupCmd(args []string) (err error) {
	if len(args) > 1 {
		return errors.New("usage: jobhunt backup [file.tar]")
	}
	out := os.Stdout
	if len(args) == 1 && args[0] != "-" {
		// Never overwrite: an older backup may be the only good one.
		f, oerr := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if oerr != nil {
			return oerr
		}
		// err is the named result, so a failed backup leaves no partial file.
		defer func() {
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				os.Remove(args[0])
			}
		}()
		out = f
	} else if fi, err := out.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return errors.New("the backup is a tar archive. Send it to a file: jobhunt backup > jobhunt.tar")
	}
	s, _, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Backup(context.Background(), out)
}

// healthCmd is the container health check. It asks the server, through its
// board listener, whether the database answers.
func healthCmd() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return api.CheckHealth(ctx, cfg.Listen)
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
