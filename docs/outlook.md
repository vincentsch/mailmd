# Connect Outlook and Microsoft 365

Outlook.com and most Microsoft 365 mailboxes no longer accept a password over
IMAP. The same goes for Hotmail and Live addresses. mailmd reads them through
Microsoft Graph instead, with a sign-in that only allows reading mail.

For that you need an app registration in Microsoft Entra. Entra is
Microsoft's directory of apps and users, and an app registration is the entry
that lets a program like mailmd ask for access to your mailbox. mailmd
doesn't ship a shared one, so your mailbox doesn't depend on someone else's
app. The registration is free, and you do it only once.

Button and menu names below are in quotes, the way Microsoft shows them.

## Before you start

Entra lets you register an app only inside a directory, which Microsoft also
calls a tenant. A work or school account belongs to one already. A personal
Outlook.com account usually doesn't. In that case, first create a free Azure
account at [azure.microsoft.com](https://azure.microsoft.com/free/) with the
same Microsoft account. It comes with a directory named "Default Directory",
and that is all mailmd needs.

## 1. Register an app

1. Sign in to the [Microsoft Entra admin center](https://entra.microsoft.com/).
2. In the menu, go to "Entra ID", then "App registrations", and click "New
   registration".
3. Under "Name", enter a name like `mailmd`.
4. Under "Supported account types", open the list and choose who can sign
   in:

   | Mailbox | Choose | Use with mailmd |
   | --- | --- | --- |
   | Outlook.com, Hotmail or Live | "Personal accounts only" | `--tenant consumers` |
   | Microsoft 365 at work or school, in your own organization | "Single tenant only" | `--tenant <your tenant ID>` |
   | Microsoft 365 in any organization | "Multiple Entra ID tenants" | `--tenant organizations` |
   | Both kinds | "Any Entra ID Tenant + Personal Microsoft accounts" | the default, `common` |

   Some pages still show the older, longer names, like "Personal Microsoft
   accounts only". Pick the one that means the same.

5. If the form has a "Redirect URI" field, leave it empty. mailmd signs in
   with a code, so it doesn't need one. Click "Register".
6. The app's "Overview" page opens. Copy the "Application (client) ID". For
   a single-organization app, also copy the "Directory (tenant) ID".

## 2. Allow sign-in with a code

mailmd uses the device code sign-in. It shows a code, and you enter that code
on a Microsoft page in any browser. Entra allows this only for apps that are
marked as public clients.

1. In your app registration, open "Authentication", then the "Settings" tab.
2. Set "Allow public client flows" to "Enabled".
3. Click "Save".

In the older layout of this page, the switch is under "Advanced settings"
and says "Yes".

## 3. Add the mail permission

1. Open "API permissions" and click "Add a permission".
2. Choose "Microsoft Graph", then "Delegated permissions".
3. Search for `Mail.Read`, check it and click "Add permissions".

`Mail.Read` lets mailmd read the mail of the person who signs in, and nothing
else. Users can usually agree to it themselves. Some organizations want an
administrator to approve it first. Then ask your admin to click "Grant admin
consent for" your organization on this page.

mailmd also asks Microsoft for your mail address, so that `mailmd status` can
show it. Microsoft answers only when the app also has `User.Read`, which new
app registrations get by default. Without it, mailmd shows the name you
signed in with.

## 4. Connect

```sh
mailmd connect outlook --client-id 12345678-1234-1234-1234-123456789abc
```

Add `--tenant consumers` or `--tenant <your tenant ID>` if you chose one of
those account types in step 1. mailmd then shows a code:

```text
Open https://microsoft.com/devicelogin and enter the code ABCD-EFGH to allow read-only access to your mail.
Waiting for you to sign in. Press Ctrl-C to cancel.
```

Open the page on any device, enter the code and sign in with the mailbox you
want to copy. Microsoft asks whether your app may read your mail. When you
accept, mailmd shows the mailbox's address and how many folders it found:

```text
Connected outlook (dana@contoso.com).
The mailbox has 12 folders. A sync leaves out Deleted Items and Junk Email.
Mail goes to ~/mail/outlook.

Next: mailmd sync outlook
```

Use `--account` to choose another name, like `--account work`. To check the
client ID and tenant without signing in, add `--dry-run`.

mailmd saves the client ID and the tenant with the account. To connect the
account again later, the account name is enough:

```sh
mailmd connect outlook --account work
```

A new `--client-id` or `--tenant` replaces the saved one. With a new client
ID, mailmd uses the default tenant unless you also give `--tenant`.

## How syncing works with Graph

Each Outlook folder becomes a folder in the archive. mailmd asks Graph for
the changes in each folder since the last sync. So later syncs fetch only new
mail, and they update the read and flagged marks that changed.

When you delete a message in Outlook, its file stays. mailmd adds
`deleted_on_server` to its frontmatter, and the sync tells you about it. When
you move a message to another folder, it gets a new file there, and the old
file is marked the same way.

With `--since`, mailmd still lists every message of a folder, but fetches
only those received on or after that day. Graph stops a filtered list at
5,000 messages, so mailmd does the filtering itself.

Junk Email and Deleted Items are left out unless you name them with
`--folder`. mailmd finds them by their role in Graph, so this works in every
mailbox language.

## Removing access

For a personal account, open [account.live.com/consent/Manage](https://account.live.com/consent/Manage)
and remove your app. For a work account, open [My Apps](https://myapps.microsoft.com/)
or ask your administrator. You can also delete the app registration in Entra.
Your archive stays as it is.
