# ticket-drift

Resets `dev` and `stage` to `main` after every merge to `main`, so hotfixes
that land on `main` alone stop turning every later merge into a conflict.
Before it resets, it works out which work on those branches has not reached
`main` and would be wiped, and posts one Discord message per ticket to the
people who wrote it. If anything before the reset fails — GitHub, ClickUp, a
single Discord message — nothing is reset.

    snapshot ─ detect ─ look up ─ notify ─ reset ─ correct / summarise
       │         │         │         │        │
       │         │         │         │        └─ force-update dev, stage to the pinned main,
       │         │         │         │           only if the branch did not gain new work
       │         │         │         └─ one Discord message per ticket, mentioning its authors
       │         │         └─ ClickUp title, link and status, read-only
       │         └─ PRs and commits on dev/stage whose code is not on main
       └─ pin the tips of main, dev and stage once

A failure in any phase stops the run before the next one.

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
| `-users` | — | path to a JSON file mapping GitHub logins to Discord user IDs; overrides `TICKET_DRIFT_USERS` |
| `-out-json` | — | write the full report to this file, also when the run fails |
| `-dry-run` | false | look everything up and print the messages and resets; post and change nothing |
| `-no-reset` | false | post the messages, worded "will be reset", and reset nothing |

In CircleCI every flag can be left out. `-no-reset` is for trying the
messages against a test webhook, not for CI: with no reset and no stored
state it would repeat the same messages on every merge to `main`.

What a ticket is lost from is decided by the code alone: a pull request on
`dev` or `stage` is lost unless its head commit is on `main`, a pull request
from the same branch merged into `main` contains it, the same branch was
merged into `main` later, or an environment-suffixed sibling branch
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
| `DISCORD_WEBHOOK_URL` | channel webhook. Required unless `-dry-run` |
| `TICKET_DRIFT_USERS` | JSON object `{"github-login": "discord-user-id"}` |
| `CLICKUP_API_TOKEN`, `CLICKUP_TEAM_ID` | required in every mode, including `-dry-run`; the run refuses to start without them |

### Discord messages

One message per ticket, in Russian, posted before anything is reset:

    **IT-10249** · Responsible managers
    https://app.clickup.com/t/… · статус в ClickUp: in stage
    `service-contractors`: **dev** и **stage** выровнены до main. Тикет нужно задеплоить заново.
    dev — #1098 `IT-10249/responsible-managers-dev`
    stage — #1062 `IT-10249/responsible-managers`
    <@111111111111111111>

PR numbers link to the pull requests. A ticket ClickUp does not know is still
reported, with «IT-12345 не найден в ClickUp» in place of the title and link.
Branches with no ticket ID and commits that came without a pull request get
messages of their own. If nothing is lost, nothing is posted.

After the resets, one summary line names the branches that were reset. If a
reset is aborted or refused after a message already announced it, a
correction names the branch and the reason.

### `TICKET_DRIFT_USERS`

People are mentioned by Discord user ID, looked up by the pull request
author's GitHub login (compared case-insensitively):

    {"octocat": "111111111111111111", "hubot": "222222222222222222"}

A login missing from the map is written as plain `@login`; the message still
posts. Only the mapped users can be pinged, so a branch name or ticket title
can never trigger `@everyone`. Keep the map in a shared CircleCI context so
the repos need no per-repo file.

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
lookup, a message, or a reset that was aborted or refused. `2` unknown
command. CI should read the report, not the exit code, for detail.

### Report

`-out-json` writes the pinned SHAs; per target the ahead count, shipped pull
requests with `matched_by` (`ancestor`, `head`, `branch`, `sibling`), lost
pull requests, ignored back-merges, carriers descended into, unattributed
commits and the reset outcome; and every group with its message and whether
it was posted. It is written on failure too, filled as far as the run got.

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
GitHub API:

    version: 2.1

    jobs:
      ticket-drift:
        docker:
          - image: cimg/go:1.25
        steps:
          - run:
              name: Reset dev and stage to main
              command: |
                go install github.com/dorim-group/ticket-drift@latest
                ticket-drift sync -out-json report.json
          - store_artifacts:
              path: report.json
              when: always

    workflows:
      ticket-drift:
        jobs:
          - ticket-drift:
              context: ticket-drift   # TICKET_DRIFT_APP_*, DISCORD_WEBHOOK_URL, TICKET_DRIFT_USERS, CLICKUP_*
              filters:
                branches:
                  only: main

### Trying it locally

    export GITHUB_TOKEN=… CLICKUP_API_TOKEN=… CLICKUP_TEAM_ID=…
    ticket-drift sync -owner dorim-group -repo service-contractors -dry-run

It prints what each target holds, every message it would post and the resets
it would make, and changes nothing.

## Version

    ticket-drift version
    ticket-drift v0.1.0 (abc1234)

The version is recorded in the report as `ticket_drift_version`. A `dev`
version means a local `go build`, not a release.

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
- The moment between the re-read and the force-update is not atomic; a merge
  landing in that sub-second window would be wiped unreported.
- More than 2000 commits ahead on a target is treated as an error, not
  processed partially.
