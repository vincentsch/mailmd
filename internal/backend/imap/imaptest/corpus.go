package imaptest

import (
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// ExpectedMessage is what a test expects for one seeded message. The values
// are written by hand, so a test doesn't check the parser against itself.
type ExpectedMessage struct {
	UID       uint32
	LocalID   string
	Subject   string
	MessageID string
	Flags     []string
}

func inboxMessages() []MessageSeed {
	return []MessageSeed{
		seedMessage(1, nil, plainMessage, "Plain hello", "<plain-1@example.test>"),
		seedMessage(2, []string{string(imap.FlagSeen), string(imap.FlagFlagged)}, htmlOnlyMessage, "HTML only", "<html-2@example.test>"),
		seedMessage(4, nil, alternativeMessage, "Alternative ✓", "<alt-4@example.test>"),
		seedMessage(5, nil, nestedMixedMessage, "Nested mixed", "<mixed-5@example.test>"),
		seedMessage(6, nil, relatedMessage, "Related inline", "<related-6@example.test>"),
		seedMessage(7, nil, signedMessage, "Signed content", "<signed-7@example.test>"),
		seedMessage(8, nil, latin1Message, "café", "<latin1-8@example.test>"),
		seedMessage(9, nil, addressEdgeMessage, "Address edges", "<address-9@example.test>"),
		seedMessage(10, []string{string(imap.FlagAnswered), string(imap.FlagDraft)}, senderOnlyMessage, "Sender only", ""),
		seedMessage(11, nil, multiAttachmentMessage, "Attachment set", "<attachments-11@example.test>"),
		seedMessage(12, nil, missingDateMessage, "Missing date", "<missing-date-12@example.test>"),
		seedMessage(13, nil, malformedBodyMessage, "Malformed body", "<malformed-body-13@example.test>"),
		seedMessage(20, nil, identitylessMessage, "", ""),
		seedMessage(21, nil, malformedHeaderMessage, "", ""),
		seedMessage(22, nil, afterBadMessage, "After bad messages", "<after-bad-22@example.test>"),
	}
}

func workMessages() []MessageSeed {
	return []MessageSeed{
		seedMessage(1, nil, workOneMessage, "Work one", "<work-1@example.test>"),
		seedMessage(3, nil, workTwoMessage, "Work two", "<work-3@example.test>"),
		seedMessage(5, nil, workThreeMessage, "Work three", "<work-5@example.test>"),
	}
}

func projectMessages() []MessageSeed {
	return []MessageSeed{
		seedMessage(1, nil, projectMessage, "Project update", "<project-1@example.test>"),
	}
}

func reportMessages() []MessageSeed {
	return []MessageSeed{
		seedMessage(1, nil, reportMessage, "Отчёт", "<report-1@example.test>"),
	}
}

func allMailMessages() []MessageSeed {
	return []MessageSeed{
		seedMessage(1, nil, plainMessage, "Plain hello", "<plain-1@example.test>"),
		seedMessage(2, nil, htmlOnlyMessage, "HTML only", "<html-2@example.test>"),
	}
}

func collisionMessages(seen bool) []MessageSeed {
	flags := []string(nil)
	if seen {
		flags = []string{string(imap.FlagSeen)}
	}
	return []MessageSeed{seedMessage(1, flags, collisionMessage, "Collision shared identity", "<collision@example.test>")}
}

func flatMessages() []MessageSeed {
	return []MessageSeed{
		seedMessage(1, nil, flatMessage, "Flat mailbox", "<flat-1@example.test>"),
	}
}

func seedMessage(uid uint32, flags []string, raw, subject, messageID string) MessageSeed {
	return MessageSeed{
		UID:          uid,
		Flags:        append([]string(nil), flags...),
		InternalDate: time.Date(2026, 7, 3, 10, 0, int(uid%60), 0, time.UTC),
		Raw:          wire(raw),
		Expected: ExpectedMessage{
			UID:       uid,
			LocalID:   expectedLocalID(subject, messageID),
			Subject:   subject,
			MessageID: messageID,
			Flags:     append([]string(nil), flags...),
		},
	}
}

// expectedLocalID returns the local IDs written down by hand. A test that
// computed them with the parser would only check the parser against itself.
func expectedLocalID(subject, messageID string) string {
	switch messageID {
	case "<plain-1@example.test>":
		return "mailmd-localid-v1-18ffea08b059ddb5b610e45ff01066b9a23dbb851a123c3277769c1867ae9bfb"
	case "<html-2@example.test>":
		return "mailmd-localid-v1-5a3dfc52ecf8fb6713f5fcdd6479054a633af8ed8f0bb8ff9a80b7e841569ddb"
	case "<alt-4@example.test>":
		return "mailmd-localid-v1-fc300aa43c09865df3e3f37765c863c06c2b21ace94354988fb9f41440b8c0b9"
	case "<mixed-5@example.test>":
		return "mailmd-localid-v1-42b49d179ed36edf17cacd6d3c4c0ae7624a21e13fbc773fa62fe2fb906bd3d9"
	case "<related-6@example.test>":
		return "mailmd-localid-v1-09a6547c0aa3e803b296e8236c35f1b3b5256361a6ff36f3e4b0bc92eb12c0ee"
	case "<signed-7@example.test>":
		return "mailmd-localid-v1-813c02aa9f2eeae9ce4153f5bcfbc4046731713076a2bde477a59b2b035b344b"
	case "<latin1-8@example.test>":
		return "mailmd-localid-v1-66b0dfbe0a7d1d742a6ea1d5ca61987e48f20bc2050e28084c3811be94a81906"
	case "<address-9@example.test>":
		return "mailmd-localid-v1-eb6feff241a29afd896ac7498b39de440cd943c454867dd388e0267b178f7a63"
	case "<attachments-11@example.test>":
		return "mailmd-localid-v1-d60940790bee52ddfe562cd5a4d33093a20cbd27c6572044a0b1aacb268f865e"
	case "<missing-date-12@example.test>":
		return "mailmd-localid-v1-ceb239e67d3043048587e50e516a7f42d034442fcc45586ecc409c1ea756f6dc"
	case "<after-bad-22@example.test>":
		return "mailmd-localid-v1-2d01f7ecfe64268f515ec5e511fd0c0c1dfc7d95994419322332408f5d716b78"
	case "<work-1@example.test>":
		return "mailmd-localid-v1-7b7d53d652ca9f761a796b58a49251e8fde57b724861e37432bf0c105ef78e7a"
	case "<work-3@example.test>":
		return "mailmd-localid-v1-edef77770f3f834a8394938b995c3fbc9740d8272a2823d0aa8f7cc0ff7e7416"
	case "<work-5@example.test>":
		return "mailmd-localid-v1-9fd2d1297244526391bce93bf5e58939b732fb4553fd7053ff8ba66ee12a426a"
	case "<project-1@example.test>":
		return "mailmd-localid-v1-99fe92934b1048cfcb2e27e017c2489b6c2da528cbce4ae0a2ed9b8f9d684a5c"
	case "<report-1@example.test>":
		return "mailmd-localid-v1-b62be1d3b0e0394cda5c27ecc327dd9e7a204bfad494f9b8dc659ddc7f64cd1c"
	case "<collision@example.test>":
		return "mailmd-localid-v1-940f0b52194d06b56d94313af71800af66e5464092aabaf47352be832c81b26a"
	case "<flat-1@example.test>":
		return "mailmd-localid-v1-09ba44dd908cb6cb80546ffb585217d62dcc8428e54dc2a6b9be6d6440d34321"
	}
	if subject == "Sender only" && messageID == "" {
		return "mailmd-localid-v1-5699e7a1c3aa71eb5eddf55614c58edb42dd34c3a32f7e4534cc459cd3aa586f"
	}
	return ""
}

// wire turns the line endings of a fixture into CRLF, as a real IMAP server
// sends them.
func wire(raw string) []byte {
	raw = strings.TrimPrefix(raw, "\n")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\n", "\r\n")
	return []byte(raw)
}

const plainMessage = `Message-ID: <plain-1@example.test>
In-Reply-To: <parent@example.test>
References: <root@example.test> <parent@example.test>
Date: Fri, 03 Jul 2026 08:00:00 +0000
From: Alice Plain <alice@example.test>
To: Bob <bob@example.test>
Subject: Plain hello
X-Duplicate: first
X-Duplicate: second
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: 7bit

Hello from plain text.
`

const htmlOnlyMessage = `Message-ID: <html-2@example.test>
Date: Fri, 03 Jul 2026 08:02:00 +0000
From: Web Sender <web@example.test>
To: Bob <bob@example.test>
Subject: HTML only
Content-Type: text/html; charset=utf-8
Content-Transfer-Encoding: quoted-printable

<p>Hello <strong>HTML</strong> only.</p>
`

const alternativeMessage = `Message-ID: <alt-4@example.test>
Date: Fri, 03 Jul 2026 08:04:00 +0000
From: =?UTF-8?Q?Zo=C3=AB_Alt?= <zoe@example.test>
To: Bob <bob@example.test>
Subject: =?UTF-8?Q?Alternative_=E2=9C=93?=
Content-Type: multipart/alternative; boundary="alt-boundary"

--alt-boundary
Content-Type: text/plain; charset=utf-8

Plain alternative.
--alt-boundary
Content-Type: text/html; charset=utf-8

<p>Rich alternative.</p>
--alt-boundary--
`

const nestedMixedMessage = `Message-ID: <mixed-5@example.test>
Date: Fri, 03 Jul 2026 08:05:00 +0000
From: Mixed Sender <mixed@example.test>
To: Bob <bob@example.test>
Subject: Nested mixed
Content-Type: multipart/mixed; boundary="mixed-boundary"

--mixed-boundary
Content-Type: multipart/alternative; boundary="inner-alt"

--inner-alt
Content-Type: text/plain; charset=utf-8

Nested plain body.
--inner-alt
Content-Type: text/html; charset=utf-8

<p>Nested <em>HTML</em> body.</p>
--inner-alt--
--mixed-boundary
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="notes.txt"
Content-Transfer-Encoding: base64

bm90ZXMgZmlsZQo=
--mixed-boundary--
`

const relatedMessage = `Message-ID: <related-6@example.test>
Date: Fri, 03 Jul 2026 08:06:00 +0000
From: Related Sender <related@example.test>
To: Bob <bob@example.test>
Subject: Related inline
Content-Type: multipart/related; boundary="related-boundary"

--related-boundary
Content-Type: text/html; charset=utf-8

<p>Inline image <img src="cid:image-1@example.test"></p>
--related-boundary
Content-Type: image/png
Content-Disposition: inline; filename="inline.png"
Content-ID: <image-1@example.test>
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--related-boundary--
`

const signedMessage = `Message-ID: <signed-7@example.test>
Date: Fri, 03 Jul 2026 08:07:00 +0000
From: Signed Sender <signed@example.test>
To: Bob <bob@example.test>
Subject: Signed content
Content-Type: multipart/signed; protocol="application/pkcs7-signature"; boundary="signed-boundary"

--signed-boundary
Content-Type: text/plain; charset=utf-8

Signed body.
--signed-boundary
Content-Type: application/pkcs7-signature; name="smime.p7s"
Content-Disposition: attachment; filename="smime.p7s"
Content-Transfer-Encoding: base64

c2lnbmF0dXJl
--signed-boundary--
`

const latin1Message = "Message-ID: <latin1-8@example.test>\n" +
	"Date: Fri, 03 Jul 2026 08:08:00 +0000\n" +
	"From: =?ISO-8859-1?Q?Andr=E9?= <andre@example.test>\n" +
	"To: Bob <bob@example.test>\n" +
	"Subject: =?ISO-8859-1?Q?caf=E9?=\n" +
	"Content-Type: text/plain; charset=iso-8859-1\n" +
	"Content-Transfer-Encoding: 8bit\n" +
	"\n" +
	"caf\xe9 body\n"

const addressEdgeMessage = `Message-ID: <address-9@example.test>
Date: Fri, 03 Jul 2026 08:09:00 +0000
From: First Sender <first@example.test>, Second Sender <second@example.test>
To: Good <good@example.test>, malformed recipient
Cc: Carbon <cc@example.test>
Bcc: Blind <bcc@example.test>
Reply-To: Replies <reply@example.test>
Subject: Address edges
Content-Type: text/plain; charset=utf-8

Address parser edge cases.
`

const senderOnlyMessage = `Sender: Sender Only <sender@example.test>
Date: Fri, 03 Jul 2026 08:10:00 +0000
To: Bob <bob@example.test>
Subject: Sender only
Content-Type: text/plain; charset=utf-8

Sender-only body.
`

const multiAttachmentMessage = `Message-ID: <attachments-11@example.test>
Date: Fri, 03 Jul 2026 08:11:00 +0000
From: Attach Sender <attach@example.test>
To: Bob <bob@example.test>
Subject: Attachment set
Content-Type: multipart/mixed; boundary="attach-boundary"

--attach-boundary
Content-Type: text/plain; charset=utf-8

Attachment body.
--attach-boundary
Content-Type: application/octet-stream
Content-Disposition: attachment; filename="normal.bin"
Content-Transfer-Encoding: base64

AAECAw==
--attach-boundary
Content-Type: text/plain; charset=utf-8
Content-Disposition: attachment; filename="=?UTF-8?Q?caf=C3=A9.txt?="
Content-Transfer-Encoding: quoted-printable

cafe payload
--attach-boundary
Content-Type: application/pdf
Content-Disposition: attachment; filename*=utf-8''r%C3%A9sum%C3%A9.pdf
Content-Transfer-Encoding: base64

cGRm
--attach-boundary
Content-Type: application/json; name="fallback.json"
Content-Transfer-Encoding: 7bit

{}
--attach-boundary
Content-Type: application/empty
Content-Disposition: attachment; filename="empty.dat"
Content-Transfer-Encoding: base64


--attach-boundary--
`

const identitylessMessage = `Content-Type: text/plain; charset=utf-8

`

const missingDateMessage = `Message-ID: <missing-date-12@example.test>
From: No Date <nodate@example.test>
To: Bob <bob@example.test>
Subject: Missing date
Content-Type: text/plain; charset=utf-8

This message has no Date header.
`

const malformedBodyMessage = `Message-ID: <malformed-body-13@example.test>
Date: Fri, 03 Jul 2026 08:13:00 +0000
From: Bad Body <badbody@example.test>
To: Bob <bob@example.test>
Subject: Malformed body
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: base64

%%%
`

const malformedHeaderMessage = "This malformed header has no colon\r\n\r\nbody text that must not leak"

const afterBadMessage = `Message-ID: <after-bad-22@example.test>
Date: Fri, 03 Jul 2026 08:22:00 +0000
From: Later Sender <later@example.test>
To: Bob <bob@example.test>
Subject: After bad messages
Content-Type: text/plain; charset=utf-8

This message proves the fetch continues after diagnostics.
`

const workOneMessage = `Message-ID: <work-1@example.test>
Date: Fri, 03 Jul 2026 09:00:00 +0000
From: Work Sender <work@example.test>
To: Worker <worker@example.test>
Subject: Work one
Content-Type: text/plain; charset=utf-8

Work one body.
`

const workTwoMessage = `Message-ID: <work-3@example.test>
Date: Fri, 03 Jul 2026 09:02:00 +0000
From: Work Sender <work@example.test>
To: Worker <worker@example.test>
Subject: Work two
Content-Type: text/plain; charset=utf-8

Work two body.
`

const workThreeMessage = `Message-ID: <work-5@example.test>
Date: Fri, 03 Jul 2026 09:05:00 +0000
From: Work Sender <work@example.test>
To: Worker <worker@example.test>
Subject: Work three
Content-Type: text/plain; charset=utf-8

Work three body.
`

const projectMessage = `Message-ID: <project-1@example.test>
Date: Fri, 03 Jul 2026 09:10:00 +0000
From: Project Sender <project@example.test>
To: Worker <worker@example.test>
Subject: Project update
Content-Type: text/plain; charset=utf-8

Project body.
`

const reportMessage = `Message-ID: <report-1@example.test>
Date: Fri, 03 Jul 2026 09:20:00 +0000
From: Report Sender <report@example.test>
To: Reader <reader@example.test>
Subject: Отчёт
Content-Type: text/plain; charset=utf-8

Report body.
`

const collisionMessage = `Message-ID: <collision@example.test>
Date: Fri, 03 Jul 2026 09:30:00 +0000
From: Collision Sender <collision@example.test>
To: Reader <reader@example.test>
Subject: Collision shared identity
Content-Type: text/plain; charset=utf-8

Same logical message in two display-colliding folders.
`

const flatMessage = `Message-ID: <flat-1@example.test>
Date: Fri, 03 Jul 2026 09:40:00 +0000
From: Flat Sender <flat@example.test>
To: Reader <reader@example.test>
Subject: Flat mailbox
Content-Type: text/plain; charset=utf-8

Flat body.
`
