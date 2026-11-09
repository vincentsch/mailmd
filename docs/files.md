# The file format

This page describes everything mailmd writes into an archive. You can rely
on it in scripts and agent instructions.

## Folder layout

```text
~/mail/                                  the archive root (--out)
└── work/                                one folder per account
    ├── INBOX/
    │   └── 2026/
    │       ├── 2026-10-21-invoice-2026-114-for-october--906396084b0bb319.md
    │       ├── 2026-10-21-invoice-2026-114-for-october--906396084b0bb319.attachments/
    │       │   ├── 01-invoice-2026-114.pdf
    │       │   └── 02-timesheet-october.csv
    │       └── 2026-10-21-invoice-2026-114-for-october--906396084b0bb319.eml   (only with --keep-raw)
    ├── Work/Clients/2026/...             subfolders keep the provider's folder tree
    ├── index.jsonl                      one line per message
    └── .mailmd/                         sync state, not for you to edit
```

Each message is saved under its folder, then the year it was sent. The file
name has three parts:

- The date the message was sent, in the sender's time zone. A message sent at
  23:30 on New Year's Eve in Berlin goes under the old year, even if your
  computer is in New York.
- The subject in lower case, with every run of spaces and punctuation turned
  into one `-`. Letters with accents and other scripts stay as they are, like
  `grüße-aus-münchen` or `会議の議事録`. Long subjects are cut at 80 bytes.
  Without a subject, this part is `no-subject`.
- The first 16 characters of the message's `local_id`, after `--`.

Messages without a date go to a `no-date` folder.

mailmd makes folder and file names safe for each operating system. It
replaces characters that Windows doesn't allow, like `:` or `?`, and it adds
a prefix to names like `CON`. A folder name that had to change this way also
gets a short suffix made from its real name, like `A-B-3f2a1b0c` for `A:B`.
So two folders never end up in one directory. The same happens to a folder
whose name differs from another only in case, like `work` next to `Work`,
because macOS and Windows treat those as one folder.

## A message file

```markdown
---
subject: "Re: Q4 roadmap draft"
from: "Bob Chen <bob@acme.example>"
to: ["Dana Reyes <dana@example.com>"]
date: "2026-10-16T10:30:00-04:00"
folder: "INBOX"
flags: ["seen", "answered"]
thread_id: "<q4-plan@acme.example>"
message_id: "<q4-plan-re2@acme.example>"
in_reply_to: "<q4-plan-re1@example.com>"
references: ["<q4-plan@acme.example>", "<q4-plan-re1@example.com>"]
ref: "1729000000:412"
local_id: "mailmd-localid-v1-6fa6bb20654d54ec5eae2b5513e9be1e30ca255f434cc3a3dd4d12f7135e4cca"
---

# Re: Q4 roadmap draft

Agreed, the weekly plan goes. I will update the page today.

Bob
```

The file starts with YAML frontmatter, which is the block between the two
`---` lines. Then come the subject as a heading and the body. Each value is a
quoted string or a list of quoted strings on one line. Only `omitted_bytes`
is a number. Fields without a value are left out.

| Field | What it holds |
| --- | --- |
| `subject` | The decoded subject |
| `from` | The sender, as `Name <address>` or only the address |
| `to`, `cc`, `bcc`, `reply_to` | Lists of recipients in the same form |
| `date` | When the message was sent, from its Date header, with the sender's UTC offset. If the header is missing or broken, mailmd uses the time the provider received the message. |
| `folder` | The provider's folder, like `INBOX` or `Work/Clients` |
| `labels` | Gmail API only: all labels of the message, like `["Inbox", "Work/Clients"]` |
| `flags` | Any of `seen`, `flagged`, `answered` and `draft` |
| `deleted_on_server` | The day mailmd noticed that the message was deleted on the server, like `"2026-10-26"`. The file stays. |
| `attachments` | The attachment file names, in order |
| `omitted_bytes` | Only when the body was left out because the message is too large (see below) |
| `parse_error` | Only when mailmd could not read the message (see below) |
| `thread_id` | The conversation. For Gmail it is Gmail's thread ID, and for Outlook the conversation ID. For IMAP it is the Message-ID of the first message in the thread, see below. |
| `message_id`, `in_reply_to`, `references` | The message's own threading headers |
| `ref` | The provider's ID of the message. For IMAP these are two numbers from the server, the folder's UIDVALIDITY and the message's UID, like `1729000000:412`. |
| `local_id` | mailmd's ID for the message. mailmd computes it from the message itself, so the same message gets the same ID in every folder and on every computer. |

When the flags of a message change on the server, the next sync writes the
new flags into the frontmatter of that file. The body doesn't change.

