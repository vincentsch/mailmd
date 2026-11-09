# mailmd connect gmail

Connect Gmail through the Gmail API

## Usage

```
mailmd connect gmail
```

## Examples

```
mailmd connect gmail --client-secret ~/Downloads/client_secret.json
mailmd connect gmail --account personal --client-secret client_secret.json --no-browser
mailmd connect gmail --account personal
```

## Flags

- `--account` Name for this account, also used as its archive folder (default: gmail)
- `--client-secret` The OAuth client JSON file you downloaded from Google Cloud (not needed to connect an account again)
- `--exclude` Leave out this folder and its subfolders. mailmd saves it with the account (repeat for more)
- `--folder` Sync only this folder and its subfolders, like INBOX or 'Work/*'. mailmd saves it with the account (repeat for more)
- `--keep-raw` Also save each original message as an .eml file next to its Markdown file
- `--no-browser` Don't open a browser, only print the sign-in address
- `--out` Folder for the archive (default: ~/mail)
- `--prefer-text` Use the plain text part of a message instead of converting its HTML part
- `--since` Fetch only mail received on or after this day, like 2025-01-31, or within an age like 30d, 12m or 2y

## Output

Shows text you can read in your terminal.

Use --json to get JSON for scripts and other programs.

## Preview changes

This command makes changes. Use `--dry-run` to see what would change without changing anything.

## Related commands

- sync
- connect imap
