# Contributing to mailmd

mailmd is a single Go program. Use the Go version in [go.mod](go.mod). With
`GOTOOLCHAIN=auto`, which is the default, Go fetches that version for you.
Release builds use `CGO_ENABLED=0` and need nothing else installed to run.

## Build and check a change

On Linux or macOS with Make:

```sh
make build
./bin/mailmd --version
make test race lint docs-check conformance
```

`make race` needs a C compiler. On Windows, run the checks in PowerShell:

```powershell
go build ./...
go test ./...
go vet ./...
```

CI builds and tests mailmd on Linux, macOS and Windows.

The tests need no network and no accounts. IMAP tests use an IMAP server that
runs inside the test. Gmail and Microsoft Graph tests use fake HTTP servers.

## Test against real mail servers

`scripts/integration.sh` starts Dovecot and GreenMail in Docker and runs
the tests in `internal/integration` against them. Then it removes the
containers again. You need Docker and Bash:

```sh
make integration
```

CI runs this in its own job. Real servers behave differently from the test
server in small ways, so run it after changes to the IMAP code.

## Package layout

| Package | What it does |
| --- | --- |
| `cmd/mailmd` | The entry point |
| `internal/cli` | The commands and their output |
| `internal/sync` | Runs a sync and writes its state and the index |
| `internal/backend/imap`, `gmail`, `graph` | Read one provider |
| `internal/backend/rfc822` | Parses MIME messages |
| `internal/writer` | Writes Markdown files and attachments |
| `internal/auth` | OAuth logins for Gmail and Microsoft |
| `internal/accounts` | The list of connected accounts |
| `internal/presets` | IMAP settings of known providers |
| `internal/demo` | The sample mail for `mailmd demo` |

## Golden files

Some writer tests compare output with files in `testdata`. When you change
the output on purpose, update them and read the diff:

```sh
MAILMD_UPDATE_GOLDEN=1 go test ./internal/writer
git diff internal/writer/testdata
```

## Help and docs

The command help is in `internal/cli/app.go`, and the help of each flag is
next to the code that defines the flag. After you change a command or a
flag, run `make docs` to update `docs/commands`, then `make docs-check`. It
also checks the links between the Markdown files, which needs Python 3. The
other pages in `docs/` are written by hand. Update them when the behavior
changes.

Write docs and messages in plain English, with short sentences, and say
what the reader can do. An error message names the cause and, where
possible, the next step.

## Rules for changes

- mailmd never writes to the mail provider. Open IMAP folders with EXAMINE
  and fetch bodies with BODY.PEEK. Ask OAuth only for read scopes.
- mailmd never deletes files in an archive.
- Every network call has a timeout.
- Every bug fix comes with a test that fails without it.
- Never commit credentials or real mail. Test fixtures use `example.com`
  addresses and no local paths.

Report security issues through [SECURITY.md](SECURITY.md).
