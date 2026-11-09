# Connect Gmail with the Gmail API

There are two ways mailmd can read Gmail.

With IMAP and an app password, the setup is quicker. The [IMAP
guide](imap.md#gmail-over-imap) covers it. mailmd then syncs
`[Gmail]/All Mail`. Each message is saved once, but all of them are in one
folder, without an Inbox or Sent split. The files also don't list Gmail's
labels.

With the Gmail API, you first set up a project in Google Cloud. You do this
only once, and this page shows each step. mailmd then saves each message
once, in the folder of its most important label, and lists all its labels in
the file.

mailmd doesn't come with a Google sign-in of its own. A shared app would need
Google's security review before it may read whole mailboxes, and everybody's
access would depend on that one app. So you create your own OAuth client in
your own Google Cloud project. An OAuth client is the entry that lets a
program ask for access to your Google account. It is free, and only you use
it.

Button and menu names below are in quotes, the way Google shows them.

## 1. Create a Google Cloud project

1. Open the [Google Cloud console](https://console.cloud.google.com/) and sign
   in with any Google account. It doesn't have to be the Gmail account you
   want to copy.
2. Click the project picker at the top of the page, then "New project".
3. Enter a name like `mailmd` and click "Create".
4. When the project is ready, select it in the project picker.

## 2. Turn on the Gmail API

1. Open the [Gmail API page](https://console.cloud.google.com/apis/library/gmail.googleapis.com)
   in the API Library.
2. Check that your new project is selected at the top.
3. Click "Enable".

## 3. Set up the consent screen

The consent screen is the page Google shows when you sign in to your app
later.

1. Open "Google Auth Platform" from the menu, or go to
   [console.cloud.google.com/auth/overview](https://console.cloud.google.com/auth/overview).
   Click "Get started".
2. Under "App Information", enter an app name like `mailmd`. Choose your own
   email address as the "User support email". Click "Next".
3. Under "Audience", choose "External" and click "Next". If your Gmail
   account belongs to a Google Workspace organization and you created the
   project in that organization, you can choose "Internal" instead. Internal
   apps have no 7-day limit (see below).
4. Under "Contact Information", enter your email address and click "Next".
5. Under "Finish", check "I agree to the Google API Services: User Data
   Policy". Click "Continue", then "Create".
6. Open "Audience" in the menu on the left. Under "Test users", click "Add
   users", enter the Gmail address you want to copy and click "Save". While
   the app is in testing, only test users can sign in.
7. Open "Data Access" and click "Add or remove scopes". Find the Gmail API
   scope `.../auth/gmail.readonly` in the list and check it. If it isn't
   there, paste `https://www.googleapis.com/auth/gmail.readonly` into
   "Manually add scopes" and click "Add to table". Click "Update", then
   "Save" at the bottom of the page.

`gmail.readonly` is the only access mailmd asks for. It lets mailmd read
your mail and nothing else.

## 4. Create the OAuth client

1. Open "Clients" in the menu on the left and click "Create client".
2. Under "Application type", choose "Desktop app". Other types don't work
   with mailmd, and mailmd tells you if you picked the wrong one.
3. Enter a name and click "Create".
4. The "OAuth client created" dialog opens. Click "Download JSON" in this
   dialog. The file has a name like
   `client_secret_1234-abc.apps.googleusercontent.com.json`.

Google shows the full client secret only this once. Later the console shows
only its last four characters, and a JSON file you download then has no
secret, which mailmd can't use. If you closed the dialog too early, create
another Desktop app client and download its JSON in the dialog.

Don't share this file. Anyone who has it can ask your Google users to sign in
to your app.

## 5. Connect

```sh
mailmd connect gmail --client-secret ~/Downloads/client_secret_1234-abc.apps.googleusercontent.com.json
```

mailmd prints the Google sign-in address and opens it in your browser. Pick
the Gmail account you added as a test user.

Google then shows "Google hasn't verified this app". That is expected,
because the app is your own. Click "Continue". If you see an "Advanced" link
instead, click it, then "Go to mailmd (unsafe)". Google shows your app's name
there. Next, allow your app to read your email.

The browser then says that the sign-in is complete, and mailmd shows your
Gmail address and how many labels it found:

```text
Connected gmail (dana@gmail.com).
The mailbox has 14 folders. A sync copies 11 of them and leaves out Important, Spam and Trash.
Mail goes to ~/mail/gmail.

Next: mailmd sync gmail
```

Use `--account` to choose another name, like `--account personal`.

mailmd saves the client ID and secret with the login, in its config folder,
so you can delete the downloaded file. To connect the account again later,
for example after the 7-day limit below, the account name is enough:

```sh
mailmd connect gmail --account personal
```

A new file that you give with `--client-secret` replaces the saved client.

To change a setting, like `--out` or `--folder`, give the account name and
the new setting. mailmd keeps the saved login and doesn't open the sign-in
page:

```sh
mailmd connect gmail --account personal --exclude Newsletters
```

If Google ended the login in the meantime, mailmd signs in again and then
saves the new settings.

### On a server or another computer without a browser

```sh
mailmd connect gmail --client-secret client_secret.json --no-browser
```

mailmd prints the sign-in address. Open it on any device, sign in and allow
access. At the end, the browser tries to load a page on `127.0.0.1` and shows
an error, because that address points to the device you are on. Copy the
whole address from the address bar and paste it into mailmd.

When `connect gmail` doesn't run in a terminal, for example when a script
starts it, there is no prompt to paste into. mailmd then prints the address
and waits up to 10 minutes for you to allow access in a browser on the same
computer. After that, it stops with an error.

## The 7-day limit in testing mode

While your app's "Publishing status" is "Testing", Google ends the login
after 7 days. After that, `mailmd sync` fails with a login error, and you
have to run `mailmd connect gmail` again.

To avoid that, open "Audience" in the Google Auth Platform and click
"Publish app". Google may say that your app needs verification. You don't
need it for your own use. Google lets an unverified app sign in up to 100
users, and you see the warning from step 5 each time you sign in. After
publishing, run `mailmd connect gmail` once more to get a login without the
7-day limit.

Internal apps in a Google Workspace organization don't have this limit.

Google also ends a login in these cases, and then you connect again:

- You change your Google password. This happens to every app that reads
  Gmail.
- The login was not used for six months.
- You remove the app's access in your Google account.

Google deletes an OAuth client that nobody used for six months. Then create a
new one as in step 4, and connect with its file and `--client-secret`.

## How labels become folders

mailmd puts each message in one folder, picked in this order:

1. Inbox
2. Sent
3. Drafts
4. Spam
5. Trash
6. Your own labels
7. Gmail categories, like `Categories/Promotions`
8. `Archive`, for mail without any of the above

The file lists all labels:

```yaml
folder: "Inbox"
labels: ["Categories/Updates", "Important", "Inbox", "Work/Clients"]
flags: ["seen"]
```

Spam and trash are left out by default. Important is never used as a folder,
because Gmail adds it to mail that already has another label.

`--exclude` and `--folder` work on labels. A message is left out when one of
its labels is excluded. `--folder Work` syncs only messages with the label
`Work` or one of its sublabels, and `--folder Archive` syncs the messages
without any label. When you change these filters, the next sync goes through
all messages again, so it finds the ones the old filter left out. Messages
you already have are not written again.

A message stays in the folder where mailmd first saved it. When its labels
change later, for example because you archived it, mailmd updates the labels
in the file but doesn't move the file.

The first sync lists all messages and then fetches each one. Later syncs ask
Gmail for the changes since the last sync. They fetch only new mail and
update labels and flags that changed.

## Removing access

To remove the account from mailmd, run `mailmd disconnect <account>`. It
deletes the saved login on your computer. Your archive stays as it is.

Google still lists your app as allowed after that. To take its access away,
open [Linked apps](https://myaccount.google.com/connections)
in your Google account, find your app and remove its access. If the account
is still connected in mailmd, its saved login stops working at once.
