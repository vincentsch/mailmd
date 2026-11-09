# Changelog

## Unreleased

First release of mailmd.

- Copy IMAP, Gmail and Outlook mailboxes into Markdown files, one file per message, with attachments next to it.
- Connect an IMAP mailbox of a known provider with only the email address, and say when the provider needs an app password.
- Connect Gmail through the Gmail API and Outlook through Microsoft Graph with read-only access.
- Trust a server's own certificate authority with `--ca-file`. mailmd saves the file with the account and uses it for every sync.
- Connect an account again with only its name. mailmd uses the saved server settings, OAuth client or client ID.
- Remove an account and its saved login with `mailmd disconnect`. The archive stays. For Gmail and Outlook, mailmd says where to remove the app's access in your Google or Microsoft account.
- Show each account's email address in `mailmd status` and `mailmd list accounts`. mailmd asks Gmail and Microsoft for it, and takes it from `--email` for IMAP.
- Sync all accounts with `mailmd sync`, fetching only new mail and updating changed flags.
- Choose folders with `--folder` and `--exclude`, and leave out older mail with `--since`.
- See the folders on the server with `mailmd list folders`, with the number of messages in each and whether a sync copies it.
- Leave out spam and trash by default. For Gmail over IMAP, sync `[Gmail]/All Mail` alone, so each message is saved once, archived mail included.
- Save messages larger than the size limit with their headers and a note, and replace them with the full message when a later sync has a higher limit.
- Save a message that can't be read with the headers that can be read and the reason. The original message goes into an `.eml` file next to it.
- When a message is deleted on the server, leave its file in place and mark it with `deleted_on_server`. The sync reports the deletions.
- Write `index.jsonl` with one line per message for scripts and agents, with the sender and the recipients, and rebuild it from the Markdown files when it is lost or damaged.
- Convert HTML mail to Markdown with its links and data tables, and drop hidden preview text. `--prefer-text` uses the plain text part instead.
- When an invite has no text, write the details of its event and its agenda into the file.
- Escape plain text, so a Markdown viewer never loads images or HTML from it.
- Join IMAP replies to their thread when References was cut short.
- Show progress in a terminal and print one summary line per account. It says how many folders the sync checked, and names them when `--folder` chose them.
- Explain errors with their cause and the next step.
- Try the output with `mailmd demo`, without an account.
- Set the config folder once with `MAILMD_CONFIG_ROOT`, for scheduled jobs and agents, instead of `--config-root` on each command.
- Run on Linux, macOS and Windows.
