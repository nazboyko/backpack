package week

import (
	"testing"
	"time"

	"github.com/nazboyko/backpack/internal/reader"
	"github.com/nazboyko/backpack/internal/store"
)

var monday = time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)

func paper(id string, r reader.Reading) store.Paper {
	return store.Paper{ID: id, Status: store.Done, Reading: r}
}

func TestDatesSortedAndPastGroupedEarlier(t *testing.T) {
	w := Build([]store.Paper{
		paper("p1", reader.Reading{Title: "Pizza night", Events: []reader.Event{
			{Title: "Pizza night", Date: "2026-11-05", StartTime: "17:00"},
		}}),
		paper("p2", reader.Reading{Title: "Spirit week", Events: []reader.Event{
			{Title: "Twin day", Date: "2026-10-28"},
			{Title: "Last week's thing", Date: "2026-10-20"},
			{Title: "Today", Date: "2026-10-26"},
			{Title: "Unreadable", Raw: "the Friday after break"},
		}}),
		paper("p3", reader.Reading{Title: "Book fair", Events: []reader.Event{
			{Title: "Book fair", Date: "2026-10-25", EndDate: "2026-10-27"}, // still on
			{Title: "Family night", Date: "2026-11-05", StartTime: "16:00"},
			{Title: "All-day thing", Date: "2026-11-05"},
		}}),
		{ID: "p4", Status: store.Queued, Reading: reader.Reading{Events: []reader.Event{{Title: "not read yet", Date: "2026-10-30"}}}},
	}, monday)

	var got []string
	for _, d := range w.Dates {
		got = append(got, d.Title)
	}
	want := []string{"Book fair", "Today", "Twin day", "All-day thing", "Family night", "Pizza night", "Unreadable"}
	if len(got) != len(want) {
		t.Fatalf("dates %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dates %v, want %v", got, want)
		}
	}
	if len(w.Earlier) != 1 || w.Earlier[0].Title != "Last week's thing" {
		t.Fatalf("earlier %+v", w.Earlier)
	}
	if d := w.Dates[2]; d.PaperID != "p2" || d.Index != 0 || d.Paper != "Spirit week" {
		t.Fatalf("row does not point back at its paper: %+v", d)
	}
}

func TestOpenTodosBeforeDone(t *testing.T) {
	w := Build([]store.Paper{
		paper("p1", reader.Reading{Title: "Field trip", Actions: []reader.Action{
			{Text: "Return library book", Done: true},
			{Text: "Pack a lunch", Due: "2026-11-04"},
			{Text: "Smile"},
			{Text: "Sign the slip", Due: "2026-10-30"},
		}}),
	}, monday)
	var got []string
	for _, a := range w.Todos {
		got = append(got, a.Text)
	}
	want := []string{"Sign the slip", "Pack a lunch", "Smile", "Return library book"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("todos %v, want %v", got, want)
		}
	}
	if w.Open != 3 {
		t.Fatalf("open %d, want 3", w.Open)
	}
	if w.Todos[3].Index != 0 {
		t.Fatal("done todo lost its index")
	}
}

func TestNothingToDoCount(t *testing.T) {
	w := Build([]store.Paper{
		paper("a", reader.Reading{Title: "Newsletter", NothingToDo: true}),
		paper("b", reader.Reading{Title: "Menu", NothingToDo: true}),
		paper("c", reader.Reading{Title: "Slip", Actions: []reader.Action{{Text: "Sign"}}}),
		{ID: "d", Status: store.Failed, Reading: reader.Reading{NothingToDo: true}},
	}, monday)
	if w.Nothing != 2 {
		t.Fatalf("nothing %d, want 2", w.Nothing)
	}
}

func TestEmptyWeekHasEmptyLists(t *testing.T) {
	w := Build(nil, monday)
	if w.Dates == nil || w.Earlier == nil || w.Todos == nil {
		t.Fatal("lists must be [] in JSON, not null")
	}
}

func TestLabel(t *testing.T) {
	cases := map[string]string{
		"2026-10-26": "Week of Oct 26", // Monday
		"2026-10-28": "Week of Oct 26", // Wednesday
		"2026-10-30": "Week of Oct 26", // Friday
		"2026-10-24": "Week of Oct 26", // Saturday: planning the coming week
		"2026-10-25": "Week of Oct 26", // Sunday
		"2026-11-01": "Week of Nov 2",
	}
	for day, want := range cases {
		d, _ := time.ParseInLocation("2006-01-02", day, time.Local)
		if got := Label(d.Add(20 * time.Hour)); got != want {
			t.Errorf("%s: %q, want %q", day, got, want)
		}
	}
}
