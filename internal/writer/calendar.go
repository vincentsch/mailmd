package writer

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

// maxAttendees is how many attendees the body names. Larger meetings get
// "and 40 more".
const maxAttendees = 10

// calendarBody returns the first event of the message's calendar part as
// Markdown, or "" when there is none. Some invites, mostly from Outlook and
// Exchange, have no text part. The details of the event are then only in the
// calendar part, so mailmd writes them into the body, where a reader sees
// them and a search finds them. The parser reads only the few fields it
// shows. It doesn't expand repeating events or convert time zones.
func calendarBody(atts []mail.Attachment) string {
	for _, a := range atts {
		if a.Data == nil || !isCalendar(a) {
			continue
		}
		if ev, ok := parseCalendar(string(a.Data)); ok {
			return ev.markdown()
		}
	}
	return ""
}

func isCalendar(a mail.Attachment) bool {
	t := strings.ToLower(a.MIMEType)
	return t == "text/calendar" || t == "application/ics" || strings.HasSuffix(strings.ToLower(a.Filename), ".ics")
}

// icsProperty is one content line, like DTSTART;TZID=Europe/Berlin:20261029T100000.
type icsProperty struct {
	name   string
	params map[string]string
	value  string
}

type calendarEvent struct {
	method, status       string
	summary, location    string
	description, rrule   string
	start, end, duration *icsProperty
	organizer            *icsProperty
	attendees            []icsProperty
	isException          bool
}

