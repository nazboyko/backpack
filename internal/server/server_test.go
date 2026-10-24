package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

const host = "127.0.0.1:7313"

var now = time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)

// stubReader answers every photo with the same reading or error.
type stubReader struct {
	mu      sync.Mutex
	reading reader.Reading
	err     error
	calls   int
}

func (s *stubReader) Read(ctx context.Context, jpg []byte, now time.Time) (reader.Reading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.reading, s.err
}

func (s *stubReader) Health(ctx context.Context) reader.Health {
	return reader.Health{Reachable: true, ModelPresent: true, Model: "stub"}
}

func newServer(t *testing.T, rd Reader) (*Server, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, rd, 7313, log.New(io.Discard, "", 0))
	s.now = func() time.Time { return now }
	return s, s.Handler()
}

func do(h http.Handler, method, path string, body io.Reader, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func photo(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 40, 60)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// uploadBody builds a multipart body with one "photos" part per file.
func uploadBody(t *testing.T, files map[string][]byte) (io.Reader, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for name, data := range files {
		fw, err := mw.CreateFormFile("photos", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(data)
	}
	mw.Close()
	return &b, mw.FormDataContentType()
}

func upload(t *testing.T, h http.Handler, files map[string][]byte) *httptest.ResponseRecorder {
	body, ct := uploadBody(t, files)
	return do(h, "POST", "/api/papers", body, "Content-Type", ct)
}

// readyPaper stores a paper that has been read, for the edit tests.
func readyPaper(t *testing.T, s *Server) store.Paper {
	t.Helper()
	p, err := s.store.Add(photo(t), now)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.store.Update(p.ID, func(q *store.Paper) error {
		q.Status = store.Done
		q.Reading = reader.Reading{
			Title: "Field trip", Summary: "Museum trip.", Kind: "form",
			Events: []reader.Event{
				{Title: "Field trip", Date: "2026-11-04", StartTime: "09:00", Confidence: reader.Read},
				{Title: "Slip due", Date: "2026-10-30", Confidence: reader.Guessed, Raw: "Friday"},
			},
			Actions: []reader.Action{
				{Text: "Sign the slip", Kind: "sign", Due: "2026-10-30", Confidence: reader.Guessed, Raw: "Friday"},
				{Text: "Send $8", Kind: "pay"},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response %d is not JSON: %s", rec.Code, rec.Body)
	}
	return v
}

func TestUploadTwoPhotosQueuesTwoPapers(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	rec := upload(t, h, map[string][]byte{"a.jpg": photo(t), "b.jpg": photo(t)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	out := decodeJSON[uploadResponse](t, rec)
	if len(out.Papers) != 2 || len(out.Problems) != 0 {
		t.Fatalf("response %+v", out)
	}
	for _, p := range s.store.List() {
		if p.Status != store.Queued {
			t.Errorf("paper %s is %s, want queued", p.ID, p.Status)
		}
	}
	if len(s.store.List()) != 2 {
		t.Fatal("papers not stored")
	}
}

func TestUploadNonImageIs400WithMessage(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	rec := upload(t, h, map[string][]byte{"slip.pdf": []byte("%PDF-1.7")})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	out := decodeJSON[map[string]any](t, rec)
	if out["error"] != string(reader.ErrNotPhoto) {
		t.Fatalf("error %q", out["error"])
	}
	if len(s.store.List()) != 0 {
		t.Fatal("a non-photo was stored")
	}
}

func TestUploadKeepsGoodPhotosFromAMixedDrop(t *testing.T) {
	_, h := newServer(t, &stubReader{})
	rec := upload(t, h, map[string][]byte{"good.jpg": photo(t), "notes.txt": []byte("hello")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	out := decodeJSON[uploadResponse](t, rec)
	if len(out.Papers) != 1 || len(out.Problems) != 1 || out.Problems[0].File != "notes.txt" {
		t.Fatalf("response %+v", out)
	}
}

func TestUploadWithoutFilesIs400(t *testing.T) {
	_, h := newServer(t, &stubReader{})
	if rec := do(h, "POST", "/api/papers", strings.NewReader("x"), "Content-Type", "text/plain"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestListHasPapersAndWeek(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	readyPaper(t, s)
	rec := do(h, "GET", "/api/papers", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	out := decodeJSON[listResponse](t, rec)
	if len(out.Papers) != 1 || len(out.Week.Dates) != 2 || out.Week.Open != 2 || out.Week.Label != "Week of Oct 26" {
		t.Fatalf("list %+v", out)
	}
}

func patchReq(t *testing.T, h http.Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(h, "PATCH", "/api/papers/"+id, strings.NewReader(body), "Content-Type", "application/json")
}

func TestPatchEachField(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	steps := []struct {
		body  string
		check func(store.Paper) bool
	}{
		{`{"title":"  Museum   trip "}`, func(p store.Paper) bool { return p.Reading.Title == "Museum trip" && p.Edited }},
		{`{"summary":"Bus at nine."}`, func(p store.Paper) bool { return p.Reading.Summary == "Bus at nine." }},
		{`{"event":{"index":1,"date":"2026-10-30"}}`, func(p store.Paper) bool {
			e := p.Reading.Events[1]
			return e.Date == "2026-10-30" && e.Confidence == reader.Checked && e.Raw == ""
		}},
		{`{"event":{"index":0,"start_time":"08:45","end_time":"14:15"}}`, func(p store.Paper) bool {
			e := p.Reading.Events[0]
			return e.StartTime == "08:45" && e.EndTime == "14:15" && e.Confidence == reader.Checked
		}},
		{`{"event":{"index":0,"title":"Science museum","location":"Riverside"}}`, func(p store.Paper) bool {
			return p.Reading.Events[0].Title == "Science museum" && p.Reading.Events[0].Location == "Riverside"
		}},
		{`{"event":{"index":0,"include":false}}`, func(p store.Paper) bool {
			return p.Reading.Events[0].Include != nil && !*p.Reading.Events[0].Include
		}},
		{`{"event":{"title":"Pick-up","date":"2026-11-04","start_time":"14:30"}}`, func(p store.Paper) bool {
			return len(p.Reading.Events) == 3 && p.Reading.Events[2].Title == "Pick-up" && p.Reading.Events[2].Confidence == reader.Checked
		}},
		{`{"event":{"index":2,"remove":true}}`, func(p store.Paper) bool { return len(p.Reading.Events) == 2 }},
		{`{"action":{"index":0,"done":true}}`, func(p store.Paper) bool { return p.Reading.Actions[0].Done }},
		{`{"action":{"index":0,"due":"2026-10-29"}}`, func(p store.Paper) bool {
			a := p.Reading.Actions[0]
			return a.Due == "2026-10-29" && a.Confidence == reader.Checked && a.Raw == ""
		}},
		{`{"action":{"index":1,"text":"Send $8 in an envelope"}}`, func(p store.Paper) bool {
			return p.Reading.Actions[1].Text == "Send $8 in an envelope"
		}},
		{`{"action":{"text":"Pack a lunch","kind":"bring"}}`, func(p store.Paper) bool {
			return len(p.Reading.Actions) == 3 && p.Reading.Actions[2].Kind == "bring"
		}},
		{`{"action":{"index":2,"remove":true}}`, func(p store.Paper) bool { return len(p.Reading.Actions) == 2 }},
	}
	for _, st := range steps {
		rec := patchReq(t, h, p.ID, st.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", st.body, rec.Code, rec.Body)
		}
		got, _ := s.store.Get(p.ID)
		if !st.check(got) {
			t.Fatalf("%s: paper after patch %+v", st.body, got.Reading)
		}
	}
}

func TestPatchRejectsBadInput(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	cases := map[string]int{
		`{"event":{"index":0,"date":"Friday"}}`:        http.StatusBadRequest,
		`{"event":{"index":0,"start_time":"3 pm"}}`:    http.StatusBadRequest,
		`{"event":{"index":9,"title":"x"}}`:            http.StatusBadRequest,
		`{"event":{"title":"No day"}}`:                 http.StatusBadRequest,
		`{"title":"   "}`:                              http.StatusBadRequest,
		`{"action":{"index":-1,"done":true}}`:          http.StatusBadRequest,
		`{"surprise":true}`:                            http.StatusBadRequest,
		`{}`:                                           http.StatusBadRequest,
		`not json`:                                     http.StatusBadRequest,
		`{"action":{"index":0,"due":"next Thursday"}}`: http.StatusBadRequest,
	}
	before, _ := s.store.Get(p.ID)
	for body, want := range cases {
		rec := patchReq(t, h, p.ID, body)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", body, rec.Code, want)
		}
		if msg := decodeJSON[map[string]string](t, rec)["error"]; msg == "" {
			t.Errorf("%s: no message", body)
		}
	}
	after, _ := s.store.Get(p.ID)
	if after.Edited != before.Edited || len(after.Reading.Events) != len(before.Reading.Events) {
		t.Fatal("a rejected patch changed the paper")
	}
	if rec := patchReq(t, h, "nope", `{"title":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown paper: %d", rec.Code)
	}
}

func TestPatchWhileReadingIsConflict(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p, _ := s.store.Add(photo(t), now)
	if rec := patchReq(t, h, p.ID, `{"title":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDoneToggleIsNotAnEdit(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	patchReq(t, h, p.ID, `{"action":{"index":1,"done":true}}`)
	if got, _ := s.store.Get(p.ID); got.Edited {
		t.Fatal("checking a box marked the reading as edited")
	}
}

func TestDeleteRemovesPhoto(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	path := s.store.PhotoPath(p)
	if rec := do(h, "DELETE", "/api/papers/"+p.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("photo still on disk")
	}
	if rec := do(h, "DELETE", "/api/papers/"+p.ID, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
}

func TestHostCheck(t *testing.T) {
	_, h := newServer(t, &stubReader{})
	for _, bad := range []string{"evil.example", "evil.example:7313", "127.0.0.1:8080", ""} {
		req := httptest.NewRequest("GET", "/api/papers", nil)
		req.Host = bad
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("host %q: status %d", bad, rec.Code)
		}
	}
	for _, good := range []string{"127.0.0.1:7313", "localhost:7313"} {
		req := httptest.NewRequest("GET", "/api/papers", nil)
		req.Host = good
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("host %q: status %d", good, rec.Code)
		}
	}
}

func TestOtherSitesCannotChangeAnything(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	cases := [][]string{
		{"Origin", "http://evil.example"},
		{"Origin", "http://127.0.0.1:7314"},
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"},
	}
	for _, hdr := range cases {
		if rec := do(h, "DELETE", "/api/papers/"+p.ID, nil, hdr...); rec.Code != http.StatusForbidden {
			t.Errorf("%v: delete status %d", hdr, rec.Code)
		}
		body, ct := uploadBody(t, map[string][]byte{"a.jpg": photo(t)})
		if rec := do(h, "POST", "/api/papers", body, "Content-Type", ct, hdr[0], hdr[1]); rec.Code != http.StatusForbidden {
			t.Errorf("%v: upload status %d", hdr, rec.Code)
		}
	}
	if _, err := s.store.Get(p.ID); err != nil {
		t.Fatal("paper deleted by another site")
	}
	// The page itself sends its own origin and same-origin.
	rec := do(h, "PATCH", "/api/papers/"+p.ID, strings.NewReader(`{"title":"ok"}`),
		"Origin", "http://127.0.0.1:7313", "Sec-Fetch-Site", "same-origin")
	if rec.Code != http.StatusOK {
		t.Fatalf("own page: %d", rec.Code)
	}
}

func TestCalendar(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	rec := do(h, "GET", "/api/calendar.ics", nil)
	if ct := rec.Header().Get("Content-Type"); ct != "text/calendar; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "backpack.ics") {
		t.Fatalf("disposition %q", cd)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") || strings.Count(body, "BEGIN:VEVENT") != 2 {
		t.Fatalf("body:\n%s", body)
	}
	one := do(h, "GET", "/api/calendar.ics?ids="+p.ID+"-0", nil).Body.String()
	if strings.Count(one, "BEGIN:VEVENT") != 1 || !strings.Contains(one, "SUMMARY:Field trip") {
		t.Fatalf("filtered body:\n%s", one)
	}
}

func TestReread(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	done := readyPaper(t, s)
	if rec := do(h, "POST", "/api/papers/"+done.ID+"/reread", nil); rec.Code != http.StatusConflict {
		t.Fatalf("reread of a read paper: %d", rec.Code)
	}
	failed, _ := s.store.Add(photo(t), now)
	s.store.Update(failed.ID, func(q *store.Paper) error { q.Status, q.Error = store.Failed, "x"; return nil })
	if rec := do(h, "POST", "/api/papers/"+failed.ID+"/reread", nil); rec.Code != http.StatusOK {
		t.Fatalf("reread of a failed paper: %d", rec.Code)
	}
	if got, _ := s.store.Get(failed.ID); got.Status != store.Queued || got.Error != "" {
		t.Fatalf("after reread %+v", got)
	}
}

func TestPhoto(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	rec := do(h, "GET", "/api/papers/"+p.ID+"/photo", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.Len() == 0 {
		t.Fatalf("photo: %d %q %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec := do(h, "GET", "/api/papers/../../etc/passwd/photo", nil); rec.Code == http.StatusOK {
		t.Fatal("path tricks reached a file")
	}
}

func TestPageIsServed(t *testing.T) {
	_, h := newServer(t, &stubReader{})
	rec := do(h, "GET", "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Backpack</title>") {
		t.Fatalf("index: %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("csp %q", csp)
	}
}

// fakeOllama answers health questions the way Ollama does.
func fakeOllama(models ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"version":"0.35.1"}`))
		case "/api/tags":
			var names []string
			for _, m := range models {
				names = append(names, `{"name":"`+m+`"}`)
			}
			w.Write([]byte(`{"models":[` + strings.Join(names, ",") + `]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestHealthUpAndDown(t *testing.T) {
	up := fakeOllama("gemma4:e4b")
	defer up.Close()
	_, h := newServer(t, &reader.Client{BaseURL: up.URL})
	got := decodeJSON[reader.Health](t, do(h, "GET", "/api/health", nil))
	if !got.Reachable || !got.ModelPresent || got.Problem != "" {
		t.Fatalf("up: %+v", got)
	}

	down := fakeOllama()
	url := down.URL
	down.Close()
	_, h = newServer(t, &reader.Client{BaseURL: url})
	got = decodeJSON[reader.Health](t, do(h, "GET", "/api/health", nil))
	if got.Reachable || !strings.Contains(got.Problem, "ollama serve") {
		t.Fatalf("down: %+v", got)
	}
}

func TestWorkerReadsQueuedPapers(t *testing.T) {
	rd := &stubReader{reading: reader.Reading{Title: "Picture day", Events: []reader.Event{{Title: "Picture day", Date: "2026-11-03"}}}}
	s, h := newServer(t, rd)
	upload(t, h, map[string][]byte{"a.jpg": photo(t)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	p := waitFor(t, s, store.Done)
	if p.Reading.Title != "Picture day" {
		t.Fatalf("reading %+v", p.Reading)
	}
}

func TestWorkerMarksFailedWithPlainMessage(t *testing.T) {
	rd := &stubReader{err: reader.ErrUnreadable}
	s, h := newServer(t, rd)
	upload(t, h, map[string][]byte{"a.jpg": photo(t)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	p := waitFor(t, s, store.Failed)
	if p.Error != string(reader.ErrUnreadable) {
		t.Fatalf("error %q", p.Error)
	}
}

func TestWorkerWaitsWhenOllamaIsDown(t *testing.T) {
	rd := &stubReader{err: reader.ErrOllamaDown}
	s, h := newServer(t, rd)
	upload(t, h, map[string][]byte{"a.jpg": photo(t)})
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		rd.mu.Lock()
		calls := rd.calls
		rd.mu.Unlock()
		if calls > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	if p := s.store.List()[0]; p.Status != store.Queued {
		t.Fatalf("status %s, want queued while Ollama is away", p.Status)
	}
}

func waitFor(t *testing.T, s *Server, status string) store.Paper {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ps := s.store.List(); len(ps) > 0 && ps[0].Status == status {
			return ps[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("paper never became %s: %+v", status, s.store.List())
	return store.Paper{}
}

func TestListenAddrIsLoopback(t *testing.T) {
	for _, port := range []int{7313, 1, 65535} {
		if a := ListenAddr(port); !strings.HasPrefix(a, "127.0.0.1:") {
			t.Fatalf("ListenAddr(%d) = %q", port, a)
		}
	}
}

func TestUploadCutShortKeepsWhatArrived(t *testing.T) {
	defer func(n int64) { maxUpload = n }(maxUpload)
	one := photo(t)
	maxUpload = int64(len(one))*2 + 1200 // room for about two photos and their headers
	s, h := newServer(t, &stubReader{})
	files := map[string][]byte{}
	for i := 0; i < 6; i++ {
		files[fmt.Sprintf("p%d.jpg", i)] = one
	}
	rec := upload(t, h, files)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	out := decodeJSON[uploadResponse](t, rec)
	if len(out.Papers) == 0 || len(out.Papers) == 6 || len(out.Problems) != 1 {
		t.Fatalf("papers %d problems %+v", len(out.Papers), out.Problems)
	}
	if len(s.store.List()) != len(out.Papers) {
		t.Fatal("reply does not match what was stored")
	}
	select {
	case <-s.wake:
	default:
		t.Fatal("the worker was not told about the photos that arrived")
	}
}

func TestEditingUndatedEventKeepsItAGuess(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p := readyPaper(t, s)
	s.store.Update(p.ID, func(q *store.Paper) error {
		q.Reading.Events = append(q.Reading.Events, reader.Event{Title: "Concert", Confidence: reader.Guessed, Raw: "after break"})
		return nil
	})
	if rec := patchReq(t, h, p.ID, `{"event":{"index":2,"start_time":"18:00"}}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	got, _ := s.store.Get(p.ID)
	if e := got.Reading.Events[2]; e.Confidence != reader.Guessed || e.Raw != "after break" {
		t.Fatalf("event %+v", e)
	}
}

func TestEmptyCalendarIs404(t *testing.T) {
	_, h := newServer(t, &stubReader{})
	rec := do(h, "GET", "/api/calendar.ics", nil)
	if rec.Code != http.StatusNotFound || decodeJSON[map[string]string](t, rec)["error"] == "" {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestPatchFailedPaperSaysReadAgain(t *testing.T) {
	s, h := newServer(t, &stubReader{})
	p, _ := s.store.Add(photo(t), now)
	s.store.Update(p.ID, func(q *store.Paper) error { q.Status = store.Failed; return nil })
	rec := patchReq(t, h, p.ID, `{"title":"x"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Read again") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRejectedRequestsStillCarrySafetyHeaders(t *testing.T) {
	_, h := newServer(t, &stubReader{})
	req := httptest.NewRequest("GET", "/api/papers", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("headers %v", rec.Header())
	}
}

func TestReadItemsGetStableIDs(t *testing.T) {
	rd := &stubReader{reading: reader.Reading{Title: "Trip",
		Events:  []reader.Event{{Title: "Trip", Date: "2026-11-04"}},
		Actions: []reader.Action{{Text: "Sign"}}}}
	s, h := newServer(t, rd)
	upload(t, h, map[string][]byte{"a.jpg": photo(t)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	p := waitFor(t, s, store.Done)
	if p.Reading.Events[0].ID == "" || p.Reading.Actions[0].ID == "" {
		t.Fatalf("no ids: %+v", p.Reading)
	}
	patchReq(t, h, p.ID, `{"event":{"title":"Pick-up","date":"2026-11-04"}}`)
	got, _ := s.store.Get(p.ID)
	if id := got.Reading.Events[1].ID; id == "" || id == got.Reading.Events[0].ID {
		t.Fatalf("added event id %q", id)
	}
}
