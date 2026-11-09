# Using mailmd with a coding agent

Coding agents like Claude Code and Codex are good at searching and reading
folders of text files. Once your mail is a folder of Markdown files, you can
ask an agent about your mail like you ask it about code. The agent needs no
plugin and no login, because it only reads files.

## Set it up

If mailmd isn't set up yet, paste this into your agent:

```text
Set up mailmd so you can read my email.
Use https://github.com/vincentsch/mailmd: read its AGENTS.md and follow
the steps. Connect my mailbox and run the first sync. Then show me where
the files are.
```

The agent installs mailmd and runs the first sync. When mailmd needs your
password, the agent asks you to type it into your own terminal, so the agent
never sees it.

If you already have an archive, start the agent in it. It is in `~/mail`,
or in the folder you gave to `--out`:

```sh
cd ~/mail
claude          # or: codex
```

Or tell the agent where it is: "My email is in ~/mail as Markdown files, one
folder per account."

## Things to ask

Ask the way you would ask a colleague who has read your mail:

```text
Find the last invoice from Northwind Studio and tell me the total.
What did I agree with Bob about the Q4 roadmap?
Which emails from this week still need an answer from me?
List the newsletters I got this month and summarize the two most useful ones.
When does my lease end? It was in an email from the landlord last year.
Draft a reply to the newest message from Alice, as a text file. Don't send anything.
Make a table of my flight bookings in 2026, with the date and the price.
```

The agent can also open attachments, like a PDF invoice or a calendar
invite. They are saved next to each message, and the message file links to
them.

## Tips

For questions about many messages, point the agent at `index.jsonl`. It has
one line per message with its sender and subject, and `to` lists the
recipients. `attachments` has the names of the attachments, like
`["INV-1043.pdf"]`. So the agent can filter the index with `jq` or `rg`
before it opens any file. [The file format](files.md#indexjsonl) lists the fields.

Each file starts with lines like `from:` and `date:`. So a search like
`rg -il '^from: .*northwind' ~/mail/work` finds all mail from a sender.

Messages of one conversation share a `thread_id`. Ask the agent to read the
whole thread before it answers a question about a conversation.

Deleted mail stays in the archive. A file with `deleted_on_server` in its
frontmatter is a message you deleted, or an old version of a draft. Tell the
agent to check that field before it treats a message as current.

Run `mailmd sync` before you ask about new mail, or set up a [scheduled
sync](scheduling.md). The agent can also run `mailmd sync` itself. When a
message seems to be missing, `mailmd list folders` shows the folders on the
server and which of them a sync leaves out, like Junk.

A message file lists its `flags`, like `seen` and `answered`. Some servers
keep no such marks. Then every message looks unread, and `mailmd status`
says so in its `Flags` line. For a question like "which emails still need an
answer", ask the agent to look for your replies in the Sent folder.

To tell every new session where your mail is, add a few lines to the agent's
instructions, like `CLAUDE.md` or `AGENTS.md` in your home folder:

```markdown
My email is in ~/mail as Markdown files, one folder per account, synced by
mailmd. Each account has an index.jsonl with one line per message. Run
`mailmd sync` to fetch new mail. Never delete or edit files in ~/mail.
To tell whether I answered a message, look for my reply in the same thread,
usually in the Sent folder. Don't go by the flags alone, because some
servers keep no read or answered marks.
```

The note above is for the default folders. Change it if you set up mailmd
in another way:

- If you connected with `--out`, write that folder instead of `~/mail`.
- If you keep mailmd's settings in a folder of your own with
  `MAILMD_CONFIG_ROOT`, the sync command in the note needs that variable.
  Without it, mailmd finds no account.

With `--out ~/Documents/mail` and the settings in
`/home/dana/work/mailmd-config`, the note becomes:

```markdown
My email is in ~/Documents/mail as Markdown files, one folder per account,
synced by mailmd. Each account has an index.jsonl with one line per message.
Run `MAILMD_CONFIG_ROOT=/home/dana/work/mailmd-config mailmd sync` to fetch
new mail. Never delete or edit files in ~/Documents/mail. To tell whether I
answered a message, look for my reply in the same thread, usually in the
Sent folder. Don't go by the flags alone, because some servers keep no read
or answered marks.
```

## Privacy

mailmd itself doesn't send your mail anywhere. But an agent that runs on a
cloud model sends the text it reads to that model's provider, like with any
other file you show it. If some mail must not leave your computer, leave it
out of the archive with `--exclude`, or use an agent with a local model.

mailmd saves its logins in its config folder, outside the archive. An agent
that only reads `~/mail` doesn't come across them. But an agent that can read
all your files can also read the logins.

## Can the agent send or delete mail?

No. mailmd only reads from your mailbox, and none of its commands change
anything there. When you change a file in the archive, nothing changes on the
server.
