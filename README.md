# mailmd

**Your mailbox as Markdown files.**

mailmd copies your email into a folder of Markdown files on your computer.
Each time you run `mailmd sync`, it adds the new mail. A message becomes one
file that starts with its sender and subject, and its attachments are saved
next to it. You can open the files in any editor and search them with
`rg`. Or you start a coding agent like Claude Code or Codex in the folder and
ask it about your mail.

mailmd works with any mailbox that offers IMAP. You can also connect Gmail
and Outlook through their own APIs. mailmd only reads your mail. It doesn't
change anything in your mailbox, and it doesn't send your mail anywhere.

[Quickstart](docs/quickstart.md) · [Use it with an agent](docs/agents.md) · [File format](docs/files.md) · [FAQ](docs/faq.md)

## Set it up with your coding agent

Paste this into your coding agent, like Claude Code or Codex:

```text
Set up mailmd so you can read my email.
Use https://github.com/vincentsch/mailmd: read its AGENTS.md and follow
the steps. Connect my mailbox and run the first sync. Then show me where
the files are.
```

The agent installs mailmd and asks for your email address. You type your
password into mailmd yourself, so the agent never sees it. After the first
sync, the agent shows you the folder, and you can ask it about your mail.

## Try it without an account

```sh
curl -fsSL https://raw.githubusercontent.com/vincentsch/mailmd/main/install.sh | sh
mailmd demo
```

If the installer prints a PATH command, run that first. `mailmd demo` writes
eight sample messages to `./mailmd-demo`. It doesn't connect to anything.

```text
mailmd-demo/demo
├── INBOX
│   └── 2026
│       ├── 2026-10-14-lunch-on-thursday--f21359fdd659276c.md
│       ├── 2026-10-15-q4-roadmap-draft--09a29b9b3bf707cd.md
│       ├── 2026-10-16-re-q4-roadmap-draft--6fa6bb20654d54ec.md
│       ├── 2026-10-20-grüße-aus-münchen-und-die-schlüssel--9849e54ebf38d7e9.md
│       ├── 2026-10-22-invitation-design-review-on-oct-28--308defc00a37c558.attachments
│       │   └── 01-invite.ics
│       └── 2026-10-22-invitation-design-review-on-oct-28--308defc00a37c558.md
├── Newsletters
│   └── 2026
│       └── 2026-10-17-the-go-digest-212--8a6baac99b65b696.md
├── Sent
│   └── 2026
│       └── 2026-10-16-re-q4-roadmap-draft--e6b9c1e14bc11749.md
├── Work
│   └── Clients
│       └── 2026
│           ├── 2026-10-21-invoice-2026-114-for-october--906396084b0bb319.attachments
│           │   ├── 01-invoice-2026-114.pdf
│           │   └── 02-timesheet-october.csv
│           └── 2026-10-21-invoice-2026-114-for-october--906396084b0bb319.md
└── index.jsonl
```

This is the invoice message from that folder:

```markdown
---
subject: "Invoice 2026-114 for October"
from: "Northwind Studio <billing@studio.example>"
to: ["dana@example.com"]
date: "2026-10-21T11:00:00+01:00"
folder: "Work/Clients"
attachments: ["invoice-2026-114.pdf", "timesheet-october.csv"]
thread_id: "<invoice-114@studio.example>"
message_id: "<invoice-114@studio.example>"
local_id: "mailmd-localid-v1-906396084b0bb319c50183d5dec2897fbf5b6fafa7f5226fec0fb557c39723cc"
---

# Invoice 2026-114 for October

Hello Dana,

attached is invoice 2026-114 for the design work in October, and the
timesheet with the hours.

Kind regards,
Northwind Studio

## Attachments

- [invoice-2026-114.pdf](2026-10-21-invoice-2026-114-for-october--906396084b0bb319.attachments/01-invoice-2026-114.pdf) (application/pdf, 125 bytes)
- [timesheet-october.csv](2026-10-21-invoice-2026-114-for-october--906396084b0bb319.attachments/02-timesheet-october.csv) (text/csv, 98 bytes)
```

The part between the two `---` lines is the frontmatter. It holds the
message's headers in a form that scripts can read. mailmd turns HTML mail
into Markdown with its links and tables, and it drops hidden preview text and
tracking images. `index.jsonl` has one line per message, so a script or an
agent can find mail without opening every file. [The file
format](docs/files.md) describes each field.

## Connect your mailbox

For most providers you only need your email address. mailmd knows the server
settings of common providers like Gmail and iCloud, and it asks for anything
it can't guess:

```sh
mailmd connect imap --email you@fastmail.com
```

Many providers want an app password for IMAP. That is a separate password
you create in your account settings for one program. mailmd tells you when
your provider needs one, and the [IMAP guide](docs/imap.md) has a table with
the links. For your own mail server, add `--host`.

To connect Gmail or Outlook through their own APIs, you first create your
own OAuth app in Google Cloud or Microsoft Entra. This app is what lets
mailmd sign in with read-only access. The [Gmail guide](docs/gmail.md) and
the [Outlook guide](docs/outlook.md) show each step:

```sh
mailmd connect gmail --client-secret ~/Downloads/client_secret.json
mailmd connect outlook --client-id 12345678-1234-1234-1234-123456789abc
```

Outlook.com and most Microsoft 365 mailboxes no longer accept passwords over
IMAP, so use `mailmd connect outlook` for them.

