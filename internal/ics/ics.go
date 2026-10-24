// Package ics writes the week's dates as an iCalendar file that Apple
// Calendar, Google Calendar and Outlook can import.
package ics

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

// Item is one event and the paper it came from.
type Item struct {
	UID   string
	Paper string
	Event reader.Event
}

// Items picks the events that go into the file: read papers only, events
// with a date, not switched off by the parent. ids, when given, keeps only
// those papers ("<paper id>") or single events ("<paper id>-<event id>").
// The UID comes from the event's own id, so importing the file again
// updates the same calendar entries.
func Items(papers []store.Paper, ids []string) []Item {
	want := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	var out []Item
	for _, p := range papers {
		if p.Status != store.Done {
			continue
		}
		for n, e := range p.Reading.Events {
			name := e.ID
			if name == "" {
				name = fmt.Sprint(n)
			}
			key := p.ID + "-" + name
			switch {
			case e.Date == "", e.Include != nil && !*e.Include:
				continue
			case len(want) > 0 && !want[p.ID] && !want[key]:
				continue
			}
			out = append(out, Item{UID: key + "@backpack.local", Paper: p.Reading.Title, Event: e})
		}
	}
	return out
}

// Write writes a VCALENDAR with one VEVENT per item. Times are floating
// local times: the file is made and opened on the same computer.
func Write(w io.Writer, items []Item, stamp time.Time) error {
	var b strings.Builder
	line := func(s string) { b.WriteString(fold(s)) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Backpack//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	for _, it := range items {
		start, end, ok := span(it.Event)
		if !ok {
			continue
		}
		line("BEGIN:VEVENT")
		line("UID:" + it.UID)
		line("DTSTAMP:" + stamp.UTC().Format("20060102T150405Z"))
		line("DTSTART" + start)
		line("DTEND" + end)
		line("SUMMARY:" + escape(it.Event.Title))
		if it.Event.Location != "" {
			line("LOCATION:" + escape(it.Event.Location))
		}
		line("DESCRIPTION:" + escape("From: "+it.Paper+"\nRead from a photo by Backpack"))
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	_, err := io.WriteString(w, b.String())
	return err
}

// span returns the DTSTART and DTEND values (with their parameters). An
// all-day event ends the day after its last day; a timed event without an
// end lasts one hour.
func span(e reader.Event) (start, end string, ok bool) {
	day, err := time.Parse("2006-01-02", e.Date)
	if err != nil {
		return "", "", false
	}
	last := day
	if e.EndDate != "" {
		if d, err := time.Parse("2006-01-02", e.EndDate); err == nil && d.After(day) {
			last = d
		}
	}
	if e.StartTime == "" {
		return ";VALUE=DATE:" + day.Format("20060102"), ";VALUE=DATE:" + last.AddDate(0, 0, 1).Format("20060102"), true
	}
	from, err := time.Parse("2006-01-02 15:04", e.Date+" "+e.StartTime)
	if err != nil {
		return "", "", false
	}
	to := from.Add(time.Hour)
	if last.After(day) {
		to = time.Date(last.Year(), last.Month(), last.Day(), from.Hour(), from.Minute(), 0, 0, time.UTC).Add(time.Hour)
	}
	if e.EndTime != "" {
		if t, err := time.Parse("2006-01-02 15:04", last.Format("2006-01-02")+" "+e.EndTime); err == nil && t.After(from) {
			to = t
		}
	}
	const local = "20060102T150405"
	return ":" + from.Format(local), ":" + to.Format(local), true
}

// escape follows RFC 5545 for TEXT values.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

// fold ends a content line with CRLF and breaks it so no physical line is
// longer than 75 octets, never inside a UTF-8 character.
func fold(s string) string {
	var b strings.Builder
	limit := 75
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 { // not UTF-8 at all; cut anyway rather than loop
			cut = limit
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = 74 // the leading space counts
	}
	b.WriteString(s)
	b.WriteString("\r\n")
	return b.String()
}
