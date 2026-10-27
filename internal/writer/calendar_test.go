package writer

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/vincentsch/mailmd/internal/backend/rfc822"
)

// outlookInvite is an Exchange invite without a text part. The times use
// a Windows time zone name, and the event has a reminder with its own
// DESCRIPTION.
const outlookInvite = `BEGIN:VCALENDAR
METHOD:REQUEST
PRODID:Microsoft Exchange Server 2010
VERSION:2.0
BEGIN:VTIMEZONE
TZID:W. Europe Standard Time
BEGIN:STANDARD
DTSTART:16010101T030000
TZOFFSETFROM:+0200
TZOFFSETTO:+0100
RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=-1SU;BYMONTH=10
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
ORGANIZER;CN="Weber, Dana":mailto:dana.weber@contoso.example
ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE;CN=Max Muster:mailto:max@example.com
ATTENDEE;ROLE=OPT-PARTICIPANT;PARTSTAT=TENTATIVE;RSVP=TRUE;CN=Lea Roth:MAILTO:lea.roth@contoso.example
DESCRIPTION;LANGUAGE=de-DE:Hallo zusammen\,\n\nAgenda:\n1. Zahlen Q3\n2. Pl
 anung Q4\n\nBitte bringt eure Folien mit <b>und</b> Ideen.\n
UID:040000008200E00074C5B7101A82E00800000000
SUMMARY;LANGUAGE=de-DE:Quartalsplanung
DTSTART;TZID=W. Europe Standard Time:20261105T100000
DTEND;TZID=W. Europe Standard Time:20261105T113000
STATUS:CONFIRMED
LOCATION;LANGUAGE=de-DE:Seeblick\, Raum 2
BEGIN:VALARM
DESCRIPTION:REMINDER
TRIGGER;RELATED=START:-PT15M
ACTION:DISPLAY
END:VALARM
END:VEVENT
END:VCALENDAR
`

// googleInvite has times in UTC, folded parameter lines, an attendee whose
// name is the address, a weekly rule and Google's ruler lines.
const googleInvite = `BEGIN:VCALENDAR
PRODID:-//Google Inc//Google Calendar 70.9054//EN
VERSION:2.0
CALSCALE:GREGORIAN
METHOD:REQUEST
BEGIN:VEVENT
DTSTART:20261029T090000Z
DTEND:20261029T093000Z
ORGANIZER;CN=Sam Lee:mailto:sam.lee@gmail.example
UID:7kq1v2r3s4t5u6v7w8x9y0@google.com
ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED;RSVP=TRUE
 ;CN=Sam Lee;X-NUM-GUESTS=0:mailto:sam.lee@gmail.example
ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=
 TRUE;CN=max@example.com;X-NUM-GUESTS=0:mailto:max@example.com
DESCRIPTION:Short sync about the launch.\n\n-::~:~::~:~:~:~:~:~:~:~:~:~:~:~
 :~:~:~:~::~:~::-\nJoin with Google Meet: https://meet.google.com/abc-defg-h
 ij\n-::~:~::~:~:~:~:~:~:~:~:~:~:~:~:~:~:~:~::~:~::-
LOCATION:
STATUS:CONFIRMED
SUMMARY:Launch sync
RRULE:FREQ=WEEKLY;BYDAY=TH
END:VEVENT
END:VCALENDAR
`

