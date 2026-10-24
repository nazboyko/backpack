// Package store keeps the papers in one JSON file and their photos next to
// it, in the data directory on this computer.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
)

// Paper statuses.
const (
	Queued  = "queued"
	Reading = "reading"
	Done    = "done"
	Failed  = "failed"
)

const fileName = "papers.json"

var ErrNotFound = errors.New("no such paper")

// Paper is one photo of one school paper and what was read from it.
type Paper struct {
	ID      string         `json:"id"`
	AddedAt time.Time      `json:"added_at"`
	Photo   string         `json:"photo"`
	Status  string         `json:"status"`
	Error   string         `json:"error,omitempty"`
	ReadMs  int64          `json:"read_ms,omitempty"`
	Reading reader.Reading `json:"reading"`
	Edited  bool           `json:"edited,omitempty"`
}

// Store is safe for concurrent use. Every change is written to disk before
// the call returns.
type Store struct {
	mu     sync.Mutex
	dir    string
	papers []*Paper // oldest first
}

// Open loads the store from dir, creating the directory if needed. A paper
// left in "reading" by a crash goes back to the queue.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	// Half-written files of ours from a crash are never read; clear them
	// away. Anything else in the folder is left alone.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if isLeftover(e.Name()) {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(data, &s.papers); err != nil {
		return nil, err
	}
	for _, p := range s.papers {
		if p.Status == Reading {
			p.Status = Queued
		}
	}
	return s, nil
}

// Dir is the data directory.
func (s *Store) Dir() string { return s.dir }

// List returns copies of all papers, newest first.
func (s *Store) List() []Paper {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Paper, 0, len(s.papers))
	for i := len(s.papers) - 1; i >= 0; i-- {
		out = append(out, clone(s.papers[i]))
	}
	return out
}

// Get returns a copy of one paper.
func (s *Store) Get(id string) (Paper, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(id)
	if p == nil {
		return Paper{}, ErrNotFound
	}
	return clone(p), nil
}

// Add stores a prepared JPEG as a new queued paper.
func (s *Store) Add(jpg []byte, now time.Time) (Paper, error) {
	id, err := newID()
	if err != nil {
		return Paper{}, err
	}
	p := &Paper{ID: id, AddedAt: now, Photo: id + ".jpg", Status: Queued}
	if err := writeFile(filepath.Join(s.dir, p.Photo), jpg); err != nil {
		return Paper{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.papers = append(s.papers, p)
	if err := s.save(); err != nil {
		s.papers = s.papers[:len(s.papers)-1]
		os.Remove(filepath.Join(s.dir, p.Photo))
		return Paper{}, err
	}
	return clone(p), nil
}

// Update changes one paper under the lock and saves. If fn returns an
// error nothing is changed.
func (s *Store) Update(id string, fn func(*Paper) error) (Paper, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.find(id)
	if p == nil {
		return Paper{}, ErrNotFound
	}
	work := clone(p)
	if err := fn(&work); err != nil {
		return Paper{}, err
	}
	work.ID, work.Photo, work.AddedAt = p.ID, p.Photo, p.AddedAt
	old := *p
	*p = work
	if err := s.save(); err != nil {
		*p = old
		return Paper{}, err
	}
	return clone(p), nil
}

// Delete removes a paper and its photo. The photo goes first: if it cannot
// be removed, the paper stays and the parent can try again.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.papers {
		if p.ID != id {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, p.Photo)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		old := s.papers
		s.papers = append(append([]*Paper{}, s.papers[:i]...), s.papers[i+1:]...)
		if err := s.save(); err != nil {
			s.papers = old
			return err
		}
		return nil
	}
	return ErrNotFound
}

// NextQueued marks the oldest queued paper as reading and returns it.
func (s *Store) NextQueued() (Paper, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.papers {
		if p.Status != Queued {
			continue
		}
		p.Status, p.Error = Reading, ""
		if err := s.save(); err != nil {
			p.Status = Queued
			return Paper{}, false, err
		}
		return clone(p), true, nil
	}
	return Paper{}, false, nil
}

// PhotoPath is where a paper's photo lives on disk.
func (s *Store) PhotoPath(p Paper) string { return filepath.Join(s.dir, p.Photo) }

func (s *Store) find(id string) *Paper {
	for _, p := range s.papers {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// save writes the whole list to a temporary file and renames it over the
// old one, so a crash leaves either the old file or the new one.
func (s *Store) save() error {
	data, err := json.MarshalIndent(s.papers, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.dir, fileName), data)
}

func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// isLeftover matches the temporary names writeFile uses for papers.json
// and the photos.
func isLeftover(name string) bool {
	return strings.HasSuffix(name, ".tmp") &&
		(strings.HasPrefix(name, fileName+".") || strings.Contains(name, ".jpg."))
}

// NewItemID names one event or action so the calendar file can update it
// in place on the next import.
func NewItemID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// clone copies a paper deeply so callers never share slices with the store.
func clone(p *Paper) Paper {
	c := *p
	c.Reading.Events = append([]reader.Event{}, p.Reading.Events...)
	for i, e := range c.Reading.Events {
		if e.Include != nil {
			v := *e.Include
			c.Reading.Events[i].Include = &v
		}
	}
	c.Reading.Actions = append([]reader.Action{}, p.Reading.Actions...)
	return c
}
