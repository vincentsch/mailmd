# Scheduling

mailmd has no background service of its own. To get new mail into your
archive on a schedule, let your system run `mailmd sync --quiet` every few
minutes.

Run the first sync by hand before you schedule it. The first sync of a large
mailbox can take a while, and you want to see that it works.

With `--quiet`, mailmd prints nothing when all went well, so cron doesn't
send you an email after every run. It still prints warnings, like a message
that was too large. Each warning appears once, in the sync that found the
problem. A folder that disappeared from the server is also reported once,
and after that `mailmd status` lists it. Errors go to standard error, and the
exit code is not 0:

| Exit code | Meaning |
| --- | --- |
| 0 | The sync worked |
| 1 | A flag or argument was wrong |
| 2 | The provider or the network failed, or some folders could not be synced |
| 3 | The login is no longer valid; run `mailmd connect` again |
| 4 | The provider didn't allow access |
| 5 | No account is connected, or the named account doesn't exist |

A sync with exit code 0 can still print warnings, for example when a message
was saved with its headers only. Those are the lines `--quiet` prints.

When a sync is still running, the next one stops with "Another mailmd sync of
this archive is running" and exit code 2. So pick an interval that is longer
than a normal sync takes.

Scheduled jobs often run with a short PATH, so use the full path to mailmd.
`command -v mailmd` prints it. The examples below use the install script's
default, `~/.local/bin/mailmd`.

## cron (Linux and macOS)

Run `crontab -e` and add:

```cron
*/15 * * * * $HOME/.local/bin/mailmd sync --quiet
```

This syncs all accounts every 15 minutes. If your system can send mail, cron
mails you anything that mailmd prints.

## systemd (Linux)

Create `~/.config/systemd/user/mailmd.service`:

```ini
[Unit]
Description=Sync mail with mailmd
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=%h/.local/bin/mailmd sync --quiet
```

And `~/.config/systemd/user/mailmd.timer`:

```ini
[Unit]
Description=Sync mail with mailmd every 15 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=15min
Persistent=true

[Install]
WantedBy=timers.target
```

Then start the timer:

```sh
systemctl --user daemon-reload
systemctl --user enable --now mailmd.timer
```

`systemctl --user list-timers` shows when it runs next, and
`journalctl --user -u mailmd` shows the output of past runs. A timer never
starts a second sync while the first one is still running.

User timers run only while you are logged in. To run them all the time, run
`loginctl enable-linger` once.

## launchd (macOS)

Create `~/Library/LaunchAgents/com.example.mailmd.plist`, with your user name
in place of `you`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.example.mailmd</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/you/.local/bin/mailmd</string>
    <string>sync</string>
    <string>--quiet</string>
  </array>
  <key>StartInterval</key>
  <integer>900</integer>
  <key>StandardOutPath</key>
  <string>/Users/you/Library/Logs/mailmd.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/you/Library/Logs/mailmd.log</string>
</dict>
</plist>
```

Load it:

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.example.mailmd.plist
```

launchd then runs the sync every 900 seconds, which is 15 minutes. Problems
show up in `~/Library/Logs/mailmd.log`. To stop it, run
`launchctl bootout gui/$(id -u)/com.example.mailmd`.

macOS protects some folders, like Documents and iCloud Drive, and can stop
the background job from writing there. Put the archive in `~/mail`, or allow
mailmd in System Settings under "Privacy & Security", then "Full Disk
Access".

## Task Scheduler (Windows)

In PowerShell:

```powershell
$action = New-ScheduledTaskAction -Execute "$env:USERPROFILE\.local\bin\mailmd.exe" -Argument "sync --quiet"
$trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 15)
Register-ScheduledTask -TaskName "mailmd" -Action $action -Trigger $trigger
```

Use the path where you put `mailmd.exe`.

## Gmail logins in testing mode

If you connected Gmail through the Gmail API and your Google Cloud app is in
testing mode, the login ends after 7 days. From then on, each scheduled sync
fails with exit code 3. The [Gmail
guide](gmail.md#the-7-day-limit-in-testing-mode) shows how to avoid that.