`mailmd list accounts` shows the accounts you connected. To remove one, run
`mailmd disconnect <account>`. It deletes the saved login and leaves the
archive as it is.

## Sync

```sh
mailmd sync
```

```text
mailmd checked 4 folders and saved 624 new messages to ~/mail/work in 12 s.
```

The mail goes to `~/mail/<account>`. To put it somewhere else, give `--out`
to `mailmd connect`. `--out` is the folder that holds the account folders, so
with `--out ~/Documents/mail` the account `work` goes to
`~/Documents/mail/work`.

The next sync fetches only new mail. It also updates the read and starred
marks that changed. If you stop a sync with Ctrl-C, the files saved so far
stay, and the next run continues from there.

mailmd leaves out spam and trash by default. [Which folders count as spam
and trash](docs/quickstart.md#which-folders-count-as-spam-and-trash) says how
it finds them. For a Gmail address over IMAP, it syncs only
`[Gmail]/All Mail`. You get one copy of every message, including mail you
archived, but no separate Inbox or Sent folder
([details](docs/imap.md#gmail-over-imap)).

To change what a sync copies, give these flags to `mailmd connect` with the
account name. mailmd saves them and keeps the saved password:

- `--include Spam` copies a folder in addition to the default ones.
- `--folder INBOX` copies only the folders you name, from now on.
- `--exclude Newsletters` leaves out a folder.
- `--since 12m` leaves out older mail.

```sh
mailmd connect imap --account work --include Spam --exclude Newsletters
```

`mailmd sync` takes the same flags, except `--include`, for one run, like
`mailmd sync work --folder Spam`. [How the folder flags work
together](docs/quickstart.md#how-the-folder-flags-work-together) explains
the details, like what `'Work/*'` matches.

`mailmd list folders work` shows each folder on the server with its number
of messages. It also says which folders a sync copies, and why it leaves out
the others:

```text
work (IMAP, dana@fastmail.com)

  FOLDER   MESSAGES  SYNC
  Archive  94        yes
  Drafts   0         yes
  INBOX    412       yes
  Sent     118       yes
  Spam     23        no, left out by default
  Trash    7         no, left out by default

A sync copies 4 of these 6 folders.
To copy Spam on every sync, run: mailmd connect imap --account work --include Spam
To copy it once, run: mailmd sync work --folder Spam
```

`mailmd status` shows what is in each archive and when it was last synced.
The sync checked 4 folders, and Drafts was empty, so the archive has mail in
3 of them:

```text
work (IMAP, dana@fastmail.com)
  Archive    ~/mail/work
  Messages   624 in 3 folders
  Last sync  2 minutes ago, completed, checked 4 folders, 624 new
```

To sync on a schedule, let your system run `mailmd sync --quiet` every few
minutes. With `--quiet`, mailmd prints only warnings and errors.
[Scheduling](docs/scheduling.md) shows how to set this up on each operating
system.

## Use it with Claude Code or Codex

Start your agent in the mail folder, or tell it where the folder is. That is
`~/mail`, or the folder you gave to `--out`:

```sh
cd ~/mail && claude
```

Then ask it about your mail:

```text
Find the last invoice from Northwind Studio and tell me the total.
What did I promise Bob in the Q4 roadmap thread?
List the newsletters I got this week and summarize the two most useful ones.
```

The agent searches the Markdown files with its normal tools. [Using mailmd
with an agent](docs/agents.md) has more prompts and tips, like how to search
`index.jsonl` with `jq`. The agent sends what it reads to its model provider,
like with any other file you show it.

## What mailmd does with your mail

- It only reads. IMAP folders are opened read-only, and the Gmail and
  Microsoft logins ask only for read access.
- It never deletes your files. When a message is deleted on the server, the
  file stays and gets `deleted_on_server` in its frontmatter. The sync tells
  you about it.
- A message larger than the size limit (64 MiB by default) is saved with its
  headers and a note. A later sync with a higher `--max-message-bytes`
  replaces it with the full message.
- If mailmd can't read a message, for example because it is cut off, it saves
  the headers it could read. The original message is saved next to it as an
  `.eml` file.
- Logins are saved in mailmd's config folder, outside the archive. They are
  not encrypted. On Linux and macOS, only your user can read these files.
  [SECURITY.md](SECURITY.md) explains what is saved where.
- There is no telemetry. mailmd talks only to your mail provider, and to
  GitHub when you run `mailmd update --check`.

## Install options

The install script works on Linux and macOS, and on Windows in Git Bash. It
checks the download against the published SHA-256 checksum. You can also get
a binary from the [releases page](https://github.com/vincentsch/mailmd/releases)
or build mailmd from source with Go:

```sh
go install github.com/vincentsch/mailmd/cmd/mailmd@latest
```

## Docs

- [Quickstart](docs/quickstart.md)
- Connecting: [IMAP](docs/imap.md), [Gmail](docs/gmail.md), [Outlook and Microsoft 365](docs/outlook.md)
- [File format](docs/files.md)
- [Using mailmd with an agent](docs/agents.md)
- [Scheduling](docs/scheduling.md)
- [Troubleshooting](docs/troubleshooting.md) and [FAQ](docs/faq.md)
- [Command reference](docs/commands/index.md)

[CONTRIBUTING.md](CONTRIBUTING.md) explains how to build and test mailmd.
Report security issues through [SECURITY.md](SECURITY.md).

Built with [rungrad](https://rungrad.com).

## License

[MIT](LICENSE). Made by [Vincent Schmalbach](https://www.vincentschmalbach.com/).