func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// writeRaw parses a raw message and writes it, and returns its Markdown.
func writeRaw(t *testing.T, raw string) string {
	t.Helper()
	msg, err := rfc822.Normalize([]byte(crlf(raw)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := WriteMessage(context.Background(), Options{Root: t.TempDir(), Account: "work"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	// The event lines end in two spaces, a Markdown line break. The tests
	// compare without them.
	return strings.ReplaceAll(string(data), "  \n", "\n")
}

func TestOutlookInviteWithoutTextShowsTheEvent(t *testing.T) {
	md := writeRaw(t, `Message-ID: <AM0PR01MB1234@contoso.example>
Date: Mon, 26 Oct 2026 09:12:44 +0000
From: "Weber, Dana" <dana.weber@contoso.example>
To: Max Muster <max@example.com>
Subject: Quartalsplanung
Content-Type: text/calendar; charset="utf-8"; method=REQUEST
Content-Transfer-Encoding: base64

`+base64.StdEncoding.EncodeToString([]byte(crlf(outlookInvite)))+`
`)
	want := `# Quartalsplanung

## Invitation

**Title:** Quartalsplanung
**When:** Thu, 5 Nov 2026, 10:00 to 11:30 (W. Europe Standard Time)
**Location:** Seeblick, Raum 2
**Organizer:** Weber, Dana <dana.weber@contoso.example>
**Attendees:** Max Muster <max@example.com>, Lea Roth <lea.roth@contoso.example> (maybe)

Hallo zusammen,

Agenda:
1. Zahlen Q3
2. Planung Q4

Bitte bringt eure Folien mit \<b>und\</b> Ideen.

## Attachments

- [invite.ics](`
	if !strings.Contains(md, want) {
		t.Fatalf("file:\n%s\nwant it to contain:\n%s", md, want)
	}
	if strings.Contains(md, "REMINDER") {
		t.Fatalf("the reminder's text was shown:\n%s", md)
	}
}

func TestGoogleInviteWithoutTextShowsTheEvent(t *testing.T) {
	md := writeRaw(t, `Message-ID: <google-invite@example.test>
Date: Mon, 26 Oct 2026 10:15:00 +0000
From: Sam Lee <sam.lee@gmail.example>
To: max@example.com
Subject: Invitation: Launch sync @ Weekly from 10am to 10:30am on Thursday
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/calendar; charset="UTF-8"; method=REQUEST

`+googleInvite+`
--b
Content-Type: application/ics; name="invite.ics"
Content-Disposition: attachment; filename="invite.ics"

`+googleInvite+`
--b--
`)
	want := `## Invitation

**Title:** Launch sync
**When:** Thu, 29 Oct 2026, 09:00 to 09:30 (UTC)
**Repeats:** weekly
**Organizer:** Sam Lee <sam.lee@gmail.example>
**Attendees:** Sam Lee <sam.lee@gmail.example> (accepted), max@example.com

Short sync about the launch.

Join with Google Meet: https://meet.google.com/abc-defg-hij

## Attachments
`
	if !strings.Contains(md, want) || strings.Count(md, "invite.ics") < 2 {
		t.Fatalf("file:\n%s\nwant it to contain:\n%s", md, want)
	}
}

func TestInviteWithTextKeepsItsText(t *testing.T) {
	md := writeRaw(t, `Message-ID: <google-text@example.test>
From: Sam Lee <sam.lee@gmail.example>
Subject: Invitation: Launch sync
Content-Type: multipart/alternative; boundary="b"

--b
Content-Type: text/plain

You have been invited to Launch sync.
--b
Content-Type: text/calendar; method=REQUEST

`+googleInvite+`
--b--
`)
	if !strings.Contains(md, "You have been invited") || strings.Contains(md, "## Invitation") {
		t.Fatalf("file:\n%s", md)
	}
}

func TestCalendarTimes(t *testing.T) {
	for _, tc := range []struct{ lines, want string }{
		{"DTSTART;VALUE=DATE:20261224\nDTEND;VALUE=DATE:20261225", "Thu, 24 Dec 2026 (all day)"},
		{"DTSTART;VALUE=DATE:20261224\nDTEND;VALUE=DATE:20261227", "Thu, 24 Dec 2026 to Sat, 26 Dec 2026 (all day)"},
		{"DTSTART;TZID=Europe/Berlin:20261029T100000\nDURATION:PT1H30M", "Thu, 29 Oct 2026, 10:00 to 11:30 (Europe/Berlin)"},
		{"DTSTART;TZID=\"/Europe/Berlin\":20261029T230000\nDTEND;TZID=Europe/Berlin:20261030T010000", "Thu, 29 Oct 2026, 23:00 to Fri, 30 Oct 2026, 01:00 (Europe/Berlin)"},
		{"DTSTART:20261029T100000\nDTEND:20261029T110000Z", "Thu, 29 Oct 2026, 10:00 to Thu, 29 Oct 2026, 11:00 (UTC)"},
		{"DTSTART:20261029T100000", "Thu, 29 Oct 2026, 10:00"},
		{"DTSTART:20261029T100000\nDURATION:PT99999999999H", "Thu, 29 Oct 2026, 10:00"},
	} {
		ev, ok := parseCalendar("BEGIN:VCALENDAR\nBEGIN:VEVENT\n" + tc.lines + "\nEND:VEVENT\nEND:VCALENDAR\n")
		if !ok || ev.when() != tc.want {
			t.Errorf("%q: when = %q, want %q", tc.lines, ev.when(), tc.want)
		}
	}
}

func TestCalendarPicksTheSeriesAndShortensLongGuestLists(t *testing.T) {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\nMETHOD:CANCEL\nBEGIN:VEVENT\nRECURRENCE-ID:20261105T100000Z\nSUMMARY:One date moved\nEND:VEVENT\n")
	b.WriteString("BEGIN:VEVENT\nSUMMARY:Team call\nDTSTART:20261029T100000Z\n")
	b.WriteString("ATTENDEE;CUTYPE=ROOM;CN=Room Seeblick:urn:uuid:8f1c\n")
	for i := 0; i < 12; i++ {
		b.WriteString("ATTENDEE:mailto:person" + string(rune('a'+i)) + "@example.com\n")
	}
	b.WriteString("END:VEVENT\nEND:VCALENDAR\n")
	ev, ok := parseCalendar(b.String())
	if !ok {
		t.Fatal("no event")
	}
	md := ev.markdown()
	if !strings.HasPrefix(md, "## Canceled event\n\n**Title:** Team call") || !strings.Contains(md, "**Attendees:** Room Seeblick, persona@example.com") || !strings.Contains(md, "personi@example.com, and 3 more") {
		t.Fatalf("markdown:\n%s", md)
	}
	if _, ok := parseCalendar("BEGIN:VCALENDAR\nBEGIN:VTODO\nSUMMARY:x\nEND:VTODO\nEND:VCALENDAR"); ok {
		t.Fatal("a calendar without an event gave an event")
	}
}
