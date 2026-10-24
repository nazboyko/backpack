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
	ID         string `json:"id,omitempty"` // set by Backpack, stays the same through edits
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
	ID         string `json:"id,omitempty"` // set by Backpack, stays the same through edits
	Text       string `json:"text"`
	Kind       string `json:"kind"`
	Due        string `json:"due,omitempty"`
	Confidence string `json:"date_confidence,omitempty"`
	Raw        string `json:"raw,omitempty"`
	Done       bool   `json:"done,omitempty"`
}

const promptText = `You are reading a photo of one paper that came home from school. A busy parent wants to know what goes in the calendar and what they have to do.
Today is %s. The school year is %s.

Events are things that happen on a day: a trip, a day off, an early dismissal, a fair, a meeting, a themed dress-up day. Give each event its date as YYYY-MM-DD, and its start_time and end_time as HH:MM (24-hour) when the paper prints a clock time. Leave the times empty otherwise: "during lunch" or "after school" is not a time. Something that runs over several days gets date and end_date. One event per happening.

Actions are things the parent must do: sign a form, return a paper, bring or send an item, pay or send money, sign up or reply. List each one separately, start it with a verb, and put its deadline in due as YYYY-MM-DD when the paper gives one. A deadline belongs to the action as its due date; it is not an event.

Dates: resolve weekdays and relative dates ("Friday", "next week") against today. A date without a year is the closest one to today, usually in the coming weeks. Set date_confidence to "read" when the paper prints the date, even without a year. Set it to "guessed" only when the paper gives just a weekday or a relative date, or when you cannot read the date clearly. The same rule holds for the due date of an action.

Use only dates that are on the paper. Never give today's date to something that already happened or has no date: a newsletter about the past week has no events. If the paper asks nothing of the parent and has no dates, set nothing_to_do to true.

Keep title under 6 words and summary under 20 words. Write in English.`

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
