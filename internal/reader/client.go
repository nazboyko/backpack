package reader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultOllama  = "http://127.0.0.1:11434"
	DefaultModel   = "gemma4:e4b"
	DefaultTimeout = 180 * time.Second
)

// Messages for the parent when the model cannot do its job.
const (
	ErrOllamaDown   Problem = "Backpack can't reach the model. Start Ollama with: ollama serve"
	ErrTooSlow      Problem = "Reading this photo took too long. Try again, or take a closer photo."
	ErrUnreadable   Problem = "Couldn't read this one. Try a brighter photo, or read it yourself."
	ErrModelStopped Problem = "The model stopped with an error. Try reading it again."
)

// MissingModel means Ollama runs but does not have the model yet.
type MissingModel struct{ Model string }

func (m MissingModel) Error() string {
	return "The model is not on this computer yet. Get it with: ollama pull " + m.Model
}

// failure keeps the plain message for the parent and the cause for the log.
type failure struct {
	msg   Problem
	cause error
}

func (f *failure) Error() string   { return string(f.msg) + " (" + f.cause.Error() + ")" }
func (f *failure) Unwrap() []error { return []error{f.msg, f.cause} }

// Explain turns any error from this package into one sentence a parent can
// act on.
func Explain(err error) string {
	var m MissingModel
	if errors.As(err, &m) {
		return m.Error()
	}
	var p Problem
	if errors.As(err, &p) {
		return string(p)
	}
	return string(ErrUnreadable)
}

// Client asks the local Ollama to read photos.
type Client struct {
	BaseURL string        // DefaultOllama when empty
	Model   string        // DefaultModel when empty
	Timeout time.Duration // per photo, DefaultTimeout when zero
	HTTP    *http.Client  // http.DefaultClient when nil
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultOllama
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) model() string {
	if c.Model == "" {
		return DefaultModel
	}
	return c.Model
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

type chatRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Think    bool            `json:"think"`
	Format   json.RawMessage `json:"format"`
	Options  map[string]any  `json:"options"`
	Messages []chatMessage   `json:"messages"`
}

type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  [][]byte `json:"images,omitempty"` // base64 in JSON
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Error   string      `json:"error"`
}

// Read sends one prepared photo to the model and returns what it found,
// validated. A reply that is not valid JSON is asked for once more.
func (c *Client) Read(ctx context.Context, jpg []byte, now time.Time) (Reading, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bad error
	for attempt := 0; attempt < 2; attempt++ {
		content, err := c.chat(ctx, jpg, now)
		if err != nil {
			return Reading{}, err
		}
		var r Reading
		if err := json.Unmarshal([]byte(content), &r); err != nil {
			bad = err
			continue
		}
		return Validate(r, now), nil
	}
	return Reading{}, &failure{ErrUnreadable, fmt.Errorf("model answer is not JSON: %w", bad)}
}

func (c *Client) chat(ctx context.Context, jpg []byte, now time.Time) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:    c.model(),
		Format:   Schema,
		Options:  map[string]any{"temperature": 0.1},
		Messages: []chatMessage{{Role: "user", Content: Prompt(now), Images: [][]byte{jpg}}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return "", &failure{ErrTooSlow, err}
		case errors.Is(err, context.Canceled):
			return "", err
		}
		return "", &failure{ErrOllamaDown, err}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", &failure{ErrTooSlow, err}
		}
		return "", &failure{ErrModelStopped, err}
	}
	var out chatResponse
	jsonErr := json.Unmarshal(data, &out)
	switch {
	case resp.StatusCode == http.StatusNotFound && strings.Contains(out.Error, "not found"):
		return "", MissingModel{c.model()}
	case resp.StatusCode != http.StatusOK:
		return "", &failure{ErrModelStopped, fmt.Errorf("ollama answered %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))}
	case jsonErr != nil:
		return "", &failure{ErrModelStopped, fmt.Errorf("ollama reply is not JSON: %w", jsonErr)}
	}
	return out.Message.Content, nil
}

// Health is what the page needs to know about the model before reading.
type Health struct {
	Reachable    bool   `json:"reachable"`
	ModelPresent bool   `json:"model_present"`
	Model        string `json:"model"`
	Ollama       string `json:"ollama_version,omitempty"`
	Problem      string `json:"problem,omitempty"` // what to do, empty when all is well
}

// Health checks that Ollama answers and has the model.
func (c *Client) Health(ctx context.Context) Health {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	h := Health{Model: c.model()}

	var v struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/api/version", &v); err != nil {
		h.Problem = string(ErrOllamaDown)
		return h
	}
	h.Reachable, h.Ollama = true, v.Version

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, "/api/tags", &tags); err != nil {
		h.Problem = string(ErrOllamaDown)
		return h
	}
	want := h.Model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range tags.Models {
		if m.Name == want {
			h.ModelPresent = true
		}
	}
	if !h.ModelPresent {
		h.Problem = MissingModel{h.Model}.Error()
	}
	return h
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}
