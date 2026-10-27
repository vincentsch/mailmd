# Security

## Reporting a problem

Please report security problems through [GitHub private vulnerability
reporting](https://github.com/vincentsch/mailmd/security/advisories/new), not
in a public issue. Tell us the mailmd version and your operating system, and
describe the steps that show the problem. Leave out real passwords and real
mail, and use made-up examples instead.

## What mailmd stores, and where

mailmd writes two kinds of files.

### The archive

The archive holds your mail as Markdown files, by default in `~/mail`. On
Linux and macOS, mailmd makes each folder and file below that root private
to your user (mode 0700 for folders, 0600 for files). If the root folder
already exists, mailmd doesn't change its permissions. The root can be a
symbolic link, for example to another disk, but mailmd doesn't follow
symbolic links inside it. If `index.jsonl` is a symbolic link, the sync
stops, so it can't write mail details somewhere else.

mailmd never deletes a file in the archive. Files you put next to a message,
like notes in its attachments folder, stay where they are.

### The config folder

The config folder holds the list of accounts and the saved logins:

| System | Config folder |
| --- | --- |
| Linux | `~/.config/mailmd`, or `$XDG_CONFIG_HOME/mailmd` |
| macOS | `~/Library/Application Support/mailmd` |
| Windows | `%AppData%\mailmd` |

Each account has a folder there with one login file:

| Account type | File | What it holds |
| --- | --- | --- |
| IMAP | `imap.json` | Your IMAP password |
| Gmail API | `gmail-token.json` | Your OAuth client ID and secret, and a refresh token for read-only Gmail access |
| Outlook | `graph-token.json` | A refresh token for the `Mail.Read` permission |

**These files are not encrypted.** They are plain JSON. On Linux and macOS,
mailmd creates them so that only your user can read them (mode 0600, in
folders with mode 0700). It refuses to use a login file that other users can
read. On Windows, the files get the default permissions of your user folder.
Anyone who can read your files can read the logins, like a program that runs
as your user or someone with your backup.

mailmd doesn't use the system keychain yet. If that matters to you:

- Use an app password for IMAP, not your main password. You can revoke it at
  any time without changing your main password.
- For Gmail and Outlook, the stored token only allows reading mail. You can
  revoke it in your Google or Microsoft account.
- Don't put the config folder into backups that other people can read.

## What mailmd sends over the network

mailmd connects to your mail provider over TLS and checks the server's
certificate. It has no option to turn that check off. The only other
connection is `mailmd update --check`, which asks GitHub for the newest
release. There is no telemetry.

## What mailmd does with your mailbox

It only reads. mailmd opens IMAP folders read-only and fetches messages
without marking them as read. The Gmail login asks only for
`gmail.readonly`, and the Microsoft login only for `Mail.Read`.

## Mail content

mailmd converts HTML mail to Markdown and removes scripts and remote images.
So when you open a converted message in a Markdown viewer, nothing is loaded
from the sender's servers. Plain text mail can contain text that a Markdown
viewer would show as HTML or as an image, like `<img src=...>`. mailmd adds a
backslash in front of such text, so the viewer shows it as text. Attachments
and the `.eml` files of `--keep-raw` are saved as they are. Treat them like
any other email attachment.

Folder and file names come from the mail, so mailmd cleans them. A subject or
a folder name can't place a file outside the archive or make a hidden file.
