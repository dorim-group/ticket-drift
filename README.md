# ticket-drift

Resets `dev` and `stage` to `main` after every merge to `main`, so hotfixes
that land on `main` alone stop turning every later merge into a conflict.
Before it resets, it works out which work on those branches has not reached
`main` and would be wiped, and posts one Discord message per ticket,
tagging its ClickUp assignees. If anything before the reset fails — GitHub, ClickUp, a
single Discord message — nothing is reset.

    snapshot ─ detect ─ look up ─ notify ─ reset ─ correct / summarise
       │         │         │         │        │
       │         │         │         │        └─ force-update dev, stage to the pinned main,
       │         │         │         │           only if the branch did not gain new work
       │         │         │         └─ one Discord message per ticket, mentioning its assignees
       │         │         └─ ClickUp title, link, status and assignees, read-only
       │         └─ PRs and commits on dev/stage whose code is not on main
       └─ pin the tips of main, dev and stage once

A failure in any phase before the reset — reading GitHub, checking ClickUp,
posting a message — stops the run with nothing reset. In the reset phase the
targets are reset independently: a refused or aborted reset of `dev` does not
stop `stage`, and the run then exits `1`.

## Install

    go install github.com/dorim-group/ticket-drift@latest

## Layout

    ./                  CLI wiring: flags, environment, phase orchestration, rendering
    internal/drift/     first-parent walk, classification, shipped rules, grouping
    internal/github/    GitHub client: App or token auth, refs, compare, pull requests
    internal/clickup/   task lookup by custom ID
    internal/discord/   webhook client with rate-limit handling

## Use

### `ticket-drift sync`

    ticket-drift sync [-dry-run] [-no-reset] [-out-json report.json]

| flag | default | |
|---|---|---|
| `-owner` | `CIRCLE_PROJECT_USERNAME` | repository owner |
| `-repo` | `CIRCLE_PROJECT_REPONAME` | repository name |
| `-base` | `main` | the branch the targets are reset to |
| `-targets` | `dev,stage` | comma-separated branches to reset; a missing one is skipped |
| `-ticket-pattern` | `IT-\d+` | Go regexp for ticket IDs in branch names, matched case-insensitively |
| `-users` | — | path to a JSON file listing people and their Discord user IDs; overrides `TICKET_DRIFT_USERS_URL` and `TICKET_DRIFT_USERS` |
| `-out-json` | — | write the full report to this file, also when the run fails |
| `-dry-run` | false | look everything up and print the messages and resets; post and change nothing |
| `-no-reset` | false | post the messages, worded "will be reset", and reset nothing |

In CircleCI every flag can be left out. `-no-reset` is for trying the
messages against a test webhook, not for CI: with no reset and no stored
state it would repeat the same messages on every merge to `main`.

What a ticket is lost from is decided by the code alone: a pull request on
`dev` or `stage` is lost unless its head commit is on `main`, a pull request
from the same branch merged into `main` contains it, the same branch — only
one whose name carries a ticket ID — was merged into `main` later (the
terminal output shows `matched_by=branch` for these), or an environment-suffixed sibling branch
(`IT-1/x-dev` and `IT-1/x`) shipped and this one has no commits of its own.
When a rule cannot be evaluated the work is reported: a false alarm beats a
silent loss. ClickUp only describes what git found.

Credentials come from the environment — never as flag defaults, which `-h`
would print:

