//go:build live

package reader

// The live check: the ten fixture papers through the real local model.
// Run with `make live-check` (BACKPACK_MODEL picks another model). It
// needs Ollama running; nothing here talks to anything else.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// expected is the hand-written reading of one fixture, plus the words that
// identify each action.
type expected struct {
	Reading
	Actions []struct {
		Action
		Match []string `json:"match"`
	} `json:"actions"`
}

type score struct {
	name                                  string
	ms                                    int64
	events, eventsWant, actions, actsWant int
	extraEvents, extraActions, guessed    int
	nothingRight                          bool
	missed                                []string
}

func TestLiveFixtures(t *testing.T) {
	client := &Client{BaseURL: os.Getenv("BACKPACK_OLLAMA"), Model: os.Getenv("BACKPACK_MODEL")}
	if h := client.Health(context.Background()); h.Problem != "" {
		t.Fatal(h.Problem)
	}
	// The day the fixtures were written for: Monday of the week they describe.
	today := time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)

	files, _ := filepath.Glob("../../testdata/papers/*.jpg")
	var scores []score
	readings := map[string]Reading{}
	start := time.Now()
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".jpg")
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var want expected
		raw, _ := os.ReadFile(strings.TrimSuffix(f, ".jpg") + ".expected.json")
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		jpg, err := Prepare(data)
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		got, err := client.Read(context.Background(), jpg, today)
		ms := time.Since(t0).Milliseconds()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			scores = append(scores, score{name: name, ms: ms, eventsWant: len(want.Events), actsWant: len(want.Actions)})
			continue
		}
		s := compare(name, want, got)
		s.ms = ms
		scores = append(scores, s)
		readings[name] = got
	}
	total := time.Since(start)
	report(t, client.model(), scores, total)
	// BACKPACK_LIVE_OUT names a file for the full readings, to look at by eye.
	if out := os.Getenv("BACKPACK_LIVE_OUT"); out != "" {
		data, _ := json.MarshalIndent(readings, "", "  ")
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Error(err)
		}
	}
}

// compare counts what the model found against the hand-written reading.
// An event is found when one with the same date (and end date, if there
// is one) came back; an action when its text holds one of the match words.
func compare(name string, want expected, got Reading) score {
	s := score{name: name, eventsWant: len(want.Events), actsWant: len(want.Actions)}
	usedE := map[int]bool{}
	for _, we := range want.Events {
		hit := false
		for i, ge := range got.Events {
			if !usedE[i] && ge.Date == we.Date && (we.EndDate == "" || ge.EndDate == we.EndDate) {
				usedE[i], hit = true, true
				break
			}
		}
		if hit {
			s.events++
		} else {
			s.missed = append(s.missed, "event "+we.Title+" "+we.Date)
		}
	}
	s.extraEvents = len(got.Events) - len(usedE)

	usedA := map[int]bool{}
	for _, wa := range want.Actions {
		hit := false
		for i, ga := range got.Actions {
			if usedA[i] {
				continue
			}
			text := strings.ToLower(ga.Text)
			for _, m := range wa.Match {
				if strings.Contains(text, strings.ToLower(m)) {
					usedA[i], hit = true, true
					break
				}
			}
			if hit {
				break
			}
		}
		if hit {
			s.actions++
		} else {
			s.missed = append(s.missed, "action "+wa.Text)
		}
	}
	s.extraActions = len(got.Actions) - len(usedA)

	for _, e := range got.Events {
		if e.Confidence == Guessed {
			s.guessed++
		}
	}
	for _, a := range got.Actions {
		if a.Confidence == Guessed {
			s.guessed++
		}
	}
	s.nothingRight = got.NothingToDo == want.NothingToDo
	return s
}

func report(t *testing.T, model string, scores []score, total time.Duration) {
	var b strings.Builder
	fmt.Fprintf(&b, "model %s, %s\n", model, time.Now().Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "| paper | seconds | events | actions | extra | guessed | nothing-to-do |\n|---|---|---|---|---|---|---|\n")
	var ev, evw, ac, acw, extra, guessed, nothing int
	var ms int64
	for _, s := range scores {
		fmt.Fprintf(&b, "| %s | %.1f | %d/%d | %d/%d | %d | %d | %v |\n", s.name, float64(s.ms)/1000,
			s.events, s.eventsWant, s.actions, s.actsWant, s.extraEvents+s.extraActions, s.guessed, s.nothingRight)
		ev, evw, ac, acw = ev+s.events, evw+s.eventsWant, ac+s.actions, acw+s.actsWant
		extra, guessed, ms = extra+s.extraEvents+s.extraActions, guessed+s.guessed, ms+s.ms
		if s.nothingRight {
			nothing++
		}
	}
	fmt.Fprintf(&b, "| total | %.1f | %d/%d | %d/%d | %d | %d | %d/%d |\n", float64(ms)/1000, ev, evw, ac, acw, extra, guessed, nothing, len(scores))
	fmt.Fprintf(&b, "wall time for all %d papers: %.1f s\n", len(scores), total.Seconds())
	missedDates := 0
	for _, s := range scores {
		if s.events < s.eventsWant {
			missedDates++
		}
		if len(s.missed) > 0 {
			fmt.Fprintf(&b, "%s missed: %s\n", s.name, strings.Join(s.missed, "; "))
		}
	}
	fmt.Fprintf(&b, "papers with a missed date: %d of %d\n", missedDates, len(scores))
	t.Log("\n" + b.String())
}
