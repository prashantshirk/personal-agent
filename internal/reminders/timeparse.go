package reminders

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var inDuration = regexp.MustCompile(`(?i)^(?:in|after)\s+(\d+)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)
var timeOfDay = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
var ampmSpace = regexp.MustCompile(`(?i)(\d{1,2}(?::\d{2})?)\s+(am|pm)\b`)

// ParseWhen resolves supported reminder phrases in the user's configured zone.
func ParseWhen(input string, now time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, fmt.Errorf("timezone is required")
	}
	s := strings.TrimSpace(strings.ToLower(input))
	localNow := now.In(loc)
	// relative: "in 3 hours", "after 30 minutes"
	if m := inDuration.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 {
			return time.Time{}, fmt.Errorf("duration must be positive")
		}
		d := time.Duration(n) * time.Minute
		if unit := m[2]; strings.HasPrefix(unit, "h") {
			d = time.Duration(n) * time.Hour
		} else if strings.HasPrefix(unit, "d") {
			d = time.Duration(n) * 24 * time.Hour
		}
		return now.Add(d), nil
	}
	// explicit machine formats
	if t, err := time.ParseInLocation(time.RFC3339, s, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, nil
	}
	// normalize natural-language phrasing, then split off any date keyword
	s = normalizeWhen(s)
	base := localNow
	haveDate := false
	var rest []string
	for _, tok := range strings.Fields(s) {
		switch {
		case tok == "tomorrow":
			base = localNow.AddDate(0, 0, 1)
			haveDate = true
		case tok == "today":
			haveDate = true
		default:
			if wd, ok := weekday(tok); ok {
				delta := (int(wd) - int(localNow.Weekday()) + 7) % 7
				if delta == 0 {
					delta = 7
				}
				base = localNow.AddDate(0, 0, delta)
				haveDate = true
			} else {
				rest = append(rest, tok)
			}
		}
	}
	if len(rest) == 1 {
		if t, err := time.ParseInLocation("2006-01-02", rest[0], loc); err == nil {
			return t, nil
		}
	}
	if len(rest) > 1 {
		return time.Time{}, fmt.Errorf("unsupported time expression %q", input)
	}
	tod := ""
	if len(rest) == 1 {
		tod = rest[0]
	}
	if tod == "" {
		if !haveDate {
			return time.Time{}, fmt.Errorf("unsupported time expression %q", input)
		}
		tod = "09:00" // a date with no clock time defaults to 9am
	}
	m := timeOfDay.FindStringSubmatch(tod)
	if m == nil {
		return time.Time{}, fmt.Errorf("unsupported time expression %q", input)
	}
	h, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	if minute > 59 {
		return time.Time{}, fmt.Errorf("minute must be below 60")
	}
	if ampm := m[3]; ampm != "" {
		if h < 1 || h > 12 {
			return time.Time{}, fmt.Errorf("invalid 12-hour time")
		}
		if ampm == "pm" && h < 12 {
			h += 12
		}
		if ampm == "am" && h == 12 {
			h = 0
		}
	} else if h > 23 {
		return time.Time{}, fmt.Errorf("hour must be below 24")
	}
	result := time.Date(base.Year(), base.Month(), base.Day(), h, minute, 0, 0, loc)
	if result.Before(localNow) && !haveDate {
		result = result.AddDate(0, 0, 1)
	}
	return result, nil
}

// normalizeWhen strips filler words and joins "7 am" -> "7am" so time tokens survive splitting.
func normalizeWhen(s string) string {
	for _, p := range []string{"every day", "everyday", "daily", "every week", "weekly", "every month", "monthly", "every"} {
		if strings.HasPrefix(s, p+" ") {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}
	for _, p := range []string{"in the morning", "in the evening", "in the afternoon", "at night", "o'clock"} {
		s = strings.ReplaceAll(s, p, "")
	}
	s = strings.ReplaceAll(s, "a.m.", "am")
	s = strings.ReplaceAll(s, "p.m.", "pm")
	s = ampmSpace.ReplaceAllString(s, "${1}${2}")
	var out []string
	for _, tok := range strings.Fields(s) {
		switch tok {
		case "at", "on", "@", "around", "by":
			continue
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}

func weekday(s string) (time.Weekday, bool) {
	for i, name := range []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"} {
		if s == name || s == name[:3] {
			return time.Weekday(i), true
		}
	}
	return 0, false
}
