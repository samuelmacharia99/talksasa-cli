# talksasa

The command line for [Talksasa](https://talksasa.com) apps: status, logs,
deploys from Git or straight from a folder, environment variables, domains and
backups, from a terminal or a CI job. It calls the Talksasa customer API, and does what the portal's
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
talksasa deploy --dir .          # or deploy this folder, with no Git host
talksasa releases                # uploaded releases; roll back with: releases deploy 2
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

## Deploy without Git

```sh
talksasa deploy --dir . --dry-run   # list what would be uploaded; sends nothing
talksasa deploy --dir .
```

`--dir` packs the folder into a `.tar.gz`, uploads it, and deploys it through
the same pipeline as a Git pull: the code is checked again, built, swapped in,
restarted and health-checked, and the previous code goes back if that fails.
It waits and fails with the deploy, as `deploy` does, and takes the same
`--no-composer`, `--no-migrations`, `--rebuild`, `--no-wait` and `--timeout`.

What goes in is what Git would track:

- Left out by default: `.git/`, `.hg/`, `.svn/`, `node_modules/`,
  `__pycache__/`, `.venv/`, `venv/`, `.DS_Store`, `Thumbs.db`,
  `.talksasa.json`, `.env` and `.env.*` (but `.env.example`, `.env.sample`,
  `.env.template` and `.env.dist` are kept), and `vendor/` in a folder that
  has a `composer.json` (a Go module's `vendor/` is kept).
- Then every `.gitignore` in the folder (each applies to its own subfolder),
  and `.git/info/exclude`, with Git's rules: `!` re-includes, `/` anchors,
  `**`, `*`, `?` and `[...]`, and a file cannot be re-included when its
  folder is excluded. A global Git excludes file is not read, and matching
  is case-sensitive. The rules are applied to the folder as it is on disk,
  not to Git's index: a file Git tracks although a rule matches it (one
  added with `git add -f`, such as committed build output) is left out
  unless `.talksasaignore` puts it back, for example `!/public/build/`.
- Last, `.talksasaignore` files, in the same syntax and above all the rest:
  `!vendor/` puts back what a default or a `.gitignore` left out, and
  `/storage/logs/` leaves out what Git keeps.

Symbolic links are never followed or uploaded; the command says how many it
skipped. It warns when a file named like `.env.production` is going in: the
app's environment comes from its **Environment** tab (`talksasa env`), never
from the folder, and the platform removes a top-level `.env` from every
release anyway.

The platform takes at most 200 MB compressed, 1 GB unpacked, 50,000 files,
50,000 folders and 64 levels of nesting. A folder over a limit is refused
before anything is sent, with its largest top-level folders and a
`.talksasaignore` line to try.

The token needs the **Upload code** permission. An app connected to a Git
repository refuses uploads: `--replace-git` disconnects the repository and
turns off auto-deploy, needs **Destructive**, and asks first. Replacing an
app's code with a folder for the first time asks too (`--yes` answers).

The same folder always packs to the same bytes, so an interrupted upload
(Ctrl-C, a dropped connection) resumes where it stopped when the same
command runs again. The platform keeps a part-sent upload for an hour after
its last chunk, and three hours from its start at most.

The platform keeps the last three uploaded releases it deployed:

```sh
talksasa releases            # number, live, status, size, files, dates
talksasa releases deploy 2   # deploy release 2 again (Deploy permission)
```

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
it. To deploy the job's checkout without a Git connection on the app, use
`talksasa deploy --dir . --app shop` with a token that has **Upload code**.

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
its database, connecting a Git repository, binding domains, the terminal and
file manager, billing, and creating or deleting services. `deploy --dir`
replaces the app's code; it does not upload into storage or change the
app's environment.

## Develop

```sh
go test ./...
```

`scripts/e2e.sh` runs every command against a platform on your own machine;
it refuses any other address.

## License

MIT
