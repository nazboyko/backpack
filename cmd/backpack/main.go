// Command backpack reads photos of school papers with a local model and
// shows the week on one page at http://127.0.0.1:7313.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/server"
	"github.com/nazboyko/backpack/internal/store"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("backpack", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", 7313, "port on 127.0.0.1 for the page")
	data := flags.String("data", "", "folder for the papers and photos (default: this computer's app data folder)")
	noOpen := flags.Bool("no-open", false, "do not open the page in the browser")
	showVersion := flags.Bool("version", false, "print the version and stop")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, "backpack", version())
		return nil
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("Pick a port between 1 and 65535, for example: backpack --port 7314")
	}

	// Take the port first: a second Backpack must stop here, before it
	// touches the data folder the first one is using.
	ln, err := net.Listen("tcp", server.ListenAddr(*port))
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("Port %d is busy. Is Backpack already open? Try: backpack --port %d", *port, *port+1)
	}
	if err != nil {
		return fmt.Errorf("Backpack could not use port %d (%v). Try: backpack --port 7314", *port, err)
	}
	defer ln.Close()

	dir := *data
	if dir == "" {
		if dir, err = dataDir(); err != nil {
			return err
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		return fmt.Errorf("Backpack could not open its data folder %s: %v", dir, err)
	}

	client := &reader.Client{BaseURL: os.Getenv("BACKPACK_OLLAMA"), Model: os.Getenv("BACKPACK_MODEL")}
	if h := client.Health(context.Background()); h.Problem != "" {
		fmt.Fprintln(stderr, h.Problem)
	}

	logger := log.New(stderr, "backpack: ", 0)
	srv := server.New(st, client, *port, logger)
	httpSrv := newHTTPServer(*port, srv.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan struct{})
	go func() {
		srv.Run(ctx)
		close(workerDone)
	}()
	closed := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
		close(closed)
	}()

	url := fmt.Sprintf("http://%s/", httpSrv.Addr)
	if *noOpen {
		fmt.Fprintln(stdout, "Backpack is open at", url)
	} else if err := openBrowser(url); err != nil {
		fmt.Fprintln(stdout, "Open this in your browser:", url)
	}
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// Let uploads in flight finish and the worker put an unfinished paper
	// back in line before the program ends.
	<-closed
	<-workerDone
	return nil
}

// newHTTPServer listens on the loopback address only; nothing on the
// network can reach the page.
func newHTTPServer(port int, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              server.ListenAddr(port),
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// dataDir is where papers and photos live when --data is not given.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "backpack"), nil
	case "windows":
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "backpack"), nil
		}
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "backpack"), nil
	}
	return filepath.Join(home, ".local", "share", "backpack"), nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// version reports the module version when installed with go install,
// and "dev" for a local build.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
