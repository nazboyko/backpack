package reader

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t.Add(9 * time.Hour)
}

func TestPromptHasTodayAndSchoolYear(t *testing.T) {
	cases := []struct{ today, weekday, year string }{
		{"2026-10-26", "Monday, 2026-10-26", "2026-2027"},
		{"2027-03-05", "Friday, 2027-03-05", "2026-2027"},
		{"2026-08-01", "Saturday, 2026-08-01", "2026-2027"},
		{"2026-07-31", "Friday, 2026-07-31", "2025-2026"},
	}
	for _, c := range cases {
		p := Prompt(day(c.today))
		if !strings.Contains(p, "Today is "+c.weekday+".") {
			t.Errorf("%s: prompt lacks %q:\n%s", c.today, c.weekday, p)
		}
		if !strings.Contains(p, "The school year is "+c.year+".") {
			t.Errorf("%s: prompt lacks school year %s", c.today, c.year)
		}
	}
}

func TestSchemaMatchesPlan(t *testing.T) {
	golden, err := os.ReadFile("testdata/schema.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(Schema, &got); err != nil {
		t.Fatalf("schema.json is not JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("schema.json differs from testdata/schema.golden.json")
	}
}

// Every field the schema asks the model for has a home in the Go types,
// so nothing the model says is dropped on the floor.
func TestSchemaFieldsExistInTypes(t *testing.T) {
	var s struct {
		Properties map[string]struct {
			Items struct {
				Properties map[string]any `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Schema, &s); err != nil {
		t.Fatal(err)
	}
	check := func(typ reflect.Type, props []string) {
		tags := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			tags[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
		}
		for _, p := range props {
			if !tags[p] {
				t.Errorf("%s has no field for schema property %q", typ.Name(), p)
			}
		}
	}
	keys := func(m map[string]any) (out []string) {
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	var top []string
	for k := range s.Properties {
		top = append(top, k)
	}
	check(reflect.TypeOf(Reading{}), top)
	check(reflect.TypeOf(Event{}), keys(s.Properties["events"].Items.Properties))
	check(reflect.TypeOf(Action{}), keys(s.Properties["actions"].Items.Properties))
}
