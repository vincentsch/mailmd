# mailmd command reference

Copy your mailbox into Markdown files

## Commands

- [mailmd connect](mailmd_connect.md) - Connect a mailbox
- [mailmd connect gmail](mailmd_connect_gmail.md) - Connect Gmail through the Gmail API
- [mailmd connect imap](mailmd_connect_imap.md) - Connect an IMAP mailbox
- [mailmd connect outlook](mailmd_connect_outlook.md) - Connect Outlook or Microsoft 365
- [mailmd demo](mailmd_demo.md) - Write a sample archive without any account
- [mailmd disconnect](mailmd_disconnect.md) - Remove an account and its saved login
- [mailmd list](mailmd_list.md) - List parts of your mailmd setup
- [mailmd list accounts](mailmd_list_accounts.md) - List connected accounts
- [mailmd status](mailmd_status.md) - Show what is in the archive
- [mailmd sync](mailmd_sync.md) - Copy new mail into the archive
- [mailmd update](mailmd_update.md) - Check for a newer version

## Global flags

- `--config-root` Folder for mailmd's settings and logins (default: your user config folder)
- `--dry-run` Preview changes without performing them
- `--include-meta` Wrap machine output as {data, meta} (commands that expose request metadata)
- `--jq` Transform stable JSON output with a jq expression (commands with machine output)
- `--json` Output stable JSON instead of the human view
- `--no-ansi` Disable all ANSI/control sequences in human output
- `--no-color` Disable color in human output
- `--no-pager` Never use a pager for long human output
- `--no-prompt` Never block on an interactive prompt
- `--plain` Print unstyled, copy-safe text (commands with human output)
- `--quiet` Suppress non-essential output
- `--template` Render stable JSON output through a Go text/template (commands with machine output)
- `--verbose` Show the details when mailmd can't read an IMAP server's answer
