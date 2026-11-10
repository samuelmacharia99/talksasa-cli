# talksasa

The command line for [Talksasa](https://talksasa.com) apps: status, logs,
deploys from Git, environment variables, domains and backups, from a terminal
or a CI job. It calls the Talksasa customer API, and does what the portal's
buttons do.

## Install

Linux and macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/samuelmacharia99/talksasa-cli/main/install.sh | sh
```

The script downloads the latest release, checks it against the release's
`checksums.txt`, and installs it to `/usr/local/bin` (or `~/.local/bin`).
Set `TALKSASA_VERSION=v0.1.0` for a particular version.

Windows: download the zip for your machine from
[Releases](https://github.com/samuelmacharia99/talksasa-cli/releases) and put
`talksasa.exe` on your `PATH`.

Every release is built by this repository's GitHub workflow, which signs its
provenance. With the GitHub CLI you can check a download came from it:

```sh
gh attestation verify talksasa_*.tar.gz --repo samuelmacharia99/talksasa-cli
```

## Sign in

Make a token in the portal under **Developers**, with only the permissions
you need, then:

```sh
talksasa login
```

The token is asked for without echo, checked, and kept in your system's
keychain (macOS Keychain, Windows Credential Manager, the Secret Service on
Linux). Where there is no keychain, it goes in a file only you can read, and
`login` says where.

## Use

```sh
talksasa apps                    # your services
talksasa link shop               # this folder is the app "shop" (writes .talksasa.json)
talksasa status
talksasa logs -f
talksasa deploy                  # pull the connected branch, wait, fail if it fails
talksasa redeploy
talksasa env list                # names and state; values are never shown
talksasa env set MAIL_FROM=shop@example.com
talksasa env set STRIPE_SECRET   # asks for the value without echo
talksasa env set --from-file .env.production
talksasa domains
talksasa backups create
```

Every command takes `--app <id or name>`, `--json` for scripts, and `--help`.

`.talksasa.json` holds the app's id and name only, never a token or an
address, so it is safe to commit.

## CI

Store a token with the **Deploy** permission as a secret, then:

```yaml
# GitHub Actions
- run: curl -fsSL https://raw.githubusercontent.com/samuelmacharia99/talksasa-cli/main/install.sh | sh
- run: talksasa deploy --app shop
  env:
    TALKSASA_TOKEN: ${{ secrets.TALKSASA_TOKEN }}
```

`TALKSASA_TOKEN` is used as given and never written to disk. `deploy` waits
for the pull to finish and exits non-zero if it failed, so the job fails with
it.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Done |
| 1 | The action ran and failed (a deploy, a restore), or was cancelled |
| 2 | The command line was wrong, or a confirmation was needed (`--yes`) |
| 3 | Not signed in, or the token is wrong, expired or revoked |
| 4 | The token lacks a permission, or the account or app is suspended |
| 5 | No such app, backup or restore |
| 6 | Something else is running; wait and try again |
| 7 | The input was refused, or the app's stack cannot do this |
| 8 | Too many requests; wait and try again |
| 9 | The platform or the app's host did not answer |
| 10 | Still running when `--timeout` ran out |
| 130 | Interrupted |

## Settings

| | |
|---|---|
| `--api-url`, `TALKSASA_API_URL` | The API address. `login --api-url` remembers it. |
| `--app`, `TALKSASA_APP` | The app, when the folder is not linked. |
| `TALKSASA_TOKEN` | A token to use instead of the stored one. |
| `TALKSASA_CONFIG_DIR` | Where settings and the fallback token file live. |
| `TALKSASA_NO_KEYRING=1` | Do not use the system keychain. |

## What it cannot do

On purpose, these stay in the portal: reading environment variable values,
changing the variables the platform manages, changing a stack or resetting
its database, binding domains, the terminal and file manager, billing, and
creating or deleting services.

## Develop

```sh
go test ./...
```

`scripts/e2e.sh` runs every command against a platform on your own machine;
it refuses any other address.

## License

MIT
