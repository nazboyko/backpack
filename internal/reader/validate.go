package reader

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	maxTitle    = 60
	maxSummary  = 200
	maxText     = 200
	maxLocation = 120
	maxItems    = 40
	isoDate     = "2006-01-02"
)

var (
	paperKinds  = map[string]bool{"notice": true, "form": true, "flyer": true, "homework": true, "calendar": true, "other": true}
	actionKinds = map[string]bool{"sign": true, "return": true, "bring": true, "pay": true, "rsvp": true, "other": true}
)

// Validate never trusts the model. It checks every date and time, trims
// and caps every string, merges repeated events, and marks anything
// doubtful as guessed so the page asks the parent to check it.
func Validate(r Reading, now time.Time) Reading {
	r.Title = clip(r.Title, maxTitle)
	if r.Title == "" {
		r.Title = "School paper"
	}
	r.Summary = clip(r.Summary, maxSummary)
	r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
	if !paperKinds[r.Kind] {
		r.Kind = "other"
	}

	var events []Event
	for _, e := range r.Events {
		if e, ok := checkEvent(e, r.Title, now); ok && len(events) < maxItems {
			events = addEvent(events, e)
		}
	}
	var actions []Action
	for _, a := range r.Actions {
		if a, ok := checkAction(a, now); ok && len(actions) < maxItems && !hasAction(actions, a) {
			actions = append(actions, a)
		}
	}
	r.Events, r.Actions = events, actions
	if r.Events == nil {
		r.Events = []Event{}
	}
	if r.Actions == nil {
		r.Actions = []Action{}
	}
	// Whatever the model said: a paper with dates or tasks needs the
	// parent, and a paper with neither does not.
	r.NothingToDo = len(r.Events) == 0 && len(r.Actions) == 0
	return r
}

func checkEvent(e Event, paperTitle string, now time.Time) (Event, bool) {
	e.Title = clip(e.Title, maxTitle)
	if e.Title == "" {
		e.Title = paperTitle
	}
	e.Location = clip(e.Location, maxLocation)
	e.Raw = ""
	e.Include = nil
	if e.Confidence != Read {
		e.Confidence = Guessed
	}

	raw := clip(e.Date, maxTitle)
	date, exact := parseDate(raw, now)
	switch {
	case raw == "":
		return e, false // an event with no date at all is not an event
	case date == "":
		e.Date, e.Raw, e.Confidence = "", raw, Guessed
	case !exact:
		e.Date, e.Raw, e.Confidence = date, raw, Guessed
	default:
		e.Date = date
	}
	if e.Date != "" && tooFar(e.Date, now) {
		e.Confidence = Guessed
	}

	if end, _ := parseDate(clip(e.EndDate, maxTitle), now); end != "" && e.Date != "" && end > e.Date {
		e.EndDate = end
	} else {
		e.EndDate = ""
	}

	// A range in one field ("5-7 PM") is split before checking.
	if e.EndTime == "" {
		e.StartTime, e.EndTime = splitRange(e.StartTime)
	}
	start, okStart, guessStart := normTime(e.StartTime)
	end, okEnd, guessEnd := normTime(e.EndTime)
	if strings.TrimSpace(e.StartTime) != "" && !okStart {
		e.Confidence = Guessed // a time was there and made no sense
	}
	if guessStart || (okStart && guessEnd) {
		e.Confidence = Guessed
	}
	e.StartTime, e.EndTime = "", ""
	if okStart {
		e.StartTime = start
		if okEnd && end > start {
			e.EndTime = end
		}
	}
	return e, true
}