// parseCalendar reads the first event of an iCalendar text (RFC 5545). An
// event that changes one date of a repeating series counts only when the
// calendar has no other event.
func parseCalendar(text string) (calendarEvent, bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// A line that starts with a space or a tab continues the line before.
	text = strings.NewReplacer("\n ", "", "\n\t", "").Replace(text)
	var method string
	var found []calendarEvent
	var current *calendarEvent
	var stack []string
	for _, line := range strings.Split(text, "\n") {
		p, ok := parseProperty(line)
		if !ok {
			continue
		}
		switch p.name {
		case "BEGIN":
			stack = append(stack, strings.ToUpper(p.value))
			if len(stack) == 2 && stack[0] == "VCALENDAR" && stack[1] == "VEVENT" {
				current = &calendarEvent{}
			}
			continue
		case "END":
			if len(stack) == 2 && current != nil {
				found = append(found, *current)
				current = nil
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if len(stack) == 1 && p.name == "METHOD" {
			method = strings.ToUpper(p.value)
		}
		// Alarms inside the event have their own DESCRIPTION, so only the
		// event's own lines count.
		if current == nil || len(stack) != 2 {
			continue
		}
		ev := current
		switch p.name {
		case "SUMMARY":
			ev.summary = unescapeText(p.value)
		case "LOCATION":
			ev.location = unescapeText(p.value)
		case "DESCRIPTION":
			ev.description = dropRulers(unescapeText(p.value))
		case "STATUS":
			ev.status = strings.ToUpper(p.value)
		case "RRULE":
			ev.rrule = strings.ToUpper(p.value)
		case "RECURRENCE-ID":
			ev.isException = true
		case "DTSTART":
			ev.start = &p
		case "DTEND":
			ev.end = &p
		case "DURATION":
			ev.duration = &p
		case "ORGANIZER":
			ev.organizer = &p
		case "ATTENDEE":
			ev.attendees = append(ev.attendees, p)
		}
	}
	if len(found) == 0 {
		return calendarEvent{}, false
	}
	ev := found[0]
	for _, f := range found {
		if !f.isException {
			ev = f
			break
		}
	}
	ev.method = method
	return ev, ev.summary != "" || ev.when() != ""
}

// parseProperty splits a content line into name, parameters and value.
// Quoted parameter values may hold ; and :, like CN="Weber, Dana".
func parseProperty(line string) (icsProperty, bool) {
	line = strings.TrimRight(line, "\r")
	quoted := false
	start := 0
	var parts []string
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '"':
			quoted = !quoted
		case c == ';' && !quoted:
			parts = append(parts, line[start:i])
			start = i + 1
		case c == ':' && !quoted:
			parts = append(parts, line[start:i])
			p := icsProperty{name: strings.ToUpper(strings.TrimSpace(parts[0])), params: map[string]string{}, value: line[i+1:]}
			for _, param := range parts[1:] {
				key, value, _ := strings.Cut(param, "=")
				p.params[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(value, `"`)
			}
			return p, p.name != ""
		}
	}
	return icsProperty{}, false
}

// unescapeText reads an iCalendar TEXT value, where \n is a line break and
// \, \; and \\ stand for the character itself.
func unescapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == 'n' || s[i] == 'N' {
				b.WriteByte('\n')
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return strings.TrimSpace(b.String())
}

// dropRulers removes lines made only of "-", ":" and "~". Google Calendar
// puts such lines around the part of the description it writes itself.
func dropRulers(s string) string {
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if len(t) < 10 || strings.Trim(t, "-:~") != "" {
			kept = append(kept, l)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// markdown writes the event like the headers of a forwarded message, then
// its description as plain text.
func (ev calendarEvent) markdown() string {
	heading := "Calendar event"
	switch {
	case ev.method == "CANCEL" || ev.status == "CANCELLED":
		heading = "Canceled event"
	case ev.method == "REQUEST":
		heading = "Invitation"
	case ev.method == "REPLY":
		heading = "Answer to an invitation"
	}
	var lines []string
	line := func(name, value string) {
		if value = oneLine(value); value != "" {
			lines = append(lines, "**"+name+":** "+escapeTitle(value))
		}
	}
	line("Title", ev.summary)
	line("When", ev.when())
	line("Repeats", repeats(ev.rrule))
	line("Location", ev.location)
	if ev.organizer != nil {
		line("Organizer", person(*ev.organizer))
	}
	var people []string
	for _, a := range ev.attendees {
		if name := person(a); name != "" {
			people = append(people, name)
		}
	}
	if len(people) > maxAttendees {
		people = append(people[:maxAttendees], fmt.Sprintf("and %d more", len(people)-maxAttendees))
	}
	line("Attendees", strings.Join(people, ", "))
	// Two spaces at the end of a line keep the lines apart in Markdown.
	out := "## " + heading + "\n\n" + strings.Join(lines, "  \n")
	if description := escapePlainText(mail.NormalizeBodyForRender(ev.description)); description != "" {
		out += "\n\n" + description
	}
	return out
}

// person formats an ORGANIZER or ATTENDEE line, like
// "Dana Weber <dana@example.com> (accepted)".
func person(p icsProperty) string {
	name, email := p.params["CN"], ""
	if len(p.value) > 7 && strings.EqualFold(p.value[:7], "mailto:") {
		email = p.value[7:]
	}
	switch {
	case strings.EqualFold(name, email):
		name = ""
	case name == "" && email == "":
		// A room or another resource can have an address like urn:uuid:...
		name = p.value
	}
	s := mail.FormatAddress(mail.Address{Name: name, Email: email})
	switch strings.ToUpper(p.params["PARTSTAT"]) {
	case "ACCEPTED":
		s += " (accepted)"
	case "DECLINED":
		s += " (declined)"
	case "TENTATIVE":
		s += " (maybe)"
	}
	return s
}

// icsTime is one DTSTART or DTEND value. zone is "UTC", the TZID as the
// sender wrote it, or "" for a time without a zone.
type icsTime struct {
	t      time.Time
	zone   string
	allDay bool
}

func parseICSTime(p *icsProperty) (icsTime, bool) {
	if p == nil {
		return icsTime{}, false
	}
	v := strings.TrimSpace(p.value)
	if len(v) == 8 {
		t, err := time.Parse("20060102", v)
		return icsTime{t: t, allDay: true}, err == nil
	}
	out := icsTime{zone: strings.TrimPrefix(p.params["TZID"], "/")}
	if strings.HasSuffix(v, "Z") {
		v, out.zone = strings.TrimSuffix(v, "Z"), "UTC"
	}
	t, err := time.Parse("20060102T150405", v)
	out.t = t
	return out, err == nil
}

// when describes the start and the end, like
// "Thu, 29 Oct 2026, 10:00 to 11:30 (W. Europe Standard Time)".
func (ev calendarEvent) when() string {
	start, ok := parseICSTime(ev.start)
	if !ok {
		return ""
	}
	end, hasEnd := parseICSTime(ev.end)
	if !hasEnd && ev.duration != nil {
		if d, ok := parseDuration(ev.duration.value); ok {
			end, hasEnd = icsTime{t: start.t.Add(d), zone: start.zone, allDay: start.allDay}, true
		}
	}
	const day = "Mon, 2 Jan 2006"
	if start.allDay {
		// The end of an all-day event is the day after its last day.
		if hasEnd && end.allDay && end.t.After(start.t.AddDate(0, 0, 1)) {
			return start.t.Format(day) + " to " + end.t.AddDate(0, 0, -1).Format(day) + " (all day)"
		}
		return start.t.Format(day) + " (all day)"
	}
	zone := func(t icsTime) string {
		if t.zone == "" {
			return ""
		}
		return " (" + t.zone + ")"
	}
	from := start.t.Format(day + ", 15:04")
	switch {
	case !hasEnd || end.allDay:
		return from + zone(start)
	case end.zone == start.zone && end.t.Format(day) == start.t.Format(day):
		return from + " to " + end.t.Format("15:04") + zone(start)
	case end.zone == start.zone:
		return from + " to " + end.t.Format(day+", 15:04") + zone(start)
	}
	return from + zone(start) + " to " + end.t.Format(day+", 15:04") + zone(end)
}

// parseDuration reads the common forms of an iCalendar DURATION, like PT1H30M
// or P1D.
func parseDuration(s string) (time.Duration, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "+")
	if !strings.HasPrefix(s, "P") {
		return 0, false
	}
	units := map[byte]time.Duration{'W': 7 * 24 * time.Hour, 'D': 24 * time.Hour, 'H': time.Hour, 'M': time.Minute, 'S': time.Second}
	var total time.Duration
	number := ""
	inTime := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			number += string(c)
		case c == 'T':
			inTime = true
		default:
			n, err := strconv.Atoi(number)
			unit, ok := units[c]
			// A number this large is no real event and would overflow.
			if err != nil || !ok || (c == 'M' && !inTime) || n > 100000 {
				return 0, false
			}
			total += time.Duration(n) * unit
			number = ""
		}
	}
	return total, number == "" && total > 0
}

// repeats names how often an event repeats, from the FREQ part of its RRULE.
func repeats(rrule string) string {
	for _, part := range strings.Split(rrule, ";") {
		if key, value, _ := strings.Cut(part, "="); key == "FREQ" {
			return map[string]string{"DAILY": "daily", "WEEKLY": "weekly", "MONTHLY": "monthly", "YEARLY": "yearly"}[value]
		}
	}
	return ""
}
