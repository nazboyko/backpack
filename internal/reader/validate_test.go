package reader

import (
	"reflect"
	"strings"
	"testing"
)

// Monday 2026-10-26 is "today" in every validation test.
var monday = day("2026-10-26")

func TestValidateKeepsAGoodReading(t *testing.T) {
	in := Reading{
		Title: "Field trip permission slip", Summary: "Museum trip; slip and $8 due Friday.",
		Kind: "form",
		Events: []Event{{Title: "Field trip", Date: "2026-11-04", StartTime: "09:00", EndTime: "14:15",
			Location: "Riverside Science Museum", Confidence: Read}},
		Actions: []Action{{Text: "Sign and return the slip", Kind: "sign", Due: "2026-10-30", Confidence: Read}},
	}
	got := Validate(in, monday)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("a valid reading changed:\n got %+v\nwant %+v", got, in)
	}
}

func TestValidateDates(t *testing.T) {
	cases := []struct {
		name, date, wantDate, wantConf, wantRaw string
	}{
		{"iso read", "2026-11-04", "2026-11-04", Read, ""},
		{"weekday only", "Friday", "2026-10-30", Guessed, "Friday"},
		{"this weekday", "this Friday", "2026-10-30", Guessed, "this Friday"},
		{"weekday is today", "Monday", "2026-10-26", Guessed, "Monday"},
		{"month day no year", "Nov. 4", "2026-11-04", Guessed, "Nov. 4"},
		{"long with weekday", "Wednesday, November 4", "2026-11-04", Guessed, "Wednesday, November 4"},
		{"us slashes", "11/5", "2026-11-05", Guessed, "11/5"},
		{"us with year", "11/12/2026", "2026-11-12", Guessed, "11/12/2026"},
		{"ordinal", "November 9th", "2026-11-09", Guessed, "November 9th"},
		{"early next year", "January 8", "2027-01-08", Guessed, "January 8"},
		{"nonsense", "sometime soon", "", Guessed, "sometime soon"},
		{"bad iso", "2026-13-40", "", Guessed, "2026-13-40"},
		{"far future", "2029-05-01", "2029-05-01", Guessed, ""},
		{"far past", "2024-01-10", "2024-01-10", Guessed, ""},
	}
	for _, c := range cases {
		r := Validate(Reading{Title: "Paper", Events: []Event{{Title: "x", Date: c.date, Confidence: Read}}}, monday)
		if len(r.Events) != 1 {
			t.Errorf("%s: %d events, want 1", c.name, len(r.Events))
			continue
		}
		e := r.Events[0]
		if e.Date != c.wantDate || e.Confidence != c.wantConf || e.Raw != c.wantRaw {
			t.Errorf("%s: %q -> date %q conf %q raw %q; want %q %q %q",
				c.name, c.date, e.Date, e.Confidence, e.Raw, c.wantDate, c.wantConf, c.wantRaw)
		}
	}
}

func TestValidateDropsEventWithoutAnyDate(t *testing.T) {
	r := Validate(Reading{Title: "P", Events: []Event{{Title: "Something", Date: "  "}}}, monday)
	if len(r.Events) != 0 {
		t.Fatalf("kept %+v", r.Events)
	}
}

func TestValidateUnknownConfidenceIsGuessed(t *testing.T) {
	r := Validate(Reading{Title: "P", Events: []Event{{Title: "x", Date: "2026-11-04", Confidence: "sure"}}}, monday)
	if r.Events[0].Confidence != Guessed {
		t.Fatalf("confidence %q, want guessed", r.Events[0].Confidence)
	}
}

func TestValidateEndDate(t *testing.T) {
	cases := map[string]string{
		"2026-11-10": "2026-11-10", // after the start: kept
		"2026-11-09": "",           // same day: dropped
		"2026-11-01": "",           // before the start: dropped
		"Tuesday":    "",           // resolves to 10-27, before the start
		"nonsense":   "",
	}
	for in, want := range cases {
		r := Validate(Reading{Title: "P", Events: []Event{{Title: "Book fair", Date: "2026-11-09", EndDate: in, Confidence: Read}}}, monday)
		if got := r.Events[0].EndDate; got != want {
			t.Errorf("end %q -> %q, want %q", in, got, want)
		}
	}
}

