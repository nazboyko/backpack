package server

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

// Wake tells the worker there may be something new to read.
func (s *Server) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run reads queued papers one at a time, oldest first, until ctx ends.
func (s *Server) Run(ctx context.Context) {
	for ctx.Err() == nil {
		p, ok, err := s.store.NextQueued()
		switch {
		case err != nil:
			s.log.Printf("taking the next paper: %v", err)
			s.pause(ctx, retryAfter)
		case !ok:
			select {
			case <-s.wake:
			case <-ctx.Done():
			}
		default:
			s.read(ctx, p)
		}
	}
}

func (s *Server) pause(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.wake:
	case <-ctx.Done():
	}
}

func (s *Server) read(ctx context.Context, p store.Paper) {
	jpg, err := os.ReadFile(s.store.PhotoPath(p))
	if err != nil {
		s.finish(p.ID, func(q *store.Paper) {
			q.Status, q.Error = store.Failed, "The photo for this paper is missing. Delete it and drop the photo again."
		})
		return
	}

	start := time.Now()
	r, err := s.reader.Read(ctx, jpg, s.now())
	ms := time.Since(start).Milliseconds()

	var missing reader.MissingModel
	switch {
	case ctx.Err() != nil:
		// Backpack is closing: read it again next time.
		s.finish(p.ID, func(q *store.Paper) { q.Status = store.Queued })
	case errors.Is(err, reader.ErrOllamaDown) || errors.As(err, &missing):
		// Nothing wrong with the paper. Keep it in line until the model is back;
		// the page shows how to start it.
		s.finish(p.ID, func(q *store.Paper) { q.Status = store.Queued })
		s.pause(ctx, retryAfter)
	case err != nil:
		s.log.Printf("reading paper %s: %v", p.ID, err)
		s.finish(p.ID, func(q *store.Paper) {
			q.Status, q.Error, q.ReadMs = store.Failed, reader.Explain(err), ms
		})
	default:
		s.finish(p.ID, func(q *store.Paper) {
			q.Status, q.Error, q.ReadMs, q.Reading = store.Done, "", ms, r
		})
	}
}

// finish saves the outcome unless the paper was deleted while it was read.
func (s *Server) finish(id string, fn func(*store.Paper)) {
	_, err := s.store.Update(id, func(q *store.Paper) error {
		fn(q)
		return nil
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Printf("saving paper %s: %v", id, err)
	}
}
