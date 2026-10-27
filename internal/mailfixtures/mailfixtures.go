package mailfixtures

import (
	"time"

	"github.com/vincentsch/mailmd/internal/mail"
)

// Messages returns the sample messages. Each call builds them anew, so a
// caller can change them without changing what the next call returns.
// LocalID is empty in each message, so the tests run the real code that
// computes it.
func Messages() []mail.Message {
	return []mail.Message{
		plainSeed(),
		htmlOnlySeed(),
		htmlUnsafeSeed(),
		attachmentsSeed(),
		inlineImageSeed(),
		multiFolderSeed(),
		sparseSeed(),
		hostileSeed(),
		unsafeNamesSeed(),
		twinASeed(),
		twinBSeed(),
	}
}

func plainSeed() mail.Message {
	return mail.Message{
		MessageID:  "<plain-001@example.com>",
		InReplyTo:  "<thread-root@example.com>",
		References: []string{" <ref-b@example.com> ", "", "<ref-a@example.com>", "<ref-a@example.com>"},
		ThreadID:   "thread-quarterly",
		From:       mail.Address{Name: "Alice Lead", Email: "alice@example.com"},
		To: []mail.Address{
			{Name: "Bob Reviewer", Email: "bob@example.com"},
			{Email: "team@example.com"},
		},
		Cc:       []mail.Address{{Name: "Carol Ops", Email: "carol@example.com"}},
		ReplyTo:  []mail.Address{{Name: "Support", Email: "support@example.com"}},
		Subject:  "Quarterly Plan",
		Date:     time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC),
		Folders:  []string{"Inbox"},
		Flags:    mail.Flags{Seen: true, Flagged: true},
		BodyText: "Line one\r\nLine two\n\n",
		Attachments: []mail.Attachment{
			{Filename: "report.pdf", MIMEType: "application/pdf", Size: 3, Data: []byte("PDF")},
			{Filename: "remote.dat", MIMEType: "application/octet-stream", Size: 7},
		},
		RawHeaders: map[string][]string{
			"X-Trace": {"trace-a", "trace-b"},
		},
	}
}

func htmlOnlySeed() mail.Message {
	return mail.Message{
		MessageID: "<html-001@example.com>",
		From:      mail.Address{Name: "Release Bot", Email: "release@example.com"},
		To:        []mail.Address{{Email: "dev@example.com"}},
		Subject:   "Release Notes",
		Date:      time.Date(2026, 4, 2, 16, 0, 0, 0, time.UTC),
		Folders:   []string{"Inbox"},
		BodyHTML:  `<h1>Release 1.2</h1><p>Hello <a href="https://example.com/release">release notes</a>.</p><ul><li>One fix</li><li>One feature</li></ul><blockquote>Ship it.</blockquote>`,
	}
}

func htmlUnsafeSeed() mail.Message {
	return mail.Message{
		MessageID: "<unsafe-html-001@example.com>",
		From:      mail.Address{Name: "Offers", Email: "offers@example.com"},
		To:        []mail.Address{{Email: "reader@example.com"}},
		Subject:   "Special Offer",
		Date:      time.Date(2026, 4, 5, 8, 15, 0, 0, time.UTC),
		Folders:   []string{"Newsletters"},
		BodyHTML:  `<h2>Deal</h2><style>p{display:none}</style><script>alert(1)</script><p><a href="javascript:alert(1)">bad</a> <a href="https://example.com/deal">safe link</a><img src="https://tracker.example/p.gif" width="1" height="1" alt="pixel"></p>`,
	}
}

func attachmentsSeed() mail.Message {
	return mail.Message{
		MessageID: "<attachments-001@example.com>",
		From:      mail.Address{Name: "Finance", Email: "finance@example.com"},
		To:        []mail.Address{{Email: "ap@example.com"}},
		Subject:   "Invoice and Notes",
		Date:      time.Date(2026, 4, 9, 11, 45, 0, 0, time.UTC),
		Folders:   []string{"Inbox"},
		BodyText:  "Invoice attached.\nNotes follow.",
		Attachments: []mail.Attachment{
			{Filename: "report.pdf", MIMEType: "application/pdf", Size: 3, Data: []byte("one")},
			{Filename: "report.pdf", MIMEType: "application/pdf", Size: 3},
			{Filename: "notes.txt", MIMEType: "text/plain", Size: 5, Data: []byte("notes")},
		},
	}
}

