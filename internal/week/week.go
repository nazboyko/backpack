// Package week turns the read papers into the one page a parent looks at:
// every date in order, every to-do, and how many papers need nothing.
package week

import (
	"sort"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

// Date is one event with the paper it came from. Index is the event's
// place in that paper, which is how the page edits it.
type Date struct {
	PaperID string `json:"paper_id"`
	Index   int    `json:"index"`
	Paper   string `json:"paper"`
	reader.Event
}

// Todo is one action with the paper it came from.
type Todo struct {
	PaperID string `json:"paper_id"`
	Index   int    `json:"index"`
	Paper   string `json:"paper"`
	reader.Action
}

// Week is the summary above the cards.
type Week struct {
	Label   string `json:"label"`   // "Week of Oct 26"
	Dates   []Date `json:"dates"`   // from today on, then dates that could not be read
	Earlier []Date `json:"earlier"` // already over
	Todos   []Todo `json:"todos"`   // open first, then done
	Open    int    `json:"open"`    // open to-dos
	Nothing int    `json:"nothing"` // read papers that need nothing
}

// Build groups the read papers (newest first, as the store lists them).
func Build(papers []store.Paper, now time.Time) Week {
	today := now.Format("2006-01-02")
	w := Week{Label: Label(now), Dates: []Date{}, Earlier: []Date{}, Todos: []Todo{}}
	var undated []Date
	for _, p := range papers {
		if p.Status != store.Done {
			continue
		}
		if p.Reading.NothingToDo {
			w.Nothing++
		}
		for i, e := range p.Reading.Events {
			d := Date{PaperID: p.ID, Index: i, Paper: p.Reading.Title, Event: e}
			last := e.Date
			if e.EndDate != "" {
				last = e.EndDate
			}
			switch {
			case e.Date == "":
				undated = append(undated, d)
			case last < today:
				w.Earlier = append(w.Earlier, d)
			default:
				w.Dates = append(w.Dates, d)
			}
		}
		for i, a := range p.Reading.Actions {
			w.Todos = append(w.Todos, Todo{PaperID: p.ID, Index: i, Paper: p.Reading.Title, Action: a})
			if !a.Done {
				w.Open++
			}
		}
	}
	byDay := func(ds []Date) func(i, j int) bool {
		return func(i, j int) bool {
			if ds[i].Date != ds[j].Date {
				return ds[i].Date < ds[j].Date
			}
			return ds[i].StartTime < ds[j].StartTime // all-day ("") first
		}
	}
	sort.SliceStable(w.Dates, byDay(w.Dates))
	sort.SliceStable(w.Earlier, byDay(w.Earlier))
	w.Dates = append(w.Dates, undated...)

	sort.SliceStable(w.Todos, func(i, j int) bool {
		a, b := w.Todos[i], w.Todos[j]
		if a.Done != b.Done {
			return !a.Done
		}
		if (a.Due == "") != (b.Due == "") {
			return a.Due != "" // tasks with a date before tasks without
		}
		return a.Due < b.Due
	})
	return w
}

// Label names the week a parent is planning: this week on a weekday, the
// coming week on a weekend.
func Label(now time.Time) string {
	offset := (int(time.Monday) - int(now.Weekday()) - 7) % 7 // back to this Monday
	switch now.Weekday() {
	case time.Saturday:
		offset = 2
	case time.Sunday:
		offset = 1
	}
	return "Week of " + now.AddDate(0, 0, offset).Format("Jan 2")
}
