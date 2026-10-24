package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOllama plays the parts of Ollama that Backpack uses. answers are the
// message contents returned by successive chat calls; the last one repeats.
type fakeOllama struct {
	t       *testing.T
	answers []string
	models  []string
	delay   time.Duration
	calls   atomic.Int32
	mu      sync.Mutex
	lastReq chatRequest
}

func (f *fakeOllama) last() chatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReq
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/version":
		w.Write([]byte(`{"version":"0.35.1"}`))
	case "/api/tags":
		var b strings.Builder
		b.WriteString(`{"models":[`)
		for i, m := range f.models {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"name":"` + m + `"}`)
		}
		b.WriteString(`]}`)
		w.Write([]byte(b.String()))
	case "/api/chat":
		n := int(f.calls.Add(1))
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("chat body: %v", err)
		}
		f.mu.Lock()
		f.lastReq = req
		f.mu.Unlock()
		if f.delay > 0 {
			select {
			case <-time.After(f.delay):
			case <-r.Context().Done():
				return
			}
		}
		if !has(f.models, req.Model) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"model '` + req.Model + `' not found"}`))
			return
		}
		answer := f.answers[min(n, len(f.answers))-1]
		out, _ := json.Marshal(map[string]any{
			"model": req.Model, "done": true,
			"message": map[string]string{"role": "assistant", "content": answer},
		})
		w.Write(out)
	default:
		http.NotFound(w, r)
	}
}

func startFake(t *testing.T, f *fakeOllama) *Client {
	t.Helper()
	f.t = t
	if f.models == nil {
		f.models = []string{"gemma4:e4b"}
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL}
}

const goodAnswer = `{"title":"Early release day","summary":"School ends at 12:30 on Thursday.","kind":"notice",
"nothing_to_do":false,"events":[{"title":"Early release","date":"2026-10-29","start_time":"12:30 PM","date_confidence":"read"}],
"actions":[{"text":"Arrange pick-up at 12:30","kind":"other","due":"2026-10-29","date_confidence":"read"}]}`

func TestReadHappyPath(t *testing.T) {
	f := &fakeOllama{answers: []string{goodAnswer}}
	c := startFake(t, f)
	photo := []byte("\xff\xd8\xff not really a jpeg")
	r, err := c.Read(context.Background(), photo, monday)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Early release day" || len(r.Events) != 1 || len(r.Actions) != 1 {
		t.Fatalf("reading: %+v", r)
	}
	if r.Events[0].StartTime != "12:30" {
		t.Errorf("time not validated: %q", r.Events[0].StartTime)
	}

	q := f.last()
	if q.Model != "gemma4:e4b" || q.Stream || q.Think {
		t.Errorf("request model %q stream %v think %v", q.Model, q.Stream, q.Think)
	}
	if q.Options["temperature"] != 0.1 {
		t.Errorf("temperature %v", q.Options["temperature"])
	}
	var want, got any
	json.Unmarshal(Schema, &want)
	json.Unmarshal(q.Format, &got)
	if !jsonEqual(got, want) {
		t.Error("format is not the schema")
	}
	if len(q.Messages) != 1 || len(q.Messages[0].Images) != 1 || !bytes.Equal(q.Messages[0].Images[0], photo) {
		t.Fatal("photo not sent as the one image of one message")
	}
	if !strings.Contains(q.Messages[0].Content, "Today is Monday, 2026-10-26.") {
		t.Error("prompt lacks today")
	}
}

func TestReadSendsThinkFalseExplicitly(t *testing.T) {
	body, _ := json.Marshal(chatRequest{})
	if !strings.Contains(string(body), `"think":false`) || !strings.Contains(string(body), `"stream":false`) {
		t.Fatalf("think/stream must be sent as false, got %s", body)
	}
}

