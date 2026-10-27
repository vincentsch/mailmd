# mailmd sync

Copy new mail into the archive

## Usage

```
mailmd sync [account...]
```

## Examples

```
mailmd sync
mailmd sync work
mailmd sync work --since 12m --exclude Newsletters
mailmd sync --dry-run
mailmd sync --quiet
```

## Flags

- `--exclude` Leave out this folder and its subfolders (repeat for more)
- `--folder` Sync only this folder and its subfolders, like INBOX or 'Work/*' (repeat for more)
- `--keep-raw` Also save each original message as an .eml file next to its Markdown file
- `--max-message-bytes` Save only the headers of a message larger than this many bytes
- `--out` Write to this folder for this run instead of the account's saved folder
- `--prefer-text` Use the plain text part of a message instead of converting its HTML part
- `--since` Fetch only mail received on or after this day, like 2025-01-31, or within an age like 30d, 12m or 2y

## Output

Shows text you can read in your terminal.

Use --json to get JSON for scripts and other programs.

Use --plain to get text without color or formatting.

Use --jq to filter the JSON output with a jq expression.

Use --template to format the output with a Go template.

## Preview changes

This command makes changes. Use `--dry-run` to see what would change without changing anything.

## Related commands

- status
- connect
