# mailmd status

Show what is in the archive

## Usage

```
mailmd status [account...]
```

## Examples

```
mailmd status
mailmd status work --json
```

## Flags

- `--out` Look in this folder instead of the account's saved folder

## Output

Shows text you can read in your terminal.

Use --json to get JSON for scripts and other programs.

Use --plain to get text without color or formatting.

Use --jq to filter the JSON output with a jq expression.

Use --template to format the output with a Go template.

## Related commands

- sync
- list accounts