IMAP has no thread IDs, so mailmd uses the first message of the References
header. Many mail programs cut References short or send only In-Reply-To.
When mailmd already has the message that a reply answers, the reply joins
that message's thread instead. A reply without any of these headers gets its
own `thread_id`. mailmd never joins threads by subject, because a subject
like "Invoice" would put unrelated mail into one thread.

## The body

When a message has an HTML part, mailmd converts it to Markdown, because the
HTML has the links and tables. mailmd uses the plain text part instead when
the message has no HTML, or when the conversion lost most of the words. That
happens with a newsletter that is one big image. With `--prefer-text` (on
`connect` or `sync`), mailmd uses the plain text part whenever there is one.

The conversion works like this:

- Headings and lists become Markdown headings and lists.
- Links come with their address, like `[the report](https://example.com/r)`.
- Tables that only arrange the layout of a newsletter become plain
  paragraphs.
- Headings move one level down, so the subject stays the only `#` heading.
- Tables with rows of data become Markdown tables. A cell that spans columns
  gets empty cells after it, so a receipt's total stays under "Amount". A
  table with cells that span rows becomes plain paragraphs.
- Hidden text is dropped, like the preview line that many newsletters put
  at the top. mailmd finds it through inline styles and through the simple
  rules of the message's style sheet. Scripts and styles are also dropped.
- Remote images are never loaded. An image with alt text becomes
  `[image: alt text]`. An image without alt text, like a tracking pixel, is
  dropped. Images that came inside the message link to their saved files.

Plain text is written as it is, with one change. A `<` that could start an
HTML tag gets a backslash in front, and so does the `[` after the `!` of a
Markdown image. A Markdown viewer then shows such text as text, so opening a
file never loads a remote image or runs HTML from the mail. An address in
angle brackets, like `<dana@example.com>`, stays as it is, because a viewer
can only make a link of it.

When a message has several text parts one after the other, like a message
and a mailing list footer, mailmd writes all of them in their order.

Some old mail has text with accents but doesn't say which character set it
uses. mailmd reads such text as Windows-1252. That is right for most of this
mail, and you get `é` instead of broken characters.

## Attachments

Attachments are saved in a folder next to the message. The folder has the
name of the Markdown file with `.attachments` at the end. Each file name
starts with a number, so the files are listed in their original order. An
attachment without a name gets one from its type, like `attachment.pdf`. A
calendar invite becomes `invite.ics`, and an attached message becomes
`forwarded-message.eml`.

The message file ends with a list of links to them:

```markdown
## Attachments

- [invoice-2026-114.pdf](2026-10-21-invoice-2026-114-for-october--906396084b0bb319.attachments/01-invoice-2026-114.pdf) (application/pdf, 125 bytes)
```

## Invites

Outlook often sends an invite with no text, only the calendar data. mailmd
then writes the event into the file, so you can read it and search it:

```markdown
## Invitation

**Title:** Quarterly planning
**When:** Thu, 5 Nov 2026, 10:00 to 11:30 (W. Europe Standard Time)
**Location:** Seeblick, room 2
**Organizer:** Dana Weber <dana@contoso.com>
**Attendees:** Max Muster <max@example.com>, Lea Roth <lea@contoso.com> (maybe)

The agenda from the invite.
```

The times are the ones in the invite, with its time zone. mailmd doesn't
convert them to yours. A canceled meeting gets the heading `## Canceled
event`. The `.ics` file is also saved as an attachment. An invite that has
text, like the ones Google Calendar sends, keeps its text.

## Forwarded messages

When a message has another message attached, mailmd writes the attached
message into the file, under a `## Forwarded message` heading. Its headers
come first, then its text. This way a search finds the text of forwarded
mail. The attached message is also saved as an `.eml` file. Its own
attachments stay inside that file, and the headers list their names.

## Messages that are too large

A message larger than the size limit (64 MiB by default, set with
`--max-message-bytes`) is saved with its headers only. Its file has
`omitted_bytes` in the frontmatter and this note instead of the body:

```markdown
_mailmd saved only the headers of this message. The message is 210.4 MB, which is more than the size limit._
```

`mailmd status <account>` lists these messages. To get the full message,
sync again with a limit above its size:

```sh
mailmd sync work --max-message-bytes 300000000
```

mailmd then fetches the message again and replaces the headers-only file with
the full message, in the same place.

## Messages mailmd can't read

Some mail is broken. A message can be cut off, or an attachment can have
broken base64. mailmd still saves such a message. Its file has the headers
that mailmd could read and `parse_error` with the reason. The original
message is always saved next to it as an `.eml` file, and the file links to
it:

