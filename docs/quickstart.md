# Quickstart

On this page you install mailmd, connect your mailbox and copy your mail into
Markdown files.

## Install

On Linux and macOS, and on Windows in Git Bash:

```sh
curl -fsSL https://raw.githubusercontent.com/vincentsch/mailmd/main/install.sh | sh
```

The script downloads the binary for your system from the latest GitHub
release and checks its SHA-256 checksum. Then it puts the binary in
`~/.local/bin`. If that folder is not on your PATH yet, the script prints the
line to add. Two environment variables change what the script does:

| Variable | What it does |
| --- | --- |
| `INSTALL_DIR` | Install somewhere else, like `INSTALL_DIR=/usr/local/bin` |
| `MAILMD_VERSION` | Install a given release, like `MAILMD_VERSION=v0.1.0` |

You can also download a binary from the [releases
page](https://github.com/vincentsch/mailmd/releases) and compare it with
`checksums.txt`, or build it with Go:

```sh
go install github.com/vincentsch/mailmd/cmd/mailmd@latest
```

Check that it works:

```sh
mailmd --version
```

## See the files first

```sh
mailmd demo
```

This writes eight sample messages to `./mailmd-demo/demo`. The mail is built
into mailmd, so you need no account, and mailmd makes no network
connections. Look at a few of the files to see what your own mail will look
like.

## Connect a mailbox

The quickest way is IMAP with your email address:

```sh
mailmd connect imap --email you@example.com
```

mailmd fills in the server for known providers. Then it asks for the
password in a hidden prompt, which doesn't show what you type. If your
provider needs an app password, mailmd says so and tells you where to create
one. The [IMAP guide](imap.md) lists the settings and links for common
providers.

When the login works, mailmd shows how many folders it found and where the
mail will go:

```text
Connected fastmail (you@fastmail.com).
The mailbox has 5 folders. A sync leaves out Spam and Trash.
Mail goes to ~/mail/fastmail.

Next: mailmd sync fastmail
```

The account name comes from the provider or your address. You can pick your
own with `--account`, like `--account work`. It is also the name of the
archive folder.

Other ways to connect:

- [Gmail through the Gmail API](gmail.md), with your own Google Cloud OAuth
  client.
- [Outlook.com and Microsoft 365 through Microsoft Graph](outlook.md), with
  your own app registration. Use this for Microsoft mailboxes, because most of
  them no longer accept passwords over IMAP.

To test a login without saving anything, add `--dry-run`.

## Choose where the mail goes

By default each account gets its own folder in `~/mail`. To use another
folder, give `--out` when you connect:

```sh
mailmd connect imap --email you@example.com --out ~/Documents/mail
```

mailmd saves the folder for that account. For a single sync into another
folder, use `mailmd sync --out <folder>`.

## Sync

```sh
mailmd sync
```

Without a name, mailmd syncs every connected account. In a terminal it shows
each folder and how many messages it has fetched so far. At the end it prints
one line per account:

```text
Syncing fastmail to ~/mail/fastmail
  Archive                  94 new
  INBOX                    412 new
  Sent                     118 new
  Left out: Spam and Trash. Use --folder to sync them.
Synced 624 new messages in 3 folders to ~/mail/fastmail in 12 s.
```

The first sync of a large mailbox can take a while. You can stop it with
Ctrl-C at any time. The files saved so far stay, and the next sync continues
from there.

Later syncs fetch only new mail. They also update the `flags` of a message
that you read or starred since the last run. When a message was deleted on
the server, your file stays. mailmd marks it with `deleted_on_server` and
tells you about it.

## Sync only part of your mail

| What you want | Flag |
| --- | --- |
| Only some folders | `--folder INBOX --folder 'Work/*'` |
| Leave out folders | `--exclude Newsletters` |
| Only recent mail | `--since 12m` or `--since 2025-01-01` |
| Also save the original message | `--keep-raw` |
| The text part instead of converted HTML | `--prefer-text` |

These flags work on `mailmd sync` for one run. Give them to `mailmd connect`
to save them for the account. To change them later, run the same connect
command again with the new flags.

A folder name also matches its subfolders, so `--exclude Work` also leaves
out `Work/Clients`. Names are not case sensitive. If a `--folder` matches no
folder, mailmd stops with an error that lists the folders the server has.

`--since` goes by the day the server received a message. That can be later
than the date in the message. If you moved to a new provider and copied your
old mail over, that mail can count as received on the day of the move.

Spam and trash are left out unless you name them with `--folder`. For a
Gmail address over IMAP, mailmd syncs only `[Gmail]/All Mail`. That gives you
one copy of every message, but no separate Inbox or Sent folder. The [IMAP
guide](imap.md#gmail-over-imap) shows how to sync chosen labels instead.

## See what is in your archive

```sh
mailmd status
mailmd list accounts
```

`status` shows how many messages each archive has and when the last sync
ran. With an account name, like `mailmd status fastmail`, it also lists the
folders and the messages mailmd couldn't save in full. `list accounts` shows
each account and its archive folder. Neither command connects to your
provider.

## Next steps

- [Using mailmd with an agent](agents.md) has prompts for Claude Code and
  Codex.
- [Scheduling](scheduling.md) shows how to sync every few minutes.
- [The file format](files.md) explains each field and file.
- [Troubleshooting](troubleshooting.md) covers login and connection problems.