func inlineImageSeed() mail.Message {
	return mail.Message{
		MessageID: "<inline-001@example.com>",
		From:      mail.Address{Name: "Design", Email: "design@example.com"},
		To:        []mail.Address{{Email: "brand@example.com"}},
		Subject:   "Inline Logo",
		Date:      time.Date(2026, 4, 12, 13, 0, 0, 0, time.UTC),
		Folders:   []string{"Inbox"},
		BodyHTML:  `<p>Logo follows <img src="cid:logo" alt="Company logo"></p>`,
		Attachments: []mail.Attachment{
			{Filename: "logo.png", MIMEType: "image/png", Size: 7, Inline: true, ContentID: "<logo@cid>", Data: []byte("PNGDATA")},
		},
	}
}

func multiFolderSeed() mail.Message {
	return mail.Message{
		MessageID: "<multi-folder-001@example.com>",
		From:      mail.Address{Name: "PM", Email: "pm@example.com"},
		To:        []mail.Address{{Email: "project@example.com"}},
		Subject:   "Project Kickoff",
		Date:      time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC),
		Folders:   []string{"Work/Projects", "Archive", "Work/Projects"},
		BodyText:  "Kickoff agenda.",
		Attachments: []mail.Attachment{
			{Filename: "agenda.txt", MIMEType: "text/plain", Size: 6, Data: []byte("agenda")},
		},
	}
}

func sparseSeed() mail.Message {
	return mail.Message{
		From:     mail.Address{Email: "unknown@example.com"},
		Subject:  "",
		BodyText: "Sparse body.",
	}
}

func hostileSeed() mail.Message {
	return mail.Message{
		MessageID: "<hostile-001@example.com>",
		From:      mail.Address{Name: "> Boss:\n\"Sign\"", Email: "boss@example.com"},
		To:        []mail.Address{{Email: "team@example.com"}},
		Subject:   "> \"Quarter\": end\nLine",
		Date:      time.Date(2026, 4, 18, 7, 30, 0, 0, time.UTC),
		Folders:   []string{"Inbox"},
		BodyText:  "Confirm: yes\n> quoted input",
	}
}

func unsafeNamesSeed() mail.Message {
	return mail.Message{
		MessageID: "<unsafe-names-001@example.com>",
		From:      mail.Address{Name: "Path Tests", Email: "paths@example.com"},
		To:        []mail.Address{{Email: "archive@example.com"}},
		Subject:   "Reserved Names",
		Date:      time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
		Folders:   []string{".mailmd", ".git", "..", "CON"},
		BodyText:  "Path tokens should be safe on disk.",
		Attachments: []mail.Attachment{
			{Filename: "CON.txt.bak", MIMEType: "text/plain", Size: 1, Data: []byte("a")},
			{Filename: "NUL.tar.gz", MIMEType: "application/gzip", Size: 1, Data: []byte("b")},
			{Filename: "trailing.  ", MIMEType: "text/plain", Size: 1, Data: []byte("c")},
			{Filename: ".mailmd", MIMEType: "text/plain", Size: 1, Data: []byte("d")},
		},
	}
}

func twinASeed() mail.Message {
	return mail.Message{
		MessageID: "<twin-a@example.com>",
		From:      mail.Address{Name: "Alice", Email: "alice@example.com"},
		To:        []mail.Address{{Email: "team@example.com"}},
		Subject:   "Status Update",
		Date:      time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		Folders:   []string{"Inbox"},
		BodyText:  "A",
	}
}

func twinBSeed() mail.Message {
	return mail.Message{
		MessageID: "<twin-b@example.com>",
		From:      mail.Address{Name: "Bob", Email: "bob@example.com"},
		To:        []mail.Address{{Email: "ops@example.com"}},
		Subject:   "Status Update",
		Date:      time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		Folders:   []string{"Inbox"},
		BodyText:  "B",
	}
}
