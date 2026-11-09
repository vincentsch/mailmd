[Changing mailmd itself? Read CONTRIBUTING.md](CONTRIBUTING.md)

# Set up mailmd for a user

mailmd copies a mailbox into a folder of Markdown files, and each sync adds
the new mail. It only reads from the mail provider. Follow these steps when a
user asks you to set it up so you can read their email.

The setup prompt is:

```text
Set up mailmd so you can read my email.
Use https://github.com/vincentsch/mailmd: read its AGENTS.md and follow
the steps. Connect my mailbox and run the first sync. Then show me where
the files are.
```

## Rules

- Don't ask for the user's password in the chat. The user types it into
  mailmd's hidden prompt in their own terminal. If the user gives you the
  password anyway and asks you to do the setup, follow
  [the steps for that case](#if-the-user-gives-you-the-password).
- Don't open files in mailmd's config folder. They hold the saved logins.
- Don't edit or delete files in the archive. A sync may overwrite them, and
  the user may rely on them.
- Ask before you change anything outside the archive, like the user's agent
  instructions or a scheduled job.
- mailmd can't change anything in the mailbox. It can't send a reply or
  delete a message, so don't offer that.

## 1. Check the binary

Run `mailmd --version`. If the shell says the command is not found, install
it:

```sh
curl -fsSL https://raw.githubusercontent.com/vincentsch/mailmd/main/install.sh | sh
```

The installer works on Linux and macOS, and on Windows in Git Bash. It puts
the binary in `~/.local/bin`, or in `INSTALL_DIR` when that is set. If the
installer says the folder is not on the PATH, use the full path for the rest
of this session and tell the user how to add it. Don't edit shell profiles
unless the user asks. With Go installed, this also works:

```sh
go install github.com/vincentsch/mailmd/cmd/mailmd@latest
```

If `mailmd --version` fails for any other reason, report the error instead of
installing over it.

## 2. Look for existing accounts

Run `mailmd list accounts`. If the user's mailbox is already there, go on with
step 5.

## 3. Choose how to connect

Ask the user for the email address they want to connect, unless they gave it
already. Then pick the way:

| Address | Way to connect |
| --- | --- |
| Outlook.com, Hotmail, Live, or a Microsoft 365 work mailbox | `mailmd connect outlook`. The user needs an app registration first: walk them through [docs/outlook.md](docs/outlook.md). |
| Gmail | `mailmd connect imap` with an app password is the quickest. mailmd then syncs only `[Gmail]/All Mail`. That is every message once, archived mail included, in one folder. There is no Inbox or Sent split and no Gmail labels. The `from:` line shows which messages the user sent. If the user wants the labels, use `mailmd connect gmail` and walk them through [docs/gmail.md](docs/gmail.md). |
| Anything else | `mailmd connect imap` |

For IMAP, [docs/imap.md](docs/imap.md) lists which providers need an app
password and where the user creates it. Tell the user before they run the
command, so they have the app password ready.

If the user's server uses its own certificate authority, like many company
mail servers, add `--ca-file <file>` with the authority's certificate as a
PEM file. mailmd saves it with the account, so later syncs need no
environment variable like `SSL_CERT_FILE`.

If the user wants the mail somewhere other than `~/mail`, add `--out <folder>`.
If the user wants mailmd's settings and login in a folder of their own, set
the `MAILMD_CONFIG_ROOT` environment variable to that folder. If your shell
keeps variables between commands, export it once. Otherwise put it in front
of each command, like `MAILMD_CONFIG_ROOT=<folder> mailmd sync`. A scheduled
job and the note for later sessions need the same variable.
To leave out folders or older mail, add `--exclude <folder>` or
`--since <age>`, like `--since 2y`. To also copy a folder that is left out by
default, like Junk, add `--include Junk`. mailmd saves these settings. Don't
use `--folder` to add a folder. On connect, `--folder` means that a sync
copies only the folders it names. [How the folder flags work
together](docs/quickstart.md#how-the-folder-flags-work-together) explains
the rules.

## 4. Let the user connect

Give the user the exact command and ask them to run it in their own terminal
window, not through you:

```sh
mailmd connect imap --email dana@example.com
```

Most agent tools don't give commands a terminal for typing a password, so the
hidden prompt only works when the user runs the command. mailmd first asks
for anything else it needs, then for the password. Then it checks the login.

For Gmail with the Gmail API, the user runs `mailmd connect gmail
--client-secret <file>` and signs in in the browser. For Outlook, the user
runs `mailmd connect outlook --client-id <id>` and enters the code that
mailmd shows on Microsoft's page. Neither needs a password from you.

When the user says it worked, run `mailmd list accounts` to check, then
`mailmd list folders <account>`. The connect output went to the user's
terminal, so you didn't see notes like which folders a sync leaves out.
`list folders` shows each folder on the server with its number of messages,
and whether a sync copies it. If the connect failed, ask the user for the
error. Its second line, when there is one, says what to do.
[docs/troubleshooting.md](docs/troubleshooting.md) has more.

When a login stops working later, the user connects again with only the
account name. For IMAP, that is `mailmd connect imap --account <name>`, and
the user types the new password. For Gmail and Outlook, it is
`mailmd connect gmail --account <name>` or
`mailmd connect outlook --account <name>`. mailmd uses the client it saved,
so the user needs no client file or client ID for that.

To change a setting later, like the folders a sync copies or the archive
folder, run the connect command with `--account <name>` and the new
setting, like `mailmd connect imap --account work --include Junk`.
mailmd keeps the saved password or login and asks for nothing, so you can
run it yourself. Run it with `--dry-run` first and show the user what it
says. Only another `--host` or `--username` needs the password again.

A new `--out` doesn't move the files that are already archived. If the user
wants the archive somewhere else, follow
[Moving the archive](docs/quickstart.md#moving-the-archive), and ask before
you move their files.

When the user wants to remove an account, run
`mailmd disconnect <account> --dry-run` and show the user which files it
would delete. Run it without `--dry-run` only when the user agrees. It
deletes the saved login and keeps the archive. For Gmail and Outlook, pass
on the last paragraph of its output. It says where the user can remove the
app's access in their Google or Microsoft account.

### If the user gives you the password

Sometimes the user writes the password into the chat and asks you to do the
setup. Then you may run the connect command yourself. Pass the password on
standard input with `--password-stdin`, through the shell's built-in
`printf`:

```sh
printf '%s\n' 'the-password' | mailmd connect imap --email dana@example.com --password-stdin
```

- Put the password in single quotes. Write a single quote inside it as
  `'\''`.
- mailmd can't ask questions without a terminal, so give the settings as
  flags. A server that mailmd doesn't know needs `--host`.
- Never echo the password or repeat it in your answers. Don't write it into
  a file or a script, and don't set it as an environment variable.
- When the connect worked, tell the user that mailmd saved the password in
  its config folder. The file is not encrypted, and on Linux and macOS only
  the user can read it. The password is now also in this chat, so suggest an
  app password if the provider offers one.

## 5. Run the first sync

```sh
mailmd sync <account>
```

The first sync of a large mailbox can take a long time. If your tool limits
how long a command may run, start it in the background, or ask the user to
run it. Stopping it is safe. Everything saved so far stays, and the next run
continues from there.

When it is done, run `mailmd status <account>`. It shows the archive folder
and the number of messages. It also lists messages that were not saved in
full.

## 6. Show the result

Tell the user where the archive is and how it is laid out. Show the folder
tree two levels deep. Then list the newest few messages from `index.jsonl`,
with date, sender and subject. Then offer these two things, and do them only
if the user agrees:

- To add a note to their agent instructions, like `~/.claude/CLAUDE.md` or
  `~/.codex/AGENTS.md`, so later sessions know where the mail is:

  ```markdown
  My email is in ~/mail as Markdown files, one folder per account, synced by
  mailmd. Each account has an index.jsonl with one line per message. Run
  `mailmd sync` to fetch new mail. Never delete or edit files in ~/mail.
  To tell whether I answered a message, look for my reply in the same thread,
  usually in the Sent folder. Don't go by the flags alone, because some
  servers keep no read or answered marks.
  ```

  Fit the note to the setup. If you used `--out`, write that folder instead
  of `~/mail`. `mailmd list accounts` shows the archive folder of each
  account, and the note names the folder that holds it. If you set
  `MAILMD_CONFIG_ROOT`, put it in front of the sync command with the full
  path, like `MAILMD_CONFIG_ROOT=/home/dana/work/mailmd-config mailmd sync`.
  [docs/agents.md](docs/agents.md#tips) shows a note with both.

- To sync every 15 minutes, using the example for their system in
  [docs/scheduling.md](docs/scheduling.md).

# Answer questions about the user's mail

- Run `mailmd sync` first when the user asks about recent mail.
- Start with `index.jsonl` in the account folder. It has one JSON line per
  message, newest first. Each line has the sender (`from`) and the
  recipients (`to`) of a message. It also has the subject and the path of
  the file, and `attachments` lists the names of the attachments. Filter it
  with `jq` or `rg`, then open only the files you need.
- Search message files with `rg -il`. The frontmatter has one `from:`, `to:`
  and `subject:` line per file.
- Messages with the same `thread_id` belong to one conversation. Read the
  whole thread before you summarize it.
- A message without `flags` is unread. Some servers keep no flags, and then
  every message looks unread. `mailmd status <account>` has a `Flags` line
  when no message is marked as read or answered. Then don't tell the user
  that their mail is unread. To tell whether the user answered a message,
  look for their reply in the same thread, usually in the Sent folder.
- A file with `deleted_on_server` in its frontmatter was deleted on the
  server. It may be an old version of a draft. Say so when you use it.
- A file with `parse_error` holds only the headers mailmd could read. The
  original message is the `.eml` file it links to.
- Attachments are in the `.attachments` folder next to each message, and the
  message links to them.
- If a message the user expects is missing, run `mailmd list folders
  <account>`. It shows each folder on the server with its number of
  messages, and whether a sync copies it. Junk and Trash are left out by
  default. `mailmd sync <account> --folder Junk --dry-run` says how many new
  messages are in Junk without writing anything. If the user wants Junk
  copied on every sync, run `mailmd connect <provider> --account <account>
  --include Junk`.
- Name the files you used, so the user can check them.

[docs/files.md](docs/files.md) describes every field and file.