func TestNormTime(t *testing.T) {
	cases := []struct {
		in, want    string
		ok, guessed bool
	}{
		{"3 pm", "15:00", true, false},
		{"3:30PM", "15:30", true, false},
		{"12:30 PM", "12:30", true, false},
		{"12 am", "00:00", true, false},
		{"9:00 AM", "09:00", true, false},
		{"9:00 a.m.", "09:00", true, false},
		{"09:00", "09:00", true, false},
		{"15:00", "15:00", true, false},
		{"noon", "12:00", true, false},
		{"3:30", "15:30", true, true},
		{"25:00", "", false, false},
		{"7:60", "", false, false},
		{"13 pm", "", false, false},
		{"5", "", false, false},
		{"after lunch", "", false, false},
		{"", "", false, false},
	}
	for _, c := range cases {
		got, ok, guessed := normTime(c.in)
		if got != c.want || ok != c.ok || guessed != c.guessed {
			t.Errorf("normTime(%q) = %q %v %v, want %q %v %v", c.in, got, ok, guessed, c.want, c.ok, c.guessed)
		}
	}
}

func TestValidateTimes(t *testing.T) {
	cases := []struct {
		name, start, end, wantStart, wantEnd, wantConf string
	}{
		{"pm range", "5:00 PM", "7:00 PM", "17:00", "19:00", Read},
		{"range in one field", "5-7 PM", "", "17:00", "19:00", Read},
		{"range with to", "3:30 to 7:00 PM", "", "15:30", "19:00", Read},
		{"end before start", "17:00", "16:00", "17:00", "", Read},
		{"end without start", "", "19:00", "", "", Read},
		{"bad start", "after school", "", "", "", Guessed},
		{"bare afternoon hour", "3:30", "", "15:30", "", Guessed},
	}
	for _, c := range cases {
		r := Validate(Reading{Title: "P", Events: []Event{{Title: "x", Date: "2026-11-05",
			StartTime: c.start, EndTime: c.end, Confidence: Read}}}, monday)
		e := r.Events[0]
		if e.StartTime != c.wantStart || e.EndTime != c.wantEnd || e.Confidence != c.wantConf {
			t.Errorf("%s: got %q-%q %s, want %q-%q %s", c.name, e.StartTime, e.EndTime, e.Confidence,
				c.wantStart, c.wantEnd, c.wantConf)
		}
	}
}

func TestValidateCollapsesDuplicateEvents(t *testing.T) {
	r := Validate(Reading{Title: "Field trip", Events: []Event{
		{Title: "Field trip", Date: "2026-11-04", StartTime: "9:00 AM", Confidence: Read},
		{Title: "Field Trip to the Riverside Science Museum", Date: "2026-11-04", StartTime: "09:00",
			EndTime: "14:15", Location: "Riverside Science Museum", Confidence: Read},
		{Title: "field trip!", Date: "2026-11-04", Confidence: Read},
		{Title: "Field trip", Date: "2026-11-05", Confidence: Read}, // another day: kept
	}}, monday)
	if len(r.Events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(r.Events), r.Events)
	}
	e := r.Events[0]
	if e.Title != "Field trip" || e.EndTime != "14:15" || e.Location != "Riverside Science Museum" {
		t.Fatalf("merge lost details: %+v", e)
	}
}

func TestValidateKeepsDifferentEventsOnOneDay(t *testing.T) {
	r := Validate(Reading{Title: "Book fair", Events: []Event{
		{Title: "Book fair", Date: "2026-11-10", Confidence: Read},
		{Title: "Book fair family night", Date: "2026-11-10", StartTime: "17:00", EndTime: "19:00", Confidence: Read},
	}}, monday)
	if len(r.Events) != 2 {
		t.Fatalf("merged two different events: %+v", r.Events)
	}
}

