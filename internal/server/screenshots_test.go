//go:build screenshots

package server

// Run with `make screenshots`. It seeds a store with the fixture papers and
// what gemma4:e4b read from them in the live check
// (testdata/papers/gemma4-e4b.readings.json, written by
// `BACKPACK_LIVE_OUT=... make live-check`), serves the real page and has
// headless Chrome take a picture of each state into docs/img. Set CHROME to
// use another browser binary.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

var shotDay = time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)

// slowReader never finishes, so a paper stays "reading" for the picture.
type slowReader struct{ health reader.Health }

func (r slowReader) Read(ctx context.Context, _ []byte, _ time.Time) (reader.Reading, error) {
	<-ctx.Done()
	return reader.Reading{}, ctx.Err()
}

func (r slowReader) Health(context.Context) reader.Health { return r.health }

var ready = reader.Health{Reachable: true, ModelPresent: true, Model: "gemma4:e4b"}

func fixtures(t *testing.T) []string {
	files, _ := filepath.Glob("../../testdata/papers/*.jpg")
	if len(files) != 10 {
		t.Fatalf("%d fixtures", len(files))
	}
	return files
}

// seed adds fixture photos; done ones get what the model read from them.
func seed(t *testing.T, st *store.Store, files []string, status string, at time.Time) {
	t.Helper()
	var readings map[string]reader.Reading
	raw, err := os.ReadFile("../../testdata/papers/gemma4-e4b.readings.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &readings); err != nil {
		t.Fatal(err)
	}
	for i, f := range files {
		data, _ := os.ReadFile(f)
		jpg, err := reader.Prepare(data)
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.Add(jpg, at.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if status == store.Queued {
			continue
		}
		r, ok := readings[strings.TrimSuffix(filepath.Base(f), ".jpg")]
		if !ok {
			t.Fatalf("no reading for %s", f)
		}
		st.Update(p.ID, func(q *store.Paper) error {
			q.Status, q.Reading, q.ReadMs = status, r, 9000+int64(i)*700
			if status == store.Failed {
				q.Reading, q.Error = reader.Reading{}, string(reader.ErrUnreadable)
			}
			return nil
		})
	}
}

// serve runs the real page on a free loopback port until the test ends.
func serve(t *testing.T, st *store.Store, rd Reader, work bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s := New(st, rd, port, log.New(io.Discard, "", 0))
	s.now = func() time.Time { return shotDay }
	web := &http.Server{Handler: s.Handler()}
	go web.Serve(ln)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); web.Close() })
	if work {
		go s.Run(ctx)
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", port)
}

func shoot(t *testing.T, url, name string, height int) {
	t.Helper()
	chrome := os.Getenv("CHROME")
	if chrome == "" {
		chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	out, _ := filepath.Abs(filepath.Join("..", "..", "docs", "img", name+".png"))
	os.MkdirAll(filepath.Dir(out), 0o755)
	profile := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--hide-scrollbars",
		"--user-data-dir="+profile, "--no-first-run", "--force-device-scale-factor=2",
		fmt.Sprintf("--window-size=860,%d", height), "--virtual-time-budget=4000",
		"--screenshot="+out, url)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("chrome: %v\n%s", err, b)
	}
	t.Logf("wrote %s", out)
}

func waitStatus(t *testing.T, st *store.Store, status string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		for _, p := range st.List() {
			if p.Status == status {
				return
			}
		}
	}
	t.Fatalf("no paper became %s", status)
}

func TestScreenshots(t *testing.T) {
	files := fixtures(t)

	t.Run("empty", func(t *testing.T) {
		st, _ := store.Open(t.TempDir())
		shoot(t, serve(t, st, slowReader{ready}, false), "empty", 620)
	})

	t.Run("banner", func(t *testing.T) {
		st, _ := store.Open(t.TempDir())
		down := reader.Health{Model: "gemma4:e4b", Problem: string(reader.ErrOllamaDown)}
		shoot(t, serve(t, st, slowReader{down}, false), "banner", 420)
	})

	t.Run("reading", func(t *testing.T) {
		st, _ := store.Open(t.TempDir())
		seed(t, st, files[:3], store.Done, shotDay.Add(-time.Hour))
		seed(t, st, files[9:], store.Failed, shotDay.Add(-time.Minute))
		seed(t, st, files[5:7], store.Queued, shotDay)
		url := serve(t, st, slowReader{ready}, true)
		waitStatus(t, st, store.Reading)
		shoot(t, url, "reading", 1500)
	})

	t.Run("week", func(t *testing.T) {
		// BACKPACK_SEED_DIR keeps this store, to open it later with
		// backpack --data for the states that need a click.
		dir := os.Getenv("BACKPACK_SEED_DIR")
		if dir == "" {
			dir = t.TempDir()
		}
		st, _ := store.Open(dir)
		seed(t, st, files, store.Done, shotDay.Add(-time.Hour))
		shoot(t, serve(t, st, slowReader{ready}, false), "week", 1800)
	})
}
