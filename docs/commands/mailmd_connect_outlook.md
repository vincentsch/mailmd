# mailmd connect outlook

Connect Outlook or Microsoft 365

## Usage

```
mailmd connect outlook
```

## Examples

```
mailmd connect outlook --client-id 12345678-1234-1234-1234-123456789abc
mailmd connect outlook --account work --client-id 12345678-1234-1234-1234-123456789abc --tenant organizations
mailmd connect outlook --account work
```

## Flags

- `--account` Name for this account, also used as its archive folder (default: outlook)
- `--client-id` The application (client) ID of your Microsoft app registration (not needed to connect an account again)
- `--exclude` Leave out this folder and its subfolders. mailmd saves it with the account (repeat for more)
- `--folder` Sync only this folder and its subfolders, like INBOX or 'Work/*', instead of the default folders. mailmd saves it with the account (repeat for more)
- `--include` Also sync this folder, like Junk, and keep the default folders. mailmd saves it with the account (repeat for more)
- `--keep-raw` Also save each original message as an .eml file next to its Markdown file
- `--out` Folder for the archive (default: ~/mail). A new folder doesn't move the mail that is already archived
- `--prefer-text` Use the plain text part of a message instead of converting its HTML part
- `--since` Fetch only mail received on or after this day, like 2025-01-31, or within an age like 30d, 12m or 2y
- `--tenant` Who can sign in: common, organizations, consumers, or your tenant ID

## Output

Shows text you can read in your terminal.

Use --json to get JSON for scripts and other programs.

## Preview changes

This command changes things. Add `--dry-run` to see what it would change; nothing changes then.

## Related commands

- sync
- connect imap
