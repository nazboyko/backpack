package reader

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

// Schema is the JSON schema sent to Ollama as `format`, so the model can
// only answer in the shape of a Reading.
//
//go:embed schema.json
var Schema json.RawMessage

// Date confidence values. The model sets read or guessed; a date a person
// has typed in is checked.
const (
	Read    = "read"
	Guessed = "guessed"
	Checked = "checked"
)

// Reading is what one paper says, as the model read it and Validate
// cleaned it.
type Reading struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Kind        string   `json:"kind"`
	NothingToDo bool     `json:"nothing_to_do"`
	Events      []Event  `json:"events"`
	Actions     []Action `json:"actions"`
}

// Event is a date to put in the calendar. Dates are YYYY-MM-DD and times
// HH:MM (24 h) once validated. Raw keeps what the model wrote when it was
// not a date, so the parent can see it.
type Event struct {
	Title      string `json:"title"`
	Date       string `json:"date"`
	EndDate    string `json:"end_date,omitempty"`
	StartTime  string `json:"start_time,omitempty"`
	EndTime    string `json:"end_time,omitempty"`
	Location   string `json:"location,omitempty"`
	Confidence string `json:"date_confidence"`
	Raw        string `json:"raw,omitempty"`
	Include    *bool  `json:"include,omitempty"` // nil or true: goes into the .ics
}

// Action is something the parent has to do.
type Action struct {
	Text       string `json:"text"`
	Kind       string `json:"kind"`
	Due        string `json:"due,omitempty"`
	Confidence string `json:"date_confidence,omitempty"`
	Raw        string `json:"raw,omitempty"`
	Done       bool   `json:"done,omitempty"`
}

const promptText = `You are reading a photo of one paper that came home from school.
Today is %s. The school year is %s.
Extract what a busy parent must know. Resolve relative dates ("this Friday", "next week") against today and return ISO dates (YYYY-MM-DD). If a date has no year, assume the one that is in the future and closest. If you cannot read a date with confidence, set date_confidence to "guessed". Do not invent dates or tasks that are not on the paper. If the paper asks nothing of the parent and has no dates, set nothing_to_do to true. Keep title under 6 words and summary under 20 words. Write in English.`

// Prompt is the text half of the request; the photo is the other half.
func Prompt(now time.Time) string {
	return fmt.Sprintf(promptText, now.Format("Monday, 2006-01-02"), schoolYear(now))
}

// schoolYear names the school year that contains now, starting in August.
func schoolYear(now time.Time) string {
	y := now.Year()
	if now.Month() < time.August {
		y--
	}
	return fmt.Sprintf("%d-%d", y, y+1)
}