```markdown
_mailmd could not read this message, because the MIME structure is broken. The original message is saved as [2026-10-12-quarterly-report--4f1c2d9a0b7e6f55.eml](2026-10-12-quarterly-report--4f1c2d9a0b7e6f55.eml)._
```

`mailmd status <account>` also lists these messages.

## The original message

With `--keep-raw`, mailmd also saves the original message exactly as the
provider sent it. It goes into an `.eml` file next to the Markdown file, and
any mail program can open it. This works for the messages a sync fetches
from then on. Messages that are already in the archive don't get an `.eml`
file.

## index.jsonl

Each account folder has an `index.jsonl` file with one JSON object per line,
one for each message file, newest first:

```json
{"date":"2026-10-21T11:00:00+01:00","from":"Northwind Studio <billing@studio.example>","to":["dana@example.com"],"subject":"Invoice 2026-114 for October","folder":"Work/Clients","flags":["seen"],"thread":"<invoice-114@studio.example>","message_id":"<invoice-114@studio.example>","attachments":2,"ref":"1729000000:412","path":"Work/Clients/2026/2026-10-21-invoice-2026-114-for-october--906396084b0bb319.md"}
```

| Field | What it holds |
| --- | --- |
| `date`, `from`, `subject`, `folder`, `labels`, `flags` | The same as in the frontmatter |
| `to` | The recipients, as a list in the same form as `to` in the frontmatter |
| `thread`, `message_id` | The same as `thread_id` and `message_id` |
| `attachments` | The number of attachments |
| `omitted_bytes` | The size of a message saved with its headers only |
| `ref` | The same as `ref` in the frontmatter |
| `gone` | `true` when the message was deleted on the server |
| `path` | The Markdown file, relative to the account folder, with `/` |

Empty fields are left out. A sync adds lines as it saves messages and sorts
the file when it is done. If you delete the file, or a crash damages a line,
the next sync builds the file again from the Markdown files. They hold every
field of the index, so nothing is lost.

Some things you can do with `jq`:

```sh
cd ~/mail/work
# The ten newest messages from Northwind
jq -r 'select((.from // "") | test("northwind"; "i")) | "\(.date) \(.subject)"' index.jsonl | head
# Unread mail in the inbox
jq -r 'select(.folder == "INBOX" and ((.flags // []) | index("seen") | not)) | .path' index.jsonl
# All messages of one thread, oldest first
jq -r 'select(.thread == "<q4-plan@acme.example>") | .path' index.jsonl | tac
# Mail you sent to Bob
jq -r 'select(.folder == "Sent" and any(.to[]?; test("bob@"; "i"))) | "\(.date) \(.subject)"' index.jsonl
```

## When things change on the server

mailmd never deletes or renames your files. This is what happens when
something changes on the server.

A new message gets a new file. When the flags of a message change, the next
sync updates the `flags` in its file.

When a message is deleted, its file stays. The frontmatter gets
`deleted_on_server` with the day, and the message's line in `index.jsonl`
gets `"gone": true`. The sync tells you how many messages were deleted.

Most mail programs save a new version of a draft as a new message and delete
the old one. So the new version gets a new file, and the old file gets
`deleted_on_server`.

When you move a message to another folder over IMAP or in Outlook, it gets a
new file in the new folder. The old file is treated like a deleted message.

When a message gets other labels in Gmail (with the Gmail API), its file
stays in the folder where it was first saved. mailmd updates its `labels`
and `flags`. This way archiving a message in Gmail doesn't make a second
copy.

When a folder is renamed, mailmd syncs the new name like a new folder. The
files in the old folder stay, and `mailmd status` lists the old folder as
missing on the server.

Sometimes an IMAP server numbers the messages of a folder again (the folder's
UIDVALIDITY changes). Then mailmd reads the whole folder again. Messages it
already has don't get a second file, because `local_id` doesn't depend on
the server's numbers. Only their `ref` changes to the new numbers.

## The .mailmd folder

`.mailmd/state.json` holds the position where each folder's sync stopped.
`.mailmd/lock` stops a second sync from working on the same archive at the
same time. Don't edit these files.

If you delete the `.mailmd` folder, the next sync reads all folders again and
finds that the messages are already there. There is one exception. A message
that was first saved with its headers only and later fetched in full gets a
second file, under its own ID.

If you put the archive in Git, a sync that finds nothing new changes no
files. `state.json` changes when a sync fetched something. It also changes
once on the sync after the first one, when mailmd checks the read and starred
marks of the new messages for the first time.

## Changing the format

The format may still change before version 1.0. Changes are listed in the
[changelog](../CHANGELOG.md).
