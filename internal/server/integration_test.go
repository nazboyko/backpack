//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

// cannedOllama answers each fixture photo with its expected reading, found
// by the hash of the prepared photo, the way the real model would answer.
func cannedOllama(t *testing.T, answers map[[32]byte]string) *http.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"version":"0.35.1"}`))
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"gemma4:e4b"}]}`))
		case "/api/chat":
			var req struct {
				Messages []struct {
					Images [][]byte `json:"images"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 1 || len(req.Messages[0].Images) != 1 {
				http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
				return
			}
			answer, ok := answers[sha256.Sum256(req.Messages[0].Images[0])]
			if !ok {
				http.Error(w, `{"error":"unknown photo"}`, http.StatusInternalServerError)
				return
			}
			out, _ := json.Marshal(map[string]any{"done": true, "message": map[string]string{"role": "assistant", "content": answer}})
			w.Write(out)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	srv.Addr = ln.Addr().String()
	return srv
}

func TestEndToEndWithFixtures(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/papers/*.jpg")
	if len(files) != 10 {
		t.Fatalf("%d fixture photos, want 10", len(files))
	}
	answers := map[[32]byte]string{}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	wantEvents, wantActions := 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := reader.Prepare(data)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(strings.TrimSuffix(f, ".jpg") + ".expected.json")
		if err != nil {
			t.Fatal(err)
		}
		var r reader.Reading
		json.Unmarshal(expected, &r)
		wantEvents += len(r.Events)
		wantActions += len(r.Actions)
		answers[sha256.Sum256(prepared)] = string(expected)
		fw, _ := mw.CreateFormFile("photos", filepath.Base(f))
		fw.Write(data)
	}
	mw.Close()

	ollama := cannedOllama(t, answers)

	// The real server on a real loopback port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, &reader.Client{BaseURL: "http://" + ollama.Addr}, port, log.New(io.Discard, "", 0))
	s.now = func() time.Time { return time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local) }
	web := &http.Server{Handler: s.Handler()}
	go web.Serve(ln)
	defer web.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	call := func(method, path, ct string, body io.Reader) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, body)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Drop the whole pile at once.
	resp := call("POST", "/api/papers", mw.FormDataContentType(), &body)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// Poll like the page does until every paper is read.
	var list listResponse
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp := call("GET", "/api/papers", "", nil)
		list = listResponse{}
		json.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		done := 0
		for _, p := range list.Papers {
			if p.Status == store.Failed {
				t.Fatalf("paper failed: %s", p.Error)
			}
			if p.Status == store.Done {
				done++
			}
		}
		if done == 10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 10 read", done)
		}
		time.Sleep(50 * time.Millisecond)
	}

	w := list.Week
	if len(w.Dates) != wantEvents || len(w.Earlier) != 0 {
		t.Fatalf("week has %d dates and %d earlier, want %d and 0", len(w.Dates), len(w.Earlier), wantEvents)
	}
	if len(w.Todos) != wantActions || w.Open != wantActions || w.Nothing != 1 || w.Label != "Week of Oct 26" {
		t.Fatalf("week todos %d open %d nothing %d label %q", len(w.Todos), w.Open, w.Nothing, w.Label)
	}
	for i := 1; i < len(w.Dates); i++ {
		if w.Dates[i].Date < w.Dates[i-1].Date {
			t.Fatalf("dates out of order at %d: %s after %s", i, w.Dates[i].Date, w.Dates[i-1].Date)
		}
	}
	if w.Dates[0].Title != "Pajama Day" {
		t.Errorf("first date %q, want Pajama Day", w.Dates[0].Title)
	}

	// Fix the guessed reading-log date, tick a to-do, drop one event from the calendar.
	var logID, newsletterID string
	for _, p := range list.Papers {
		switch p.Reading.Title {
		case "Reading log":
			logID = p.ID
		case "Class newsletter":
			newsletterID = p.ID
		}
	}
	for _, edit := range []struct{ id, body string }{
		{logID, `{"action":{"index":0,"due":"2026-10-30"}}`},
		{logID, `{"action":{"index":0,"done":true}}`},
		{w.Dates[0].PaperID, fmt.Sprintf(`{"event":{"index":%d,"include":false}}`, w.Dates[0].Index)},
	} {
		resp := call("PATCH", "/api/papers/"+edit.id, "application/json", strings.NewReader(edit.body))
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("patch %s: %d %s", edit.body, resp.StatusCode, b)
		}
		resp.Body.Close()
	}
	got, _ := st.Get(logID)
	if a := got.Reading.Actions[0]; !a.Done || a.Confidence != reader.Checked {
		t.Fatalf("reading log action %+v", a)
	}

	resp = call("GET", "/api/calendar.ics", "", nil)
	cal, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if n := strings.Count(string(cal), "BEGIN:VEVENT"); n != wantEvents-1 {
		t.Fatalf("calendar has %d events, want %d", n, wantEvents-1)
	}
	if strings.Contains(string(cal), "SUMMARY:Pajama Day") {
		t.Fatal("switched-off event is in the calendar")
	}

	// Throw the newsletter away.
	news, _ := st.Get(newsletterID)
	resp = call("DELETE", "/api/papers/"+newsletterID, "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := os.Stat(st.PhotoPath(news)); !os.IsNotExist(err) {
		t.Fatal("newsletter photo still on disk")
	}
	if len(st.List()) != 9 {
		t.Fatal("newsletter still listed")
	}
}
