# FAQ

## Why Markdown?

Markdown files are plain text. You can open them in any editor and search
them with `rg`. You can put them in Git and back them up like any other
file. Coding agents read them without any setup. The frontmatter at the top
of each file has the headers in a form that scripts can read.

## How is this different from mbsync or notmuch?

mbsync and OfflineIMAP copy your mail into a Maildir. There each message is
a raw MIME file, made for a mail program to read. mailmd writes files that
you or an agent can read directly. The body is readable text with the HTML
converted, and attachments are separate files. notmuch builds a search index
on top of a Maildir. mailmd has no search of its own. You search the files
with `rg`, or filter `index.jsonl` with `jq`.

mailmd also talks to the Gmail API and Microsoft Graph, so it works with
Outlook mailboxes that no longer allow IMAP.

## Does mailmd change anything in my mailbox?

No. mailmd opens IMAP folders read-only and downloads messages without
marking them as read. The Gmail and Microsoft logins only ask for permission
to read mail.

## Does it send my mail anywhere?

No. mailmd only talks to your mail provider, and to GitHub when you run
`mailmd update --check`. It has no telemetry. What you do with the files is up
to you. An agent on a cloud model sends what it reads to its provider.

## Where are my passwords stored?

In mailmd's config folder, in one file per account. On Linux and macOS,
only your user can read these files. They are not encrypted, so anyone who
can read your files can read them. [SECURITY.md](../SECURITY.md) has the
details. `mailmd disconnect <account>` deletes the file of one account.

## Can I sync only some of my mail?

Yes. Give the flags to `mailmd connect` with the account name, and mailmd
saves them for the account. `--exclude` leaves out a folder, and `--folder`
makes a sync copy only the folders you name. `--since` leaves out older mail,
like `--since 12m` for the last 12 months. Spam and trash are left out by
default. `--include Spam` copies the spam folder too, and the other folders
stay. On `mailmd sync`, the flags apply only to that run.
`mailmd list folders` shows which folders a sync copies. [How the folder
flags work
together](quickstart.md#how-the-folder-flags-work-together) has the
details.

## What happens when I delete a message on the server?

The file stays, because mailmd never deletes files in your archive. The
file gets `deleted_on_server` with the day in its frontmatter, and
`index.jsonl` marks it with `"gone": true`. The sync tells you how many
messages were deleted.

## Can I delete or edit files in the archive?

You can, but mailmd doesn't protect your changes. Sometimes mailmd reads a
folder again from the start, for example after the server numbered its
messages again. Then mailmd writes the messages again. A file you deleted
comes back, and a file you edited gets its original text back. If you want
to write notes about your mail, put them in another folder.

## Can I sync several accounts?

Yes. Connect each one with its own `--account` name. `mailmd sync` syncs all
of them, and `mailmd sync work` only one.

## Can I also save the original messages?

Yes, with `--keep-raw`. mailmd then saves each message it fetches as an
`.eml` file next to its Markdown file. Messages that are already in the
archive don't get one, because mailmd doesn't fetch them again. A message
that mailmd can't read always gets its `.eml` file.

## How large can a message be?

64 MiB by default. A larger message is saved with its headers only, and a
note says so. Raise the limit with `--max-message-bytes`. The next sync with
the higher limit fetches such messages again and replaces their files with
the full message.

## Why does mailmd use the HTML part of a message?

Most mail has a plain text part and an HTML part with the same text. The
HTML part has the links and tables, so mailmd converts it to Markdown. When
the conversion loses most of the words, mailmd uses the text part. To always
use the text part, give `--prefer-text` to `mailmd connect` or `mailmd
sync`.

## Does it work with Proton Mail?

Proton Mail offers IMAP only through Proton Mail Bridge, which runs on your
computer. mailmd has not been tested with it. Bridge uses a certificate that
it makes itself. mailmd accepts it when you give that certificate to
`mailmd connect imap` with `--ca-file`, or when you add it to the
certificates your system trusts.

## Does it work on Windows?

The releases have binaries for Windows on amd64 and arm64, and mailmd never
uses characters in file names that Windows doesn't allow. So far mailmd has
been used mostly on Linux. If something doesn't work on Windows, please open
an issue.

## Why do I need my own Google or Microsoft app?

A shared app for reading Gmail would need Google's security review, and
everyone who uses mailmd would depend on that one app. With your own app,
the access belongs to you alone. For Gmail you can also use IMAP with an app
password, which needs no Google Cloud setup. mailmd then syncs
`[Gmail]/All Mail`, so you get every message once, but the files don't list
Gmail's labels.

## Is there a search command or an MCP server?

No. You search the files with `rg`, and agents read them with the tools they
already have.