func TestValidateNothingToDo(t *testing.T) {
	withAction := Validate(Reading{Title: "P", NothingToDo: true,
		Actions: []Action{{Text: "Send $8", Kind: "pay"}}}, monday)
	if withAction.NothingToDo {
		t.Error("nothing_to_do kept although the paper has a task")
	}
	withEvent := Validate(Reading{Title: "P", NothingToDo: true,
		Events: []Event{{Title: "Picture day", Date: "2026-11-03", Confidence: Read}}}, monday)
	if withEvent.NothingToDo {
		t.Error("nothing_to_do kept although the paper has a date")
	}
	empty := Validate(Reading{Title: "Newsletter", NothingToDo: false}, monday)
	if !empty.NothingToDo {
		t.Error("a paper with no dates and no tasks should need nothing")
	}
	if empty.Events == nil || empty.Actions == nil {
		t.Error("empty lists should be [] in JSON, not null")
	}
}

func TestValidateCapsAndTrims(t *testing.T) {
	long := strings.Repeat("word ", 100)
	r := Validate(Reading{
		Title: "  " + long, Summary: long + long, Kind: "Flyer",
		Events:  []Event{{Title: long, Date: "2026-11-04", Confidence: Read}},
		Actions: []Action{{Text: "  Bring\n a   lunch  ", Kind: "BRING"}, {Text: long + long + long, Kind: "homework"}},
	}, monday)
	if n := len([]rune(r.Title)); n > 60 {
		t.Errorf("title %d chars", n)
	}
	if n := len([]rune(r.Summary)); n > 200 {
		t.Errorf("summary %d chars", n)
	}
	if n := len([]rune(r.Events[0].Title)); n > 60 {
		t.Errorf("event title %d chars", n)
	}
	if r.Actions[0].Text != "Bring a lunch" || r.Actions[0].Kind != "bring" {
		t.Errorf("action not trimmed: %+v", r.Actions[0])
	}
	if n := len([]rune(r.Actions[1].Text)); n > 200 {
		t.Errorf("action %d chars", n)
	}
	if r.Kind != "flyer" || r.Actions[1].Kind != "other" {
		t.Errorf("kinds: paper %q action %q", r.Kind, r.Actions[1].Kind)
	}
}

func TestValidateEmptyTitles(t *testing.T) {
	r := Validate(Reading{Title: " ", Events: []Event{{Title: "", Date: "2026-11-03", Confidence: Read}},
		Actions: []Action{{Text: "  ", Kind: "sign"}}}, monday)
	if r.Title != "School paper" {
		t.Errorf("paper title %q", r.Title)
	}
	if r.Events[0].Title != "School paper" {
		t.Errorf("event title %q, want the paper title", r.Events[0].Title)
	}
	if len(r.Actions) != 0 {
		t.Errorf("kept an empty action")
	}
}

func TestValidateActionDue(t *testing.T) {
	cases := []struct {
		due, conf, wantDue, wantConf, wantRaw string
	}{
		{"2026-10-30", Read, "2026-10-30", Read, ""},
		{"Friday", Read, "2026-10-30", Guessed, "Friday"},
		{"before the trip", Read, "", Guessed, "before the trip"},
		{"", Read, "", "", ""},
		{"2026-10-30", "", "2026-10-30", Guessed, ""},
	}
	for _, c := range cases {
		r := Validate(Reading{Title: "P", Actions: []Action{{Text: "Sign the slip", Kind: "sign", Due: c.due, Confidence: c.conf}}}, monday)
		a := r.Actions[0]
		if a.Due != c.wantDue || a.Confidence != c.wantConf || a.Raw != c.wantRaw {
			t.Errorf("due %q: got %q %q %q, want %q %q %q", c.due, a.Due, a.Confidence, a.Raw, c.wantDue, c.wantConf, c.wantRaw)
		}
	}
}

func TestValidateDropsRepeatedActions(t *testing.T) {
	r := Validate(Reading{Title: "P", Actions: []Action{
		{Text: "Send $8.", Kind: "pay"}, {Text: "send $8", Kind: "pay"}, {Text: "Pack a lunch", Kind: "bring"},
	}}, monday)
	if len(r.Actions) != 2 {
		t.Fatalf("got %d actions, want 2", len(r.Actions))
	}
}

func TestValidateCapsListLength(t *testing.T) {
	var events []Event
	for i := 0; i < 100; i++ {
		events = append(events, Event{Title: strings.Repeat("e", i+1), Date: "2026-11-04", StartTime: "", Confidence: Read})
	}
	if r := Validate(Reading{Title: "P", Events: events}, monday); len(r.Events) > maxItems {
		t.Fatalf("%d events kept", len(r.Events))
	}
}
