package ics

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

var stamp = time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)

func off() *bool { f := false; return &f }

func papers() []store.Paper {
	return []store.Paper{
		{ID: "aaaaaaaaaaaa", Status: store.Done, Reading: reader.Reading{
			Title: "Picture day flyer",
			Events: []reader.Event{
				{Title: "Picture day", Date: "2026-11-03"},
			},
		}},
		{ID: "bbbbbbbbbbbb", Status: store.Done, Reading: reader.Reading{
			Title: "Field trip slip",
			Events: []reader.Event{
				{Title: "Field trip", Date: "2026-11-04", StartTime: "09:00", EndTime: "14:15", Location: "Riverside Science Museum"},
				{Title: "Not wanted", Date: "2026-11-05", Include: off()},
				{Title: "No date", Raw: "soon"},
			},
		}},
		{ID: "cccccccccccc", Status: store.Queued},
	}
}

func TestGolden(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, Items(papers(), nil), stamp); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/two-events.ics")
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Fatalf("ics differs from golden:\n%s", b.String())
	}
}

func TestItemsSkipsSwitchedOffAndDateless(t *testing.T) {
	items := Items(papers(), nil)
	if len(items) != 2 {
		t.Fatalf("%d items, want 2", len(items))
	}
	if items[0].UID != "aaaaaaaaaaaa-0@backpack.local" || items[1].UID != "bbbbbbbbbbbb-0@backpack.local" {
		t.Fatalf("uids %s %s", items[0].UID, items[1].UID)
	}
}

func TestItemsFilterByIDs(t *testing.T) {
	if got := Items(papers(), []string{"bbbbbbbbbbbb"}); len(got) != 1 || got[0].Event.Title != "Field trip" {
		t.Fatalf("paper filter: %+v", got)
	}
	if got := Items(papers(), []string{"aaaaaaaaaaaa-0", " "}); len(got) != 1 || got[0].Event.Title != "Picture day" {
		t.Fatalf("event filter: %+v", got)
	}
	if got := Items(papers(), []string{"bbbbbbbbbbbb-1"}); len(got) != 0 {
		t.Fatalf("a switched-off event came back through the filter: %+v", got)
	}
}

func TestLinesAreCRLFAndFolded(t *testing.T) {
	long := strings.Repeat("Bring a lunch, a water bottle; and a hat. ", 6) + "Été"
	var b bytes.Buffer
	Write(&b, []Item{{UID: "x@backpack.local", Paper: "P", Event: reader.Event{Title: long, Date: "2026-11-04"}}}, stamp)
	out := b.String()
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Fatal("bare LF in output")
	}
	if !strings.HasSuffix(out, "END:VCALENDAR\r\n") {
		t.Fatal("last line not CRLF terminated")
	}
	for _, l := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Errorf("line of %d octets: %q", len(l), l)
		}
		if !strings.HasPrefix(l, " ") && !strings.Contains(l, ":") {
			t.Errorf("line is neither a property nor a continuation: %q", l)
		}
	}
	// Unfolding gives back the escaped summary in one piece.
	unfolded := strings.ReplaceAll(out, "\r\n ", "")
	if !strings.Contains(unfolded, "SUMMARY:"+escape(long)+"\r\n") {
		t.Fatal("summary did not survive folding")
	}
}

func TestEscaping(t *testing.T) {
	got := escape("Pizza, pop; and a note\nsecond line \\ end")
	want := `Pizza\, pop\; and a note\nsecond line \\ end`
	if got != want {
		t.Fatalf("escape = %q, want %q", got, want)
	}
}

func TestSpans(t *testing.T) {
	cases := []struct {
		e          reader.Event
		start, end string
	}{
		{reader.Event{Date: "2026-11-09", EndDate: "2026-11-10"}, ";VALUE=DATE:20261109", ";VALUE=DATE:20261111"},
		{reader.Event{Date: "2026-10-29", StartTime: "12:30"}, ":20261029T123000", ":20261029T133000"},
		{reader.Event{Date: "2026-11-05", StartTime: "17:00", EndTime: "19:00"}, ":20261105T170000", ":20261105T190000"},
		{reader.Event{Date: "2026-12-31", StartTime: "23:30"}, ":20261231T233000", ":20270101T003000"},
		{reader.Event{Date: "2026-11-06", EndDate: "2026-11-08", StartTime: "18:00", EndTime: "12:00"}, ":20261106T180000", ":20261108T120000"},
		{reader.Event{Date: "2026-11-06", EndDate: "2026-11-08", StartTime: "18:00"}, ":20261106T180000", ":20261108T190000"},
	}
	for _, c := range cases {
		s, e, ok := span(c.e)
		if !ok || s != c.start || e != c.end {
			t.Errorf("%+v: %q %q %v, want %q %q", c.e, s, e, ok, c.start, c.end)
		}
	}
}

func TestUIDFollowsTheEventNotItsPlace(t *testing.T) {
	ps := []store.Paper{{ID: "pppppppppppp", Status: store.Done, Reading: reader.Reading{Title: "P", Events: []reader.Event{
		{ID: "e2", Title: "Second", Date: "2026-11-04"},
	}}}}
	items := Items(ps, nil)
	if items[0].UID != "pppppppppppp-e2@backpack.local" {
		t.Fatalf("uid %s", items[0].UID)
	}
	if got := Items(ps, []string{"pppppppppppp-e2"}); len(got) != 1 {
		t.Fatal("event id filter missed")
	}
}

func TestFoldDoesNotHangOnBadUTF8(t *testing.T) {
	done := make(chan string)
	go func() { done <- fold(strings.Repeat("\x80", 200)) }()
	select {
	case out := <-done:
		if !strings.HasSuffix(out, "\r\n") {
			t.Fatal("no line end")
		}
	case <-time.After(time.Second):
		t.Fatal("fold loops on bytes that are not UTF-8")
	}
}
