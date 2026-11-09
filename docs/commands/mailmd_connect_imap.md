# mailmd connect imap

Connect an IMAP mailbox

## Usage

```
mailmd connect imap
```

## Examples

```
mailmd connect imap --email you@fastmail.com
mailmd connect imap --account work --host imap.example.com --username you@example.com
printf '%s\n' "$PASSWORD" | mailmd connect imap --email you@example.com --host imap.example.com --password-stdin
mailmd connect imap --account work
```

## Flags

- `--account` Name for this account, also used as its archive folder (default: from the provider or address)
- `--ca-file` PEM file with the certificate of your server's own certificate authority. mailmd trusts it in addition to the system's certificates and saves it with the account
- `--email` Your email address. mailmd fills in the server settings of known providers
- `--exclude` Leave out this folder and its subfolders. mailmd saves it with the account (repeat for more)
- `--folder` Sync only this folder and its subfolders, like INBOX or 'Work/*'. mailmd saves it with the account (repeat for more)
- `--host` IMAP server, like imap.example.com
- `--keep-raw` Also save each original message as an .eml file next to its Markdown file
- `--out` Folder for the archive (default: ~/mail)
- `--password-stdin` Read the password from the first line of standard input
- `--port` IMAP port (default: 993, or 143 with --tls starttls)
- `--prefer-text` Use the plain text part of a message instead of converting its HTML part
- `--since` Fetch only mail received on or after this day, like 2025-01-31, or within an age like 30d, 12m or 2y
- `--tls` How to encrypt the connection: implicit or starttls
- `--username` Login name (default: the email address)

## Output

Shows text you can read in your terminal.

Use --json to get JSON for scripts and other programs.

## Preview changes

This command makes changes. Use `--dry-run` to see what would change without changing anything.

## Related commands

- sync
- list accounts