func checkAction(a Action, now time.Time) (Action, bool) {
	a.Text = clip(a.Text, maxText)
	if a.Text == "" {
		return a, false
	}
	a.Kind = strings.ToLower(strings.TrimSpace(a.Kind))
	if !actionKinds[a.Kind] {
		a.Kind = "other"
	}
	a.Done, a.Raw = false, ""
	raw := clip(a.Due, maxTitle)
	if raw == "" {
		a.Due, a.Confidence = "", ""
		return a, true
	}
	if a.Confidence != Read {
		a.Confidence = Guessed
	}
	due, exact := parseDate(raw, now)
	switch {
	case due == "":
		a.Due, a.Raw, a.Confidence = "", raw, Guessed
	case !exact:
		a.Due, a.Raw, a.Confidence = due, raw, Guessed
	default:
		a.Due = due
	}
	if a.Due != "" && tooFar(a.Due, now) {
		a.Confidence = Guessed
	}
	return a, true
}

// addEvent appends e unless it repeats an event already in the list (same
// day, same start time, the same title or one title inside the other); a
// repeat only fills in what the first one was missing.
func addEvent(list []Event, e Event) []Event {
	for i := range list {
		f := &list[i]
		if f.Date != e.Date || f.Date == "" || !sameEvent(f, &e) {
			continue
		}
		if f.EndDate == "" {
			f.EndDate = e.EndDate
		}
		if f.StartTime == "" {
			f.StartTime, f.EndTime = e.StartTime, e.EndTime
		} else if f.EndTime == "" && e.StartTime == f.StartTime {
			f.EndTime = e.EndTime
		}
		if f.Location == "" {
			f.Location = e.Location
		}
		if e.Confidence == Guessed {
			f.Confidence = Guessed
		}
		return list
	}
	return append(list, e)
}

func sameEvent(a, b *Event) bool {
	wa, wb := words(a.Title), words(b.Title)
	if strings.Join(wa, " ") == strings.Join(wb, " ") {
		return a.StartTime == b.StartTime || a.StartTime == "" || b.StartTime == ""
	}
	return a.StartTime == b.StartTime && (contains(wa, wb) || contains(wb, wa))
}

func hasAction(list []Action, a Action) bool {
	key := strings.Join(words(a.Text), " ")
	for _, b := range list {
		if strings.Join(words(b.Text), " ") == key {
			return true
		}
	}
	return false
}

var fillers = map[string]bool{"the": true, "a": true, "an": true, "to": true, "at": true, "of": true, "for": true, "and": true, "on": true, "in": true}

// words lowercases a title into its meaningful words.
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if !fillers[w] {
			out = append(out, w)
		}
	}
	return out
}

// contains reports whether every word of small appears in big.
func contains(big, small []string) bool {
	if len(small) == 0 {
		return false
	}
	set := map[string]bool{}
	for _, w := range big {
		set[w] = true
	}
	for _, w := range small {
		if !set[w] {
			return false
		}
	}
	return true
}

// clip trims s, folds runs of white space into one space and cuts it to n
// characters.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = strings.TrimSpace(string(r[:n]))
	}
	return s
}

