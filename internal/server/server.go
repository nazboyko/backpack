// Package server serves the page and the small JSON API behind it, on
// 127.0.0.1 only, and runs the worker that reads queued papers one by one.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/nazboyko/backpack/internal/ics"
	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
	"github.com/nazboyko/backpack/internal/week"
)

//go:embed ui
var uiFiles embed.FS

const (
	maxPhoto   = 40 << 20 // one phone photo, HEIC or JPEG, with room to spare
	maxPhotos  = 50
	retryAfter = 5 * time.Second // wait before trying again when Ollama is away
)

// maxUpload caps one drop of a week's pile; a variable so tests can trip it.
var maxUpload int64 = 400 << 20

// Reader reads a prepared photo; *reader.Client is the real one.
type Reader interface {
	Read(ctx context.Context, jpg []byte, now time.Time) (reader.Reading, error)
	Health(ctx context.Context) reader.Health
}

// Server holds what the handlers and the worker share.
type Server struct {
	store  *store.Store
	reader Reader
	port   int
	now    func() time.Time
	log    *log.Logger
	wake   chan struct{}
}

// New makes a server for the given store, reader and port.
func New(st *store.Store, r Reader, port int, logger *log.Logger) *Server {
	return &Server{store: st, reader: r, port: port, now: time.Now, log: logger, wake: make(chan struct{}, 1)}
}

// ListenAddr is the only address Backpack listens on.
func ListenAddr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

// Handler routes every request, behind the host and origin checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/papers", s.listPapers)
	mux.HandleFunc("POST /api/papers", s.upload)
	mux.HandleFunc("PATCH /api/papers/{id}", s.patch)
	mux.HandleFunc("DELETE /api/papers/{id}", s.remove)
	mux.HandleFunc("POST /api/papers/{id}/reread", s.reread)
	mux.HandleFunc("GET /api/papers/{id}/photo", s.photo)
	mux.HandleFunc("GET /api/calendar.ics", s.calendar)
	mux.HandleFunc("GET /api/health", s.health)
	return s.guard(mux)
}

// guard turns away requests that did not come from this page on this
// computer: a wrong Host header (DNS rebinding) or, for anything that
// changes data, a request sent by another site open in the browser.
func (s *Server) guard(next http.Handler) http.Handler {
	hosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.port): true,
		fmt.Sprintf("localhost:%d", s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		if !hosts[r.Host] {
			http.Error(w, "Backpack only answers on this computer.", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			site := r.Header.Get("Sec-Fetch-Site")
			if (origin != "" && !hosts[strings.TrimPrefix(origin, "http://")]) ||
				(site != "" && site != "same-origin" && site != "none") {
				http.Error(w, "Backpack only takes changes from its own page.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type listResponse struct {
	Papers []store.Paper `json:"papers"`
	Week   week.Week     `json:"week"`
}

func (s *Server) listPapers(w http.ResponseWriter, r *http.Request) {
	papers := s.store.List()
	writeJSON(w, http.StatusOK, listResponse{Papers: papers, Week: week.Build(papers, s.now())})
}

type uploadProblem struct {
	File  string `json:"file"`
	Error string `json:"error"`
}

type uploadResponse struct {
	Papers   []store.Paper   `json:"papers"`
	Problems []uploadProblem `json:"problems"`
}

// upload takes one or more photos from the "photos" field. Good photos are
// queued even when others in the same drop are not photos; those come back
// as problems. If nothing could be added the answer is 400.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Drop one or more photos.")
		return
	}
	out := uploadResponse{Papers: []store.Paper{}, Problems: []uploadProblem{}}
	for n := 0; ; {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil {
			if part.FormName() != "photos" || part.FileName() == "" {
				part.Close()
				continue
			}
			if n++; n > maxPhotos {
				out.Problems = append(out.Problems, uploadProblem{part.FileName(), fmt.Sprintf("Only %d photos at a time.", maxPhotos)})
				part.Close()
				continue
			}
			var p store.Paper
			var problem string
			if p, problem, err = s.addPhoto(part); err == nil {
				if problem != "" {
					out.Problems = append(out.Problems, uploadProblem{part.FileName(), problem})
				} else {
					out.Papers = append(out.Papers, p)
				}
				continue
			}
		}
		// The drop broke off (too big, or the browser went away). Whatever
		// arrived whole is kept and read.
		if len(out.Papers) == 0 {
			writeError(w, http.StatusRequestEntityTooLarge, "That was too much at once. Drop fewer photos at a time.")
			return
		}
		out.Problems = append(out.Problems, uploadProblem{"", fmt.Sprintf("Only the first %d photos arrived. Drop the rest again.", len(out.Papers))})
		break
	}
	if len(out.Papers) == 0 {
		msg := "Drop one or more photos."
		if len(out.Problems) > 0 {
			msg = out.Problems[0].Error
		}
		writeJSON(w, http.StatusBadRequest, struct {
			Error    string          `json:"error"`
			Problems []uploadProblem `json:"problems"`
		}{msg, out.Problems})
		return
	}
	s.Wake()
	writeJSON(w, http.StatusCreated, out)
}

// addPhoto stores one uploaded photo. It returns a problem to show for
// this file, or an error when the upload itself broke off.
func (s *Server) addPhoto(part *multipart.Part) (store.Paper, string, error) {
	defer part.Close()
	data, err := io.ReadAll(io.LimitReader(part, maxPhoto+1))
	if err != nil {
		return store.Paper{}, "", err
	}
	if len(data) > maxPhoto {
		return store.Paper{}, string(reader.ErrTooBig), nil
	}
	jpg, err := reader.Prepare(data)
	if err != nil {
		return store.Paper{}, reader.Explain(err), nil
	}
	p, err := s.store.Add(jpg, s.now())
	if err != nil {
		s.log.Printf("saving a photo: %v", err)
		return store.Paper{}, "Backpack could not save this photo. Check that the disk is not full.", nil
	}
	return p, "", nil
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	switch err := s.store.Delete(r.PathValue("id")); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "This paper is already gone.")
	case err != nil:
		s.log.Printf("deleting a paper: %v", err)
		writeError(w, http.StatusInternalServerError, "Backpack could not delete this paper. Try again.")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) reread(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Update(r.PathValue("id"), func(p *store.Paper) error {
		if p.Status != store.Failed {
			return errConflict
		}
		p.Status, p.Error = store.Queued, ""
		return nil
	})
	if errors.Is(err, errConflict) {
		writeError(w, http.StatusConflict, "This paper is read already.")
		return
	}
	if s.storeError(w, err) {
		return
	}
	s.Wake()
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) photo(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, s.store.PhotoPath(p))
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if q := r.URL.Query().Get("ids"); q != "" {
		ids = strings.Split(q, ",")
	}
	items := ics.Items(s.store.List(), ids)
	if len(items) == 0 {
		writeError(w, http.StatusNotFound, "There are no dates for the calendar yet.")
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="backpack.ics"`)
	if err := ics.Write(w, items, s.now()); err != nil {
		s.log.Printf("writing the calendar: %v", err)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.reader.Health(r.Context()))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
