package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

// A patch changes one thing on a paper: its title or summary, one event or
// one action. An edit without an index adds a new event or action.
type patch struct {
	Title   *string     `json:"title"`
	Summary *string     `json:"summary"`
	Event   *eventEdit  `json:"event"`
	Action  *actionEdit `json:"action"`
}

type eventEdit struct {
	Index     *int    `json:"index"`
	Remove    bool    `json:"remove"`
	Title     *string `json:"title"`
	Date      *string `json:"date"`
	EndDate   *string `json:"end_date"`
	StartTime *string `json:"start_time"`
	EndTime   *string `json:"end_time"`
	Location  *string `json:"location"`
	Include   *bool   `json:"include"`
}

type actionEdit struct {
	Index  *int    `json:"index"`
	Remove bool    `json:"remove"`
	Text   *string `json:"text"`
	Kind   *string `json:"kind"`
	Due    *string `json:"due"`
	Done   *bool   `json:"done"`
}

// badEdit is a 400 with a message the page can show next to the field.
type badEdit string

func (b badEdit) Error() string { return string(b) }

var (
	errConflict = errors.New("not in a state for this")
	timeRe      = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

const (
	errNoIndex  badEdit = "That line is not on this paper any more. Reload the page."
	errBadDate  badEdit = "Use a date like 2026-11-04."
	errBadTime  badEdit = "Use a time like 15:30."
	errNoTitle  badEdit = "Give it a name."
	errNoChange badEdit = "Nothing to change."
)

func (s *Server) patch(w http.ResponseWriter, r *http.Request) {
	var pt patch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pt); err != nil {
		writeError(w, http.StatusBadRequest, string(errNoChange))
		return
	}
	p, err := s.store.Update(r.PathValue("id"), func(p *store.Paper) error {
		if p.Status != store.Done {
			return errConflict
		}
		return apply(p, pt)
	})
	if s.storeError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// storeError answers for a failed store call and reports whether it did.
func (s *Server) storeError(w http.ResponseWriter, err error) bool {
	var bad badEdit
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "This paper is gone. Reload the page.")
	case errors.Is(err, errConflict):
		writeError(w, http.StatusConflict, "This paper is still being read. Try again in a moment.")
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, string(bad))
	default:
		s.log.Printf("saving a change: %v", err)
		writeError(w, http.StatusInternalServerError, "Backpack could not save that. Try again.")
	}
	return true
}

func apply(p *store.Paper, pt patch) error {
	rd := &p.Reading
	changed := false
	if pt.Title != nil {
		if rd.Title = clean(*pt.Title, 60); rd.Title == "" {
			return errNoTitle
		}
		changed = true
	}
	if pt.Summary != nil {
		rd.Summary, changed = clean(*pt.Summary, 200), true
	}
	if pt.Event != nil {
		if err := applyEvent(rd, *pt.Event); err != nil {
			return err
		}
		changed = changed || pt.Event.Remove || pt.Event.Index == nil || touchesEvent(*pt.Event)
	}
	if pt.Action != nil {
		if err := applyAction(rd, *pt.Action); err != nil {
			return err
		}
		changed = changed || pt.Action.Remove || pt.Action.Index == nil || touchesAction(*pt.Action)
	}
	if pt.Title == nil && pt.Summary == nil && pt.Event == nil && pt.Action == nil {
		return errNoChange
	}
	if changed {
		p.Edited = true
	}
	rd.NothingToDo = len(rd.Events) == 0 && len(rd.Actions) == 0
	return nil
}

// touchesEvent is false for a plain include switch, which is not an edit
// of what the paper says.
func touchesEvent(e eventEdit) bool {
	return e.Title != nil || e.Date != nil || e.EndDate != nil || e.StartTime != nil || e.EndTime != nil || e.Location != nil
}

func touchesAction(a actionEdit) bool { return a.Text != nil || a.Kind != nil || a.Due != nil }

func applyEvent(rd *reader.Reading, ed eventEdit) error {
	var e *reader.Event
	switch {
	case ed.Index == nil:
		if ed.Remove {
			return errNoIndex
		}
		rd.Events = append(rd.Events, reader.Event{Title: rd.Title, Confidence: reader.Checked})
		e = &rd.Events[len(rd.Events)-1]
	case *ed.Index < 0 || *ed.Index >= len(rd.Events):
		return errNoIndex
	case ed.Remove:
		rd.Events = append(rd.Events[:*ed.Index], rd.Events[*ed.Index+1:]...)
		return nil
	default:
		e = &rd.Events[*ed.Index]
	}
	if ed.Title != nil {
		if e.Title = clean(*ed.Title, 60); e.Title == "" {
			return errNoTitle
		}
	}
	if ed.Location != nil {
		e.Location = clean(*ed.Location, 120)
	}
	if ed.Include != nil {
		v := *ed.Include
		e.Include = &v
	}
	dated := false
	for _, f := range []struct {
		in       *string
		out      *string
		optional bool
	}{{ed.Date, &e.Date, false}, {ed.EndDate, &e.EndDate, true}} {
		if f.in == nil {
			continue
		}
		v := strings.TrimSpace(*f.in)
		if v == "" && f.optional {
			*f.out, dated = "", true
			continue
		}
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return errBadDate
		}
		*f.out, dated = v, true
	}
	for _, f := range []struct{ in, out *string }{{ed.StartTime, &e.StartTime}, {ed.EndTime, &e.EndTime}} {
		if f.in == nil {
			continue
		}
		v := strings.TrimSpace(*f.in)
		if v != "" && !timeRe.MatchString(v) {
			return errBadTime
		}
		*f.out, dated = v, true
	}
	if ed.Index == nil && e.Date == "" {
		return errBadDate // a new event needs a day
	}
	if e.EndDate != "" && e.EndDate <= e.Date {
		e.EndDate = ""
	}
	if e.StartTime == "" || (e.EndTime != "" && e.EndTime <= e.StartTime) {
		e.EndTime = ""
	}
	if dated {
		// A person typed it in: no longer a guess.
		e.Confidence, e.Raw = reader.Checked, ""
	}
	return nil
}

func applyAction(rd *reader.Reading, ed actionEdit) error {
	var a *reader.Action
	switch {
	case ed.Index == nil:
		if ed.Remove {
			return errNoIndex
		}
		rd.Actions = append(rd.Actions, reader.Action{Kind: "other"})
		a = &rd.Actions[len(rd.Actions)-1]
	case *ed.Index < 0 || *ed.Index >= len(rd.Actions):
		return errNoIndex
	case ed.Remove:
		rd.Actions = append(rd.Actions[:*ed.Index], rd.Actions[*ed.Index+1:]...)
		return nil
	default:
		a = &rd.Actions[*ed.Index]
	}
	if ed.Text != nil {
		a.Text = clean(*ed.Text, 200)
	}
	if a.Text == "" {
		return errNoTitle
	}
	if ed.Kind != nil {
		switch k := strings.TrimSpace(*ed.Kind); k {
		case "sign", "return", "bring", "pay", "rsvp", "other":
			a.Kind = k
		default:
			a.Kind = "other"
		}
	}
	if ed.Due != nil {
		v := strings.TrimSpace(*ed.Due)
		if v != "" {
			if _, err := time.Parse("2006-01-02", v); err != nil {
				return errBadDate
			}
		}
		a.Due, a.Raw, a.Confidence = v, "", ""
		if v != "" {
			a.Confidence = reader.Checked
		}
	}
	if ed.Done != nil {
		a.Done = *ed.Done
	}
	return nil
}

// clean trims and folds white space and cuts to n characters.
func clean(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n]))
	}
	return s
}
