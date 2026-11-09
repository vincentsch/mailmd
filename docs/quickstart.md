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
The mailbox has 5 folders. A sync copies 3 of them and leaves out Spam and Trash.
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

`--out` is the folder that holds the account folders. mailmd puts each
account in a folder of its own inside it, so the mail of an account named
`work` goes to `~/Documents/mail/work`. mailmd saves the folder for that
account. For a single sync into another folder, use
`mailmd sync --out <folder>`.

## Moving the archive

mailmd doesn't move any files when you change `--out` later. You move the
account's folder yourself, then tell mailmd the new place. Say your archive
is in `~/mail/work`, and you want it in `~/Documents/mail/work`:

1. Make sure no sync runs. If you sync on a schedule, pause the job.
2. Move the account's folder. A normal move takes the hidden `.mailmd`
   folder with it, which holds the position of the last sync:

   ```sh
   mkdir -p ~/Documents/mail
   mv ~/mail/work ~/Documents/mail/
   ```

   On Windows, move the folder in Explorer, or with `Move-Item` in
   PowerShell.
3. Save the new place. `--out` is the folder that holds the account
   folders, so mailmd adds `work` itself:

   ```sh
   mailmd connect imap --account work --out ~/Documents/mail
   ```

   mailmd keeps the saved password and your other settings. For Gmail and
   Outlook, use `mailmd connect gmail` or `mailmd connect outlook` with the
   account name. They keep the saved login and don't sign in again.
4. Run `mailmd sync work`. It says that `work` is up to date, or it fetches
   only the mail that is new since the last sync.

If you change `--out` first, mailmd says that the archived mail stays in the
old folder, and you move it before the next sync. A sync that runs before
the move starts a new archive in the new place and downloads the mail
again. Nothing is lost then, but you have two copies.

The config folder isn't part of the archive, so it stays where it is. The
same goes for a CA file that you gave with `--ca-file`. If you move that
file, connect again with `--ca-file` and the new path. Also change the path
in your scheduled job and in your agent instructions, if they name the
folder.

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
  Left out: Spam and Trash. To copy one of them, give it to mailmd connect with --include.
mailmd checked 3 folders and saved 624 new messages to ~/mail/fastmail in 12 s.
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
| Also copy a folder that is left out by default, like Junk | `--include Junk` |
| Only some folders | `--folder INBOX --folder 'Work/*'` |
| Leave out folders | `--exclude Newsletters` |
| Only recent mail | `--since 12m` or `--since 2025-01-01` |
| Also save the original message | `--keep-raw` |
| The text part instead of converted HTML | `--prefer-text` |

Give these flags to `mailmd connect` to save them for the account. You can
do that when you first connect, or later with the account name. mailmd
then keeps the saved password or login, so it doesn't ask for it:

```sh
mailmd connect imap --account fastmail --include Spam
```

```text
Changed the settings of fastmail (you@fastmail.com) and kept its saved password.
The mailbox has 5 folders. A sync copies 4 of them and leaves out Trash.
Mail goes to ~/mail/fastmail.
```

`mailmd sync` takes the same flags, except `--include`, for one run. mailmd
doesn't save them. On `sync`, `--folder` and `--exclude` replace the folder
settings saved with the account for that run. So
`mailmd sync fastmail --folder Spam` copies only the spam folder, once, and
the next normal sync leaves it out again. Add `--dry-run` to see first how
many new messages a sync would save. A dry run writes nothing.

To see the folders on the server, run `mailmd list folders fastmail`. It
shows how many messages each folder has and whether a sync copies it. For a
folder that a sync leaves out, it also says why.

`--since` goes by the day the server received a message. That can be later
than the date in the message. If you moved to a new provider and copied your
old mail over, that mail can count as received on the day of the move.

### How the folder flags work together

Without folder settings, a sync copies every folder except spam and trash.
For a Gmail address over IMAP, mailmd syncs only `[Gmail]/All Mail`. That
gives you one copy of every message, but no separate Inbox or Sent folder.
The [IMAP guide](imap.md#gmail-over-imap) shows how to sync chosen labels
instead.

The three folder flags change that:

- `--include Junk` copies Junk in addition to the folders a sync copies
  anyway. Use it when you want one more folder.
- `--folder INBOX` copies only the folders you name, and the ones that
  `--include` adds. On `mailmd connect` this means that from now on a sync
  copies only these folders, and folders you create later on the server are
  left out. A folder you name is copied even when it is left out by default,
  like Junk.
- `--exclude Newsletters` leaves out a folder. It wins over `--folder` and
  `--include`.

You can repeat each flag for more folders. On `mailmd connect`, each flag
replaces the list that is saved for it and keeps the others. So
`--include Junk` keeps a saved `--exclude Newsletters`. To clear a saved
list, give the flag an empty value, like `--folder ''`. With
`--folder '' --include '' --exclude ''`, a sync copies the default folders
again.

This is how mailmd matches folder names:

- A name matches the folder and all its subfolders. So `--exclude Work` also
  leaves out `Work/Clients`. Names are not case sensitive.
- `*` stands for any part of a name, but not for the `/` between a folder
  and its subfolder. So `'Work/*'` matches `Work/Clients` and its
  subfolders, but not `Work` itself. `--folder Work` copies Work and all its
  subfolders.
- `'*'` matches every folder. `--folder '*' --exclude Trash` copies all
  folders except Trash, also Junk and folders you create later.
- Put a name with `*` in single quotes, so that your shell doesn't change
  it.

If a `--folder` or `--include` matches no folder, mailmd stops with an error
that lists the folders the server has. On `connect`, it saves nothing then.

## See what is in your archive

```sh
mailmd status
mailmd list accounts
```

`status` shows how many messages each archive has and when the last sync
ran. With an account name, like `mailmd status fastmail`, it also lists the
folders and the messages mailmd couldn't save in full. `list accounts` shows
each account and its archive folder. Neither command connects to your
provider. `mailmd list folders fastmail` does, to show the folders as the
server has them.

## Remove an account

```sh
mailmd disconnect fastmail --dry-run
mailmd disconnect fastmail
```

`disconnect` removes the account from mailmd and deletes its saved login.
The archive stays as it is. With `--dry-run`, mailmd shows the files it
would delete and changes nothing.

For Gmail and Outlook, your Google or Microsoft account still lists the app
you signed in with. mailmd prints the page where you can remove it. The
[Gmail guide](gmail.md#removing-access) and the
[Outlook guide](outlook.md#removing-access) have the details.

## Next steps

- [Using mailmd with an agent](agents.md) has prompts for Claude Code and
  Codex.
- [Scheduling](scheduling.md) shows how to sync every few minutes.
- [The file format](files.md) explains each field and file.
- [Troubleshooting](troubleshooting.md) covers login and connection problems.