func tooFar(date string, now time.Time) bool {
	d, err := time.ParseInLocation(isoDate, date, now.Location())
	if err != nil {
		return true
	}
	today := midnight(now)
	return d.After(today.AddDate(0, 18, 0)) || d.Before(today.AddDate(0, -18, 0))
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

var (
	isoRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	ordinalRe = regexp.MustCompile(`(\d)(st|nd|rd|th)\b`)
	weekdays  = map[string]time.Weekday{
		"sunday": time.Sunday, "sun": time.Sunday, "monday": time.Monday, "mon": time.Monday,
		"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday,
		"wednesday": time.Wednesday, "wed": time.Wednesday, "thursday": time.Thursday,
		"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday,
		"friday": time.Friday, "fri": time.Friday, "saturday": time.Saturday, "sat": time.Saturday,
	}
	withYear    = []string{"2006-1-2", "2006/1/2", "1/2/2006", "1/2/06", "1-2-2006", "January 2 2006", "Jan 2 2006", "2 January 2006", "2 Jan 2006"}
	withoutYear = []string{"1/2", "1-2", "January 2", "Jan 2", "2 January", "2 Jan"}
)

// parseDate reads a date the way a parent would. exact is true only for a
// valid YYYY-MM-DD; anything else that still resolves to a day ("Friday",
// "Nov. 4", "11/4") comes back with exact false so it is marked guessed.
func parseDate(s string, now time.Time) (date string, exact bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if isoRe.MatchString(s) {
		if _, err := time.Parse(isoDate, s); err == nil {
			return s, true
		}
		return "", false
	}

	t := strings.ToLower(s)
	t = strings.NewReplacer(",", " ", ".", " ", "sept", "sep").Replace(t)
	t = ordinalRe.ReplaceAllString(t, "$1")
	f := strings.Fields(t)
	if len(f) > 0 && (f[0] == "this" || f[0] == "next" || f[0] == "on" || f[0] == "by") {
		f = f[1:]
	}
	today := midnight(now)
	switch {
	case len(f) == 1 && f[0] == "today":
		return today.Format(isoDate), false
	case len(f) == 1 && f[0] == "tomorrow":
		return today.AddDate(0, 0, 1).Format(isoDate), false
	case len(f) == 1:
		if wd, ok := weekdays[f[0]]; ok {
			return today.AddDate(0, 0, (int(wd)-int(today.Weekday())+7)%7).Format(isoDate), false
		}
	}
	if len(f) > 1 {
		if _, ok := weekdays[f[0]]; ok {
			f = f[1:] // "Wednesday November 4": the weekday adds nothing
		}
	}
	t = strings.Join(f, " ")
	for _, layout := range withYear {
		if d, err := time.ParseInLocation(layout, t, now.Location()); err == nil {
			return d.Format(isoDate), false
		}
	}
	for _, layout := range withoutYear {
		if d, err := time.ParseInLocation(layout, t, now.Location()); err == nil {
			return closestYear(d, today).Format(isoDate), false
		}
	}
	return "", false
}

// closestYear puts a month and day without a year into the year that
// makes it nearest to today, leaning to the future.
func closestYear(d, today time.Time) time.Time {
	c := time.Date(today.Year(), d.Month(), d.Day(), 0, 0, 0, 0, today.Location())
	switch {
	case c.Before(today.AddDate(0, -6, 0)):
		return c.AddDate(1, 0, 0)
	case c.After(today.AddDate(0, 6, 0)):
		return c.AddDate(-1, 0, 0)
	}
	return c
}

var timeRe = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm|a|p)?$`)

// normTime turns "3 pm", "3:30PM" or "15:30" into "15:30". guessed is set
// for a bare afternoon hour like "3:30", read as PM because school does not
// happen at 3:30 in the night.
func normTime(s string) (hhmm string, ok, guessed bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.NewReplacer(" ", "", ".", "", "o'clock", "").Replace(t)
	switch t {
	case "":
		return "", false, false
	case "noon", "midday":
		return "12:00", true, false
	}
	m := timeRe.FindStringSubmatch(t)
	if m == nil {
		return "", false, false
	}
	h, _ := strconv.Atoi(m[1])
	min := 0
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
	}
	if min > 59 {
		return "", false, false
	}
	switch suffix := m[3]; {
	case suffix != "":
		if h < 1 || h > 12 {
			return "", false, false
		}
		if h == 12 {
			h = 0
		}
		if suffix[0] == 'p' {
			h += 12
		}
	case m[2] == "":
		return "", false, false // a bare number is not a time
	case h > 23:
		return "", false, false
	case h >= 1 && h <= 6:
		h += 12
		guessed = true
	}
	return fmt.Sprintf("%02d:%02d", h, min), true, guessed
}

var rangeRe = regexp.MustCompile(`^(.+?)\s*(?:-|–|—|to|until)\s*(.+)$`)

// splitRange splits "5-7 PM" into "5 PM" and "7 PM". Anything that is not
// a range comes back as the start.
func splitRange(s string) (start, end string) {
	m := rangeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return s, ""
	}
	start, end = strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	low := strings.ToLower(end)
	if !strings.ContainsAny(strings.ToLower(start), "ap") {
		for _, suf := range []string{"am", "pm", "a.m.", "p.m."} {
			if strings.HasSuffix(low, suf) {
				start += " " + suf
				break
			}
		}
	}
	return start, end
}