func TestReadRetriesMalformedJSONOnce(t *testing.T) {
	f := &fakeOllama{answers: []string{"Sure! Here is the JSON: {", goodAnswer}}
	c := startFake(t, f)
	if _, err := c.Read(context.Background(), []byte("x"), monday); err != nil {
		t.Fatalf("second answer was good, got %v", err)
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("%d calls, want 2", n)
	}
}

func TestReadFailsAfterTwoMalformedAnswers(t *testing.T) {
	f := &fakeOllama{answers: []string{"not json"}}
	c := startFake(t, f)
	_, err := c.Read(context.Background(), []byte("x"), monday)
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("got %v, want ErrUnreadable", err)
	}
	if Explain(err) != string(ErrUnreadable) {
		t.Errorf("Explain = %q", Explain(err))
	}
	if n := f.calls.Load(); n != 2 {
		t.Fatalf("%d calls, want exactly 2", n)
	}
}

func TestReadTimeout(t *testing.T) {
	f := &fakeOllama{answers: []string{goodAnswer}, delay: 2 * time.Second}
	c := startFake(t, f)
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err := c.Read(context.Background(), []byte("x"), monday)
	if !errors.Is(err, ErrTooSlow) {
		t.Fatalf("got %v, want ErrTooSlow", err)
	}
	if time.Since(start) > time.Second {
		t.Error("timeout did not stop the call")
	}
}

func TestReadOllamaDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now
	c := &Client{BaseURL: url}

	_, err := c.Read(context.Background(), []byte("x"), monday)
	if !errors.Is(err, ErrOllamaDown) {
		t.Fatalf("got %v, want ErrOllamaDown", err)
	}
	h := c.Health(context.Background())
	if h.Reachable || h.Problem != string(ErrOllamaDown) || !strings.Contains(h.Problem, "ollama serve") {
		t.Fatalf("health %+v", h)
	}
}

func TestReadModelMissing(t *testing.T) {
	f := &fakeOllama{answers: []string{goodAnswer}, models: []string{"llama3:8b"}}
	c := startFake(t, f)
	_, err := c.Read(context.Background(), []byte("x"), monday)
	var m MissingModel
	if !errors.As(err, &m) || m.Model != "gemma4:e4b" {
		t.Fatalf("got %v, want MissingModel", err)
	}
	if !strings.Contains(Explain(err), "ollama pull gemma4:e4b") {
		t.Errorf("Explain = %q", Explain(err))
	}
	h := c.Health(context.Background())
	if !h.Reachable || h.ModelPresent || !strings.Contains(h.Problem, "ollama pull gemma4:e4b") {
		t.Fatalf("health %+v", h)
	}
}

func TestHealthReady(t *testing.T) {
	c := startFake(t, &fakeOllama{models: []string{"gemma4:26b", "gemma4:e4b"}})
	h := c.Health(context.Background())
	if !h.Reachable || !h.ModelPresent || h.Problem != "" || h.Ollama != "0.35.1" {
		t.Fatalf("health %+v", h)
	}
}

func TestHealthModelWithoutTag(t *testing.T) {
	c := startFake(t, &fakeOllama{models: []string{"gemma4:latest"}})
	c.Model = "gemma4"
	if h := c.Health(context.Background()); !h.ModelPresent {
		t.Fatalf("gemma4 should match gemma4:latest: %+v", h)
	}
}

func TestReadOtherOllamaError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model requires more system memory"}`))
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL}).Read(context.Background(), []byte("x"), monday)
	if !errors.Is(err, ErrModelStopped) || !strings.Contains(err.Error(), "system memory") {
		t.Fatalf("got %v", err)
	}
	if Explain(err) != string(ErrModelStopped) {
		t.Errorf("Explain leaks internals: %q", Explain(err))
	}
}

func TestExplainUnknownError(t *testing.T) {
	if got := Explain(errors.New("boom: stack trace")); got != string(ErrUnreadable) {
		t.Fatalf("Explain = %q", got)
	}
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
