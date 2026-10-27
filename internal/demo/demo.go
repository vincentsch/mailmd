// Package demo holds the sample mailbox that "mailmd demo" writes. The mail
// is made up and uses example domains.
package demo

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/vincentsch/mailmd/internal/backend"
	"github.com/vincentsch/mailmd/internal/backend/mock"
	"github.com/vincentsch/mailmd/internal/backend/rfc822"
)

// Message is one sample message and its folder.
type Message struct {
	Folder string
	// Raw is the message in RFC 822 format, with CRLF line endings.
	Raw []byte
}

// Messages returns the sample mailbox.
func Messages() []Message {
	var out []Message
	for _, m := range messages() {
		out = append(out, Message{Folder: m.folder, Raw: []byte(strings.ReplaceAll(strings.TrimPrefix(m.raw, "\n"), "\n", "\r\n"))})
	}
	return out
}

type message struct {
	folder string
	raw    string
}

// Service returns an offline provider that serves the sample mailbox.
func Service() (*mock.Service, error) {
	s := &mock.Service{Items: map[string][]mock.Item{}}
	seen := map[string]bool{}
	for _, m := range Messages() {
		msg, err := rfc822.Normalize(m.Raw)
		if err != nil {
			return nil, fmt.Errorf("demo message in %s: %w", m.Folder, err)
		}
		m := message{folder: m.Folder, raw: string(m.Raw)}
		key := "demo:" + m.folder
		if !seen[key] {
			seen[key] = true
			role := backend.RoleNone
			switch m.folder {
			case "INBOX":
				role = backend.RoleInbox
			case "Sent":
				role = backend.RoleSent
			case "Spam":
				role = backend.RoleJunk
			}
			s.FolderRecords = append(s.FolderRecords, backend.Folder{Path: m.folder, Fetchable: true, ScopeKey: key, Role: role})
		}
		s.Items[key] = append(s.Items[key], mock.Item{Message: &msg, RawBytes: int64(len(m.raw))})
	}
	return s, nil
}

