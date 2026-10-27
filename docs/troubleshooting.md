# Troubleshooting

An error from mailmd says what went wrong. When mailmd knows what you can do
about it, a second line says that. This page explains the common errors in
more detail.

When an IMAP server sends an answer that mailmd can't read, add `--verbose`
to the command to see that answer. `--verbose` never prints your password or
the content of your mail.

## Logging in

**"imap.example.com rejected the login for you@example.com. The server said:
..."**

The username or password is wrong, or the provider doesn't accept your normal
password over IMAP. Gmail and iCloud, for example, need an app password, and
the [IMAP guide](imap.md#providers-mailmd-knows) has the links. Some
providers, like GMX and WEB.DE, also want you to turn on IMAP in their web
settings first. The text after "The server said" comes from your provider and
often names the exact reason.

**Outlook.com or Microsoft 365 rejects the password**

Microsoft turned off password logins over IMAP for most mailboxes. Use
`mailmd connect outlook` with the [Outlook guide](outlook.md).

**"The login of work is no longer valid."** or, during a sync, **"... rejected
the login ..."** with **"If the password changed, enter the new one."**

The saved login stopped working. Maybe you changed the password or deleted
the app password, or an OAuth login expired. Run the connect command that the
second line shows. The account name is enough:
`mailmd connect imap --account work` asks only for the new password, and for
Gmail and Outlook mailmd uses the client it saved. Your archive stays as it
is.

For Gmail through the Gmail API, this happens every 7 days while your Google
Cloud app is in testing mode. The [Gmail
guide](gmail.md#the-7-day-limit-in-testing-mode) shows how to change that.

**"Your Microsoft organization requires an administrator to approve mailmd's
read access."**

Your organization doesn't let users approve apps themselves. Ask an
administrator to open your app registration in Entra and click "Grant admin
consent" under "API permissions".

**Outlook: "The sign-in did not finish: Microsoft asks for a client secret
..."**

Your app registration doesn't allow sign-in with a code. In Entra, open
"Authentication" and set "Allow public client flows" to "Enabled", as in
[step 2 of the Outlook guide](outlook.md#2-allow-sign-in-with-a-code).

**Outlook: "The sign-in did not finish: Microsoft can't find an app with this
client ID ..."**

The client ID has a typo, or the `--tenant` doesn't fit the account types you
chose for the app registration. The table in [step 1 of the Outlook
guide](outlook.md#1-register-an-app) shows which `--tenant` goes with which
choice.

**Gmail: "The client file is for a Web application ..."**

You created the OAuth client with the wrong type. Create a new one of the
type "Desktop app", as in the [Gmail guide](gmail.md#4-create-the-oauth-client).

**Gmail: "The client file has no client_secret."**

Google shows the client secret only once, in the dialog right after you
create the client. A JSON file that you download later doesn't have it.
Create a new client of the type "Desktop app" and click "Download JSON" in
that dialog.

**Gmail: "Cannot use the OAuth client saved for personal, because ..."**

mailmd can't read the client from the account's login file, for example
because the file is damaged. Connect again with the client JSON file:
`mailmd connect gmail --account personal --client-secret client_secret.json`.
If you don't have the file any more, create a new client as in the [Gmail
guide](gmail.md#4-create-the-oauth-client).

**Gmail: the browser shows "Access blocked" or "app has not completed the
Google verification process"**

The Google account you signed in with is not a test user of your app. Add it
in the Google Auth Platform: open "Audience", then click "Add users" under
"Test users".

**Gmail on a computer without a browser**

Use `--no-browser`. mailmd prints the sign-in address. After you allow access
on another device, you paste the address of the page you end up on. The
[Gmail guide](gmail.md#on-a-server-or-another-computer-without-a-browser)
explains it.

## Connecting to the server

**"Cannot find the server imap.example.com. The DNS lookup failed."**

The host name has a typo, or the computer is offline.

**"imap.example.com:993 refused the connection."** or **"... did not answer
within 30s."**

The port is wrong, or a firewall blocks it. IMAP with implicit TLS uses port
993. STARTTLS uses port 143 and needs `--tls starttls`.

**"... did not answer with TLS."** or **"The TLS handshake ... timed out."**

You connected to a STARTTLS port with implicit TLS. Use `--tls starttls
--port 143`.

**"... did not send an IMAP greeting."**

It is the other way round. You used `--tls starttls` on a port that expects
implicit TLS, or the port doesn't belong to an IMAP server. Use port 993
without `--tls`.

**"The TLS certificate of mail.example.com is not trusted."**

The server uses a certificate that your system doesn't trust, like one that
the server signed itself. mailmd has no option to turn this check off. Add the
server's CA certificate to the certificates your system trusts. On Linux you
can also point the `SSL_CERT_FILE` environment variable to a file that holds
it.

**"The TLS certificate of mail.example.com is for a different host name."**

Use the host name that the certificate was made for. Your provider's help
pages usually name it.

## During a sync

**The sync stops with "... could not be reached."**

mailmd tries again for a while when the connection drops. If the server
stays away, the sync stops. Everything saved so far stays, and the next sync
continues from there.

**"The folder Archive could not be synced: ..."**

One folder failed, and the others were synced. The reason after the colon
comes from the server. The next sync tries the folder again.

**"Another mailmd sync of this archive is running."**

Two syncs can't work on the same archive at the same time. Wait until the
other one finishes. If you run syncs on a schedule, choose a longer interval.

**"3 messages were larger than the size limit, so mailmd saved only the
headers."**

Each of those messages has a file with its headers and a note.
`mailmd status work` lists them. To save them in full, sync with a limit
above their size:

```sh
mailmd sync --max-message-bytes 300000000
```

mailmd fetches them again and replaces each headers-only file with the full
message.

**"2 messages could not be read. mailmd saved the headers it could read, and
the original as an .eml file."**

The server sent messages that mailmd couldn't parse, like a message that is
cut off. Each of them has a file with the headers mailmd could read and
`parse_error` with the reason. The original message is next to it.
`mailmd status work` lists their files. If you can, please open an issue with
the headers of such a message. Remove anything private first.

**"--folder "Inbx" matches no folder of work."**

No folder has that name. The second line lists the folders the server has.
Folder names don't depend on case, and a name also matches its subfolders.
When `connect imap` says this, it saved nothing, so run it again with the
right name. `connect gmail` and `connect outlook` save the login first, so
you don't have to sign in again. Their message says so.

**"The folder Newsletters is no longer on the server. Its files stay in the
archive."**

The folder was renamed or deleted on the server. mailmd says this once.
After that, `mailmd status` lists the folder as gone. A renamed folder is
synced under its new name.

**"The sync state file .mailmd/state.json in the archive is damaged."** or
**"The saved position of this folder is damaged."**

Move the file out of the `.mailmd` folder and sync again. mailmd then reads
all folders again, but it doesn't write the messages you already have a
second time.

**index.jsonl looks wrong, or you deleted it**

The next sync builds it again from the Markdown files, which hold every field
of the index. A line that a crash cut short is repaired the same way.

**"Other users on this computer can read the saved login of work."**

mailmd refuses to use a login file that other users can read. Run
`chmod 600` on the files in mailmd's config folder, or connect the account
again.

**"The saved login of work can't be used ..."**

The login file in mailmd's config folder is damaged. Connect the account
again with the command that the second line shows.

## Finding the files

**Where is the config folder?**

| System | Folder |
| --- | --- |
| Linux | `~/.config/mailmd`, or `$XDG_CONFIG_HOME/mailmd` |
| macOS | `~/Library/Application Support/mailmd` |
| Windows | `%AppData%\mailmd` |

`--config-root` chooses another folder for one command. mailmd puts its files
in a `mailmd` folder inside it, so `--config-root ~/.config` and
`--config-root ~/.config/mailmd` both find the files above.

**Where is my mail?**

`mailmd list accounts` shows the archive folder of each account.

## Still stuck

Run the command again with `--verbose` and open an issue at
[github.com/vincentsch/mailmd/issues](https://github.com/vincentsch/mailmd/issues).
Before you post the output, check it for names and addresses that you don't
want to share.
