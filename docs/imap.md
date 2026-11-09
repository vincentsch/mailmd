# Connect with IMAP

IMAP is the protocol mail programs use to read a mailbox. Almost every mail
provider offers it, and so does your own mail server if you run one. mailmd
opens each folder read-only and downloads messages without marking them as
read.

```sh
mailmd connect imap --email you@example.com
```

For the providers in the table below, the email address is enough. mailmd
fills in the server and tells you which password to use. For other providers,
it asks for the server in the terminal, or you give it with `--host`.

## Providers mailmd knows

All of them use port 993 with TLS.

| Provider | Addresses | Server | Password |
| --- | --- | --- | --- |
| Gmail | gmail.com, googlemail.com | imap.gmail.com | App password. Turn on 2-Step Verification, then create one at [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords). |
| iCloud Mail | icloud.com, me.com, mac.com | imap.mail.me.com | App-specific password from [account.apple.com](https://account.apple.com), under "Sign-In and Security". |
| Fastmail | fastmail.com, fastmail.fm | imap.fastmail.com | App password from Fastmail's settings, under "Privacy & Security". |
| Yahoo Mail | yahoo.com, ymail.com and Yahoo's country domains | imap.mail.yahoo.com | App password from "Account security", then "Generate app password". |
| AOL Mail | aol.com | imap.aol.com | App password from "Account security", then "Generate app password". |
| GMX | gmx.de, gmx.net, gmx.at, gmx.ch | imap.gmx.net | Your GMX password. Turn on IMAP first in the web mail settings, under "POP3/IMAP". |
| GMX | gmx.com | imap.gmx.com | Same as above. |
| WEB.DE | web.de | imap.web.de | Your WEB.DE password. Turn on IMAP first in the web mail settings, under "POP3/IMAP". |
| Posteo | posteo.de and Posteo's other domains | posteo.de | Your Posteo password. |
| mailbox.org | mailbox.org | imap.mailbox.org | Your password, or an app password if you use two-factor login. |
| Zoho Mail | zoho.com, zohomail.com | imap.zoho.com | Turn on IMAP access in the settings first. With two-factor login you need an app password. |
| Zoho Mail (EU) | zoho.eu, zohomail.eu | imap.zoho.eu | Same as above. |
| Telekom Mail | t-online.de | secureimap.t-online.de | The email password from the Telekom mail settings. It is not your Telekom login password. |
| Yandex Mail | yandex.com, yandex.ru | imap.yandex.com | App password from your Yandex ID security settings. |

An app password is a separate password that you create in your account
settings for one program. You can delete it again at any time, and your
normal password stays the same.

Outlook.com addresses, including Hotmail and Live, no longer accept
passwords over IMAP. mailmd stops and points you to `mailmd connect outlook`,
which uses Microsoft Graph. The [Outlook guide](outlook.md) shows the setup.

If your address is on your own domain but your mail is at one of these
providers, give the server yourself, like `--host imap.fastmail.com`. mailmd
then shows the password hint for that provider.

## Your own server

```sh
mailmd connect imap --account work --email you@example.com --host mail.example.com
```

| Flag | Default | Notes |
| --- | --- | --- |
| `--email` | | Your address. `mailmd status` shows it |
| `--host` | from the address, for known providers | The IMAP server name |
| `--port` | 993, or 143 with STARTTLS | |
| `--tls` | `implicit` | Use `starttls` for servers on port 143 that upgrade the connection |
| `--username` | your email address | Some servers want only the part before the @ |
| `--account` | from the provider or the domain | Also the name of the archive folder |
| `--ca-file` | | A PEM file with the certificate of your server's own certificate authority |

mailmd always encrypts the connection. It checks the server's certificate
against the certificates your system trusts, and there is no option to turn
that off. It never sends a password over a connection that isn't encrypted.

### A server with its own certificate authority

Many companies sign the certificates of their servers with their own
certificate authority (CA). Your system doesn't know that CA, so the check
fails with "The TLS certificate of mail.example.com is not trusted". Give
mailmd the CA's certificate as a PEM file:

```sh
mailmd connect imap --account work --email you@example.com --host mail.example.com --ca-file ~/company-ca.pem
```

mailmd then trusts that CA for this account, in addition to the
certificates your system trusts. It saves the path of the file with the
account and reads the file on every sync. So you don't have to change your
system or set an environment variable, and your other accounts aren't
affected. Leave the file where it is. If the server signed its certificate
itself, give that certificate as the CA file.

A PEM file starts with `-----BEGIN CERTIFICATE-----`. If you got a binary
`.cer` or `.der` file, convert it first:

```sh
openssl x509 -inform der -in company-ca.cer -out company-ca.pem
```

## How mailmd gets the password

mailmd never takes the password as a flag, because flags end up in your shell
history. There are three ways to give it:

- In a terminal, mailmd asks for it in a hidden prompt.
- `--password-stdin` reads the first line of standard input. This works well
  with a password manager, like
  `pass show mail/work | mailmd connect imap --email you@example.com --password-stdin`.
- The `MAILMD_IMAP_PASSWORD` environment variable.

mailmd saves the password in its config folder for later syncs. The file is
not encrypted. On Linux and macOS, only your user can read it.
[SECURITY.md](../SECURITY.md) has the details.

## Test before saving

```sh
mailmd connect imap --email you@example.com --dry-run
```

This checks the login and shows what a sync would leave out. It saves
nothing.

## Gmail over IMAP

Gmail shows each label as an IMAP folder, so a message with two labels is in
two folders. Mail you archived has no label and is only in
`[Gmail]/All Mail`, which has every message once. So for Gmail, mailmd syncs
`[Gmail]/All Mail` alone by default. This means:

- You get one copy of every message, archived mail included, in
  `~/mail/<account>/[Gmail]/All Mail/`.
- There is no Inbox or Sent folder in the archive. Mail you received and mail
  you sent are in the same folder. The `from:` line of a file tells you which
  messages you sent.
- The files don't say which labels a message has. Gmail's IMAP server shows
  labels only through an extension that mailmd doesn't use.
- Spam and trash are not in All Mail. mailmd leaves them out, as it does for
  every other provider.

If you want the labels, use [the Gmail API](gmail.md) instead. It also saves
each message once, in a folder like Inbox or Sent, and lists all its labels
in the file.

To sync chosen labels instead of All Mail, name them with `--folder`:

```sh
mailmd connect imap --email you@gmail.com --folder INBOX --folder '[Gmail]/Sent Mail' --folder Work
```

Each label is then synced as its own folder. A message with two of these
labels, like a message in your inbox that also has the label Work, is saved
twice, once in each folder. Mail you archived without one of these labels is
not synced at all.

## Changing settings later

To change a setting, run `mailmd connect imap` with the account name and
the new setting:

```sh
mailmd connect imap --account work --exclude Newsletters
```

mailmd keeps the saved password and doesn't ask for it, also with
`--dry-run`. It logs in with the saved password to check the new settings,
then saves them. Settings you don't give stay as they are, and so do your
archive and its sync state.

mailmd needs the password again only when you give another `--host` or
`--username`, because that is a new login. It checks the new login before it
replaces the old one.

For a new password, give only the account name. mailmd takes the server,
the login name and the CA file from the saved account and asks for the
password:

```sh
mailmd connect imap --account work
```

When the server no longer accepts the saved password, a connect that
changes settings says so and asks for the new password. A script then gives
it with `--password-stdin`.

To use another CA file, add `--ca-file` with the new file. To stop using
one, add `--ca-file ''`.