func messages() []message {
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[]/Count 0>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n"))
	return []message{
		{"INBOX", `
Message-ID: <lunch-1@example.org>
Date: Tue, 14 Oct 2026 09:12:00 +0200
From: Alice Martin <alice@example.org>
To: Dana Reyes <dana@example.com>
Subject: Lunch on Thursday?
Content-Type: text/plain; charset=utf-8

Hi Dana,

are you free for lunch on Thursday at 12:30? The usual place near the
station.

Alice
`},
		{"INBOX", `
Message-ID: <q4-plan@acme.example>
Date: Wed, 15 Oct 2026 16:40:00 -0400
From: Bob Chen <bob@acme.example>
To: Dana Reyes <dana@example.com>
Subject: Q4 roadmap draft
Content-Type: text/plain; charset=utf-8

Dana,

here is the first version of the Q4 roadmap. The two open points are the
export feature and the pricing page. Can you look at both before Friday?

Thanks,
Bob
`},
		{"Sent", `
Message-ID: <q4-plan-re1@example.com>
In-Reply-To: <q4-plan@acme.example>
References: <q4-plan@acme.example>
Date: Thu, 16 Oct 2026 08:05:00 +0200
From: Dana Reyes <dana@example.com>
To: Bob Chen <bob@acme.example>
Subject: Re: Q4 roadmap draft
Content-Type: text/plain; charset=utf-8

Hi Bob,

the export feature looks good. For the pricing page I would keep the
annual plan and drop the weekly one.

Dana

On Wed, Oct 15, 2026 at 16:40, Bob Chen <bob@acme.example> wrote:
> here is the first version of the Q4 roadmap.
`},
		{"INBOX", `
Message-ID: <q4-plan-re2@acme.example>
In-Reply-To: <q4-plan-re1@example.com>
References: <q4-plan@acme.example> <q4-plan-re1@example.com>
Date: Thu, 16 Oct 2026 10:30:00 -0400
From: Bob Chen <bob@acme.example>
To: Dana Reyes <dana@example.com>
Subject: Re: Q4 roadmap draft
Content-Type: text/plain; charset=utf-8

Agreed, the weekly plan goes. I will update the page today.

Bob
`},
		{"Work/Clients", `
Message-ID: <invoice-114@studio.example>
Date: Tue, 21 Oct 2026 11:00:00 +0100
From: Northwind Studio <billing@studio.example>
To: dana@example.com
Subject: Invoice 2026-114 for October
Content-Type: multipart/mixed; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8

Hello Dana,

attached is invoice 2026-114 for the design work in October, and the
timesheet with the hours.

Kind regards,
Northwind Studio

--b1
Content-Type: application/pdf; name="invoice-2026-114.pdf"
Content-Disposition: attachment; filename="invoice-2026-114.pdf"
Content-Transfer-Encoding: base64

` + pdf + `
--b1
Content-Type: text/csv; name="timesheet-october.csv"
Content-Disposition: attachment; filename="timesheet-october.csv"

date,task,hours
2026-10-02,Logo variants,3.5
2026-10-09,Landing page,6
2026-10-16,Review call,1
--b1--
`},
		{"Newsletters", `
Message-ID: <digest-212@news.example.net>
Date: Fri, 17 Oct 2026 07:00:00 +0000
From: The Go Digest <digest@news.example.net>
To: dana@example.com
Subject: The Go Digest #212
Content-Type: text/html; charset=utf-8

<html><body>
<div style="display:none">Your weekly Go news, in five minutes.</div>
<h1>The Go Digest</h1>
<p>Hello <b>Dana</b>, here is what happened in <a href="https://go.dev/blog">Go</a> this week.</p>
<h2>Top stories</h2>
<ul>
<li><a href="https://example.com/a">Go 1.27 released</a> with iterator improvements</li>
<li><a href="https://example.com/b">Profiling in production</a></li>
</ul>
<table>
<tr><th>Package</th><th>Stars</th></tr>
<tr><td>cobra</td><td>38k</td></tr>
<tr><td>bubbletea</td><td>27k</td></tr>
</table>
<p><img src="https://track.example.net/open.gif" width="1" height="1" alt=""></p>
<p style="font-size:10px">You get this because you subscribed. <a href="https://example.com/unsub">Unsubscribe</a></p>
</body></html>
`},
		{"INBOX", `
Message-ID: <berlin-1@example.de>
Date: Mon, 20 Oct 2026 18:20:00 +0200
From: =?UTF-8?Q?J=C3=B6rg_M=C3=BCller?= <joerg@example.de>
To: Dana Reyes <dana@example.com>
Subject: =?UTF-8?Q?Gr=C3=BC=C3=9Fe_aus_M=C3=BCnchen_und_die_Schl=C3=BCssel?=
Content-Type: text/plain; charset=utf-8

Hallo Dana,

die Schlüssel für die Wohnung liegen beim Nachbarn im dritten Stock.
Viele Grüße aus München!

Jörg
`},
		{"INBOX", `
Message-ID: <invite-77@calendar.example.com>
Date: Wed, 22 Oct 2026 14:00:00 +0200
From: Alice Martin <alice@example.org>
To: Dana Reyes <dana@example.com>
Subject: Invitation: Design review on Oct 28
Content-Type: multipart/mixed; boundary="c1"

--c1
Content-Type: text/plain; charset=utf-8

You are invited to the design review on Tuesday, October 28, 10:00 to
11:00, in room 3.

--c1
Content-Type: text/calendar; charset=utf-8; method=REQUEST

BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Example//Calendar//EN
METHOD:REQUEST
BEGIN:VEVENT
UID:invite-77@calendar.example.com
DTSTART:20261028T080000Z
DTEND:20261028T090000Z
SUMMARY:Design review
LOCATION:Room 3
END:VEVENT
END:VCALENDAR
--c1--
`},
		{"Spam", `
Message-ID: <prize-1@spam.example>
Date: Thu, 23 Oct 2026 03:00:00 +0000
From: Prize Center <winner@spam.example>
To: dana@example.com
Subject: You have won!
Content-Type: text/plain; charset=utf-8

Click here to claim your prize.
`},
	}
}
