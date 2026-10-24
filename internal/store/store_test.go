package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
)

var t0 = time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	a, err := s.Add([]byte("photo a"), t0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Add([]byte("photo b"), t0.Add(time.Minute))
	if len(a.ID) != 12 || a.Status != Queued || a.Photo != a.ID+".jpg" {
		t.Fatalf("new paper %+v", a)
	}
	if got, _ := os.ReadFile(s.PhotoPath(a)); string(got) != "photo a" {
		t.Fatalf("photo on disk %q", got)
	}

	_, err = s.Update(a.ID, func(p *Paper) error {
		p.Status = Done
		p.Reading = reader.Reading{Title: "Picture day", Events: []reader.Event{{Title: "Picture day", Date: "2026-11-03"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A fresh store on the same directory sees everything, newest first.
	list := open(t, dir).List()
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("list order %+v", list)
	}
	if list[1].Reading.Title != "Picture day" || list[1].Status != Done {
		t.Fatalf("update not saved: %+v", list[1])
	}

	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.PhotoPath(a)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("photo still on disk: %v", err)
	}
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted paper still there: %v", err)
	}
	if err := s.Delete(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if len(open(t, dir).List()) != 1 {
		t.Fatal("delete not saved")
	}
}

func TestUpdateErrorChangesNothing(t *testing.T) {
	s := open(t, t.TempDir())
	p, _ := s.Add([]byte("x"), t0)
	_, err := s.Update(p.ID, func(p *Paper) error {
		p.Status = Done
		return errors.New("bad edit")
	})
	if err == nil {
		t.Fatal("error swallowed")
	}
	if got, _ := s.Get(p.ID); got.Status != Queued {
		t.Fatalf("status %q after a failed update", got.Status)
	}
}

func TestUpdateCannotMoveThePhoto(t *testing.T) {
	s := open(t, t.TempDir())
	p, _ := s.Add([]byte("x"), t0)
	got, _ := s.Update(p.ID, func(q *Paper) error {
		q.Photo, q.ID = "../../etc/passwd", "other"
		return nil
	})
	if got.Photo != p.Photo || got.ID != p.ID {
		t.Fatalf("update changed identity: %+v", got)
	}
}

func TestCopiesDoNotShareSlices(t *testing.T) {
	s := open(t, t.TempDir())
	p, _ := s.Add([]byte("x"), t0)
	s.Update(p.ID, func(q *Paper) error {
		q.Reading.Actions = []reader.Action{{Text: "Sign the slip"}}
		return nil
	})
	got, _ := s.Get(p.ID)
	got.Reading.Actions[0].Done = true
	again, _ := s.Get(p.ID)
	if again.Reading.Actions[0].Done {
		t.Fatal("a copy changed the stored paper")
	}
}

func TestCrashLeavesOldFileReadable(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	s.Add([]byte("x"), t0)
	// A crash in the middle of a save leaves a half-written temp file.
	if err := os.WriteFile(filepath.Join(dir, "papers.json.123.tmp"), []byte(`[{"id":"broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir)
	if len(s2.List()) != 1 {
		t.Fatal("store did not load the last good file")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestReadingGoesBackToQueueAfterCrash(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	p, _ := s.Add([]byte("x"), t0)
	got, ok, err := s.NextQueued()
	if err != nil || !ok || got.ID != p.ID || got.Status != Reading {
		t.Fatalf("NextQueued = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.NextQueued(); ok {
		t.Fatal("the same paper was handed out twice")
	}
	again, _ := open(t, dir).Get(p.ID)
	if again.Status != Queued {
		t.Fatalf("after restart status %q, want queued", again.Status)
	}
}

func TestNextQueuedIsOldestFirst(t *testing.T) {
	s := open(t, t.TempDir())
	a, _ := s.Add([]byte("a"), t0)
	s.Add([]byte("b"), t0.Add(time.Second))
	got, _, _ := s.NextQueued()
	if got.ID != a.ID {
		t.Fatal("not first in, first out")
	}
}

func TestConcurrentUpdatesKeepEveryChange(t *testing.T) {
	s := open(t, t.TempDir())
	p, _ := s.Add([]byte("x"), t0)
	s.Update(p.ID, func(q *Paper) error {
		for i := 0; i < 20; i++ {
			q.Reading.Actions = append(q.Reading.Actions, reader.Action{Text: fmt.Sprint("task ", i)})
		}
		return nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := s.Update(p.ID, func(q *Paper) error {
				q.Reading.Actions[i].Done = true
				return nil
			}); err != nil {
				t.Error(err)
			}
			s.List()
		}(i)
	}
	wg.Wait()
	got, _ := s.Get(p.ID)
	for i, a := range got.Reading.Actions {
		if !a.Done {
			t.Errorf("update %d lost", i)
		}
	}
}

func TestPhotoAndFilesArePrivate(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	p, _ := s.Add([]byte("x"), t0)
	for _, f := range []string{s.PhotoPath(p), filepath.Join(dir, "papers.json")} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v, want owner only", filepath.Base(f), info.Mode().Perm())
		}
	}
}
