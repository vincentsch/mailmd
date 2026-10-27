# Releasing mailmd

A release is a Git tag like `v0.1.0`. Pushing the tag starts
`.github/workflows/release.yml`. It runs the CI checks again, then builds
six binaries with a checksum file and publishes them as a GitHub release.
Everything before the tag happens on your machine.

## Check the candidate

Start from a clean checkout of `main` with the Go version from `go.mod`,
Make, Docker and Python 3:

```sh
make test race lint docs-check conformance integration
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
git diff --check
```

Look through `git ls-files` for anything that doesn't belong in a public
repository:

- real mail or logins
- local paths
- private notes

Then search for secrets and read every match:

```sh
git grep -n -i -E 'password|secret|token|BEGIN PRIVATE KEY' -- ':!*_test.go'
```

Test fixtures use made-up values like `PASSWORD_SENTINEL`. A clean search
alone doesn't prove there is no secret.

## Prepare the changelog

Rename `## Unreleased` in `CHANGELOG.md` to `## 0.1.0 - YYYY-MM-DD`, with
the release date, and commit it. The tag is `v0.1.0`; the binary reports
`0.1.0`. Check the notes the release will get:

```sh
python3 scripts/release-notes.py 0.1.0
```

The release fails when the dated section is missing. Afterwards, start a new
`## Unreleased` section.

## Build and try the binaries

```sh
make build-all VERSION=0.1.0
```

`dist/` then holds `mailmd-linux-amd64`, `mailmd-linux-arm64`,
`mailmd-darwin-amd64`, `mailmd-darwin-arm64`, `mailmd-windows-amd64.exe`,
`mailmd-windows-arm64.exe` and `checksums.txt`. Run the binary for your
system:

```sh
dist/mailmd-linux-amd64 --version
dist/mailmd-linux-amd64 demo --out /tmp/mailmd-release-demo
```

For the others, check them with `file`. A binary built for another system
doesn't prove that it runs there. The CI jobs on macOS and Windows check
that for their own architecture.

## Try the installer

Serve the binaries the way GitHub would, from a scratch folder:

```sh
scripts/fake-release.sh dist /tmp/mailmd-release-fixture v0.1.0
python3 -m http.server 8765 --bind 127.0.0.1 --directory /tmp/mailmd-release-fixture
```

In another shell:

```sh
INSTALL_DIR=/tmp/mailmd-install MAILMD_VERSION=v0.1.0 \
  MAILMD_DOWNLOAD_BASE=http://127.0.0.1:8765 sh install.sh
/tmp/mailmd-install/mailmd --version
```

`go test ./scripts` also checks that the installer rejects a wrong checksum.
It also checks that the installer leaves an existing installation alone when
something fails. Stop the server and remove the scratch folders afterwards.

## Publish

Only when publishing is approved:

1. Make the repository public.
2. Turn on private vulnerability reporting under Settings, Security, and
   open the [reporting form](https://github.com/vincentsch/mailmd/security/advisories/new)
   to check that it works. `SECURITY.md` points there.
3. Push `main` and wait until CI is green on Linux, macOS and Windows,
   including the integration job.
4. Tag the commit and push the tag:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

The release job waits for CI to pass. Then it builds the six binaries and
records their build provenance. It publishes them with `checksums.txt` and
the changelog section. Only that job can write releases.

## Check the published release

From a fresh shell, on each system you can reach:

```sh
curl -fsSL https://raw.githubusercontent.com/vincentsch/mailmd/main/install.sh | sh
mailmd --version
mailmd demo --out /tmp/mailmd-demo
```

Check the build provenance of each binary:

```sh
gh attestation verify mailmd-linux-amd64 --repo vincentsch/mailmd
```

Check the Go install, which should report the tag's version:

```sh
GOBIN=/tmp/mailmd-gobin go install github.com/vincentsch/mailmd/cmd/mailmd@v0.1.0
/tmp/mailmd-gobin/mailmd --version
```

Finally, paste the README's agent prompt into a coding agent and check that
it can follow `AGENTS.md` from the public repository.