| variable | what |
|---|---|
| `GITHUB_TOKEN` | plain token; if set it is used instead of the App. For local runs |
| `TICKET_DRIFT_APP_ID` | GitHub App ID |
| `TICKET_DRIFT_APP_INSTALLATION_ID` | installation ID (the number at the end of the install URL) |
| `TICKET_DRIFT_APP_PRIVATE_KEY` | the App's PEM, base64-encoded so it survives an env var |
| `DISCORD_WEBHOOK_URL` | channel webhook. Required unless `-dry-run`. If unset, `DISCORD_WEBHOOK` — the variable the repos' CircleCI contexts already carry for deploy notifications — is used |
| `TICKET_DRIFT_USERS` | JSON list of people, see [below](#ticket_drift_users) |
| `TICKET_DRIFT_USERS_URL` | `https://` link to the same list, downloaded when the run starts; overrides `TICKET_DRIFT_USERS`, see [below](#from-a-link) |
| `CLICKUP_API_TOKEN`, `CLICKUP_TEAM_ID` | required in every mode, including `-dry-run`; the run refuses to start without them |

### Discord messages

One message per ticket, in Russian, posted before anything is reset:

    ### 🎫 IT-10249 · Responsible managers
    📋 Открыть в ClickUp · статус: **in stage**
    🔄 `service-contractors`: **dev** и **stage** выровнены до **main**
    🚀 Тикет нужно задеплоить заново
    🔀 Pull requests:
    • **dev** — #1098 `IT-10249/responsible-managers-dev`
    • **stage** — #1062 `IT-10249/responsible-managers`
    👤 Ответственный: <@111111111111111111>

«Открыть в ClickUp» links to the task and PR numbers link to the pull requests.
A branch without a ticket is headed 🌿, commits without a PR 📝, and after the
resets a ✅ summary follows (⚠️ instead for a branch that could not be reset).
A ticket ClickUp does not know is still reported, headed «❓ IT-12345 не найден
в ClickUp», with no ClickUp line.
Whenever at least one ticket has to be looked up, the run first verifies the
ClickUp token and team (`CLICKUP_TEAM_ID`); if that check fails, the run
stops with nothing posted and nothing reset. Once it has passed, any 4xx ClickUp gives for one ticket ID
(it answers an unknown ID with 404 or 401) is taken as "not found", so a typo
in a branch name is announced instead of blocking the reset.
Branches with no ticket ID and commits that came without a pull request get
messages of their own. If nothing is lost, nothing is posted.

After the resets, one summary line names the branches that were reset. If a
reset is aborted or refused after a message already announced it, a
correction names the branch and the reason.

### `TICKET_DRIFT_USERS`

A JSON list with one entry per person:

    [
      {
        "name": "Octo Cat",
        "discord_id": "111111111111111111",
        "clickup_email": "octo@example.com",
        "github_login": "octocat"
      }
    ]

| field | |
|---|---|
| `discord_id` | required; 15 to 21 digits, quoted |
| `clickup_email`, `github_login` | at least one; compared case-insensitively |
| `name` | optional, for the reader and error messages |

A ticket message mentions every ClickUp assignee of the ticket, matched by
email. One who is not in the list is written by ClickUp name, without a ping.
The authors of the pull requests are mentioned instead when the ticket has no
assignee or ClickUp does not know it, and in addition to the names when no
assignee is in the list. Branches without a ticket mention the pull request
authors, commits without a pull request their authors; both are matched by
GitHub login, and a login not in the list is written as plain `@login`.

Emails are never posted or written to the report. Only people in the
list can be pinged, so a branch name or ticket title can never trigger
`@everyone`. Keep the list in a shared CircleCI context so the repos need no
per-repo file.

An unknown field, a bad Discord ID, an entry with neither an email nor a
login, or an email or login used by two entries stops the run at start-up with
an error naming the entry.

#### From a link

Set `TICKET_DRIFT_USERS_URL` instead to keep the list in a bucket (S3, R2, …)
and edit it there. The run downloads it first and stops, with nothing posted
or reset, when the link is not `https://`, the answer is not 200 within 15
seconds, the file is empty or over 1 MiB, a redirect leaves `https`, or the
list is invalid. The log names the link without its query string, user info
or fragment.

Anyone with the link can read the emails in it, so give the file an
unguessable name and keep the link as a secret in the CircleCI context. A
signed S3 or R2 link expires within 7 days, which does not suit a CI job.

The older form, `{"github-login": "discord-user-id"}`, is still accepted; with
it no assignee can be matched. Versions before this list existed (v0.1.0)
reject the list, so update the tool first.

To get a Discord user ID: User Settings → Advanced → turn on **Developer
Mode**, then right-click the person → **Copy User ID**.

### GitHub permissions

The App (or token) needs:

- **Contents: read and write** — compare branches, read and force-update refs;
- **Pull requests: read** — attribute commits to pull requests;
- permission to force-push `dev` and `stage`: if they are protected, allow
  the App to bypass the protection or force-push.

`-dry-run` needs read access only.

### Exit codes

`0` done — including nothing to do and dry runs. `1` anything failed: a
lookup, a message, or a reset that was aborted or refused. `2` no command or
an unknown command. CI should read the report, not the exit code, for detail.

### Report

`-out-json` writes the pinned SHAs; per target the ahead count, shipped pull
requests with `matched_by` (`ancestor`, `head`, `branch`, `sibling`), lost
pull requests, ignored back-merges, carriers descended into, unattributed
commits and the reset outcome; and every group with its `people` (PR and
commit authors), `assignees` (ClickUp names), message and whether it was
posted. It is written on failure too, filled as far as the run got.

| reset outcome | meaning |
|---|---|
| `done` | reset to the pinned `main` |
| `not_needed` | already at `main` |
| `skipped` | missing branch, dry run, `-no-reset`, or the run stopped earlier |
| `superseded` | a run for a newer `main` already reset it |
| `aborted_moved` | new work landed since the snapshot; not reset |
| `failed` | GitHub refused or could not be asked; not reset |

### CircleCI

Runs on `main` only. No checkout is needed — everything goes through the
GitHub API. Pin a release tag rather than `@latest`, so a new release reaches
CI only when you move the pin:

    version: 2.1

    jobs:
      ticket-drift:
        docker:
          - image: cimg/go:1.25
        steps:
          - run:
              name: Reset dev and stage to main
              command: |
                go install github.com/dorim-group/ticket-drift@v0.2.0
                ticket-drift sync -out-json report.json
          - store_artifacts:
              path: report.json
              when: always

    workflows:
      ticket-drift:
        jobs:
          - ticket-drift:
              context: ticket-drift   # TICKET_DRIFT_APP_*, DISCORD_WEBHOOK_URL, TICKET_DRIFT_USERS(_URL), CLICKUP_*
              filters:
                branches:
                  only: main

While the repository is private, `go install` needs
`GOPRIVATE=github.com/dorim-group` in the job's environment and git
credentials that can read it, for example a read-only token in
`MODULE_READ_TOKEN` and, before the install step,
`git config --global url."https://x-access-token:${MODULE_READ_TOKEN}@github.com/dorim-group/".insteadOf "https://github.com/dorim-group/"`.
Do not put that token in `GITHUB_TOKEN`: ticket-drift would use it instead of
the App.

### Trying it locally

    export GITHUB_TOKEN=… CLICKUP_API_TOKEN=… CLICKUP_TEAM_ID=…
    ticket-drift sync -owner dorim-group -repo service-contractors -dry-run

It prints what each target holds, every message it would post and the resets
it would make, and changes nothing.

## Version

    ticket-drift version
    ticket-drift v0.1.0 (none)

Installed with `go install …@v0.1.0`, the binary reports the module version
Go recorded at build time; the commit in brackets is `none`, since
`go install` stamps no commit. A release binary shows the commit instead,
`ticket-drift v0.1.0 (abc1234)`. A local `go build` in a git checkout reports the
pseudo-version Go derives from the checkout, such as
`v0.0.0-20261122191154-62323ad417e4+dirty`; `dev` means no version
information was recorded at all.
The version is recorded in the report as `ticket_drift_version`.

## Release

Pushing a tag that starts with `v` runs GoReleaser in GitHub Actions
(`.github/workflows/release.yml`): it runs the tests, builds linux and darwin
binaries for amd64 and arm64 with the version and commit stamped in, and
publishes them with `checksums.txt` as a GitHub release.

    git tag v0.1.0 && git push origin v0.1.0

`go install github.com/dorim-group/ticket-drift@v0.1.0` works from the same
tag and needs no release assets. Pushes to `main` and pull requests run
`go vet`, the race tests and a `gofmt` check (`.github/workflows/test.yml`).

## Known limits

- Overlapping runs (two merges to `main` close together) each post their
  messages, so the same ticket can be announced twice.
- A branch merged into a target, then force-pushed to drop work, then merged
  into `main` is treated as shipped; the dropped commits are wiped without a
  message.
- A hotfix cherry-picked onto `dev` and separately onto `main` has different
  SHAs; it is reported as lost although the change is in `main`.
- If the run fails halfway through notifications, a re-run posts the already
  delivered messages again.
- A Discord post whose answer is lost — a timeout, a dropped connection or a
  5xx after Discord had already accepted it — is retried, so that message can
  appear twice.
- The moment between the re-read and the force-update is not atomic; a merge
  landing in that sub-second window would be wiped unreported.
- More than 2000 commits ahead on a target is treated as an error, not
  processed partially.
