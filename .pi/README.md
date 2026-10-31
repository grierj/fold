# Autoflow for Fold

The extension and role/model assignments are installed globally. This directory
adds repository checks and pins publication to `origin/master` with a normal
merge commit. It does not install packages or change the global model map.

## Not ready for unattended publication

**Do not launch `/autoflow <prompt>` yet.** The installed extension proceeds from
planning through publication and merge automatically; it has no PR-only or
manual-merge configuration option. This check configuration does not disable it.
The following differences need resolving before a real Fold flow:

- Its publisher instructs a local rebase and `git push --force-with-lease` on
  updates, and `git push -u origin HEAD` initially. Fold requires exact branch
  refspecs (`HEAD:refs/heads/<branch>`) and merging master into open branches,
  never rebasing them. Fix the publisher rather than relying on a task prompt
  to override its instructions.
- Fold's independent-review substitution currently names **orchestrate**, not
  Autoflow. Grier must approve Autoflow's review equivalence before its verdicts
  can satisfy that merge rule. Its efficiency gate also does not explicitly
  cover the required simplicity/database review.
- Security/visibility-policy, repository-settings, and production/deploy changes
  still wait for Grier unless already decided. The extension has no configured
  per-change merge hold. Keep those changes out of unattended flows until this
  can be enforced.
- The extension treats "no checks reported" as a pass. Fold requires `test`,
  `web`, and `provision` green on the PR head. Keep branch protection intact;
  do not treat a workflow completion as review or CI approval.
- The UI gate is text-only today; `checks.screenshot` is parsed but unused.
  UI work still needs `.cursor/skills/verify-ui/SKILL.md`, retained evidence,
  and inspection of the changed pages' 390px screenshots. Automated Playwright
  accessibility/layout checks do not replace that proof.
- After merge, Fold requires merging master into the other open branches and
  reporting the merge SHA; Autoflow does not perform that follow-up.

These are extension/policy follow-ups, not settings silently changed by this setup.

## Checks

| Stage | Command | Timeout | Coverage |
| --- | --- | --- | --- |
| Every task and publisher acceptance | `bash .pi/autoflow-check.sh` | 15 min | Web types/build/bundle budget, static Go build, shell/Go/web lint, Postgres Go tests (including schema drift and repo guards), Vitest, token contrast |
| Completed task queue | `make e2e` | 20 min | Real embedded app, fresh Postgres e2e database, Chromium, accessibility and layout gates |
| Completed task queue | `make provision-dry-run` | 10 min | Provisioner checks in a disposable Ubuntu container; no real host provisioning |

`web/src/**`, `web/public/**`, `web/index.html`, and `templates/**` trigger the
UI gate. Models, architecture rounds, and stall threshold inherit from
`~/.pi/agent/autoflow/config.json`. Token/wall-clock checkpoint settings are not
wired into the installed extension, so none are specified here.

The local checks do not replace GitHub CI's vulnerability scans or mandatory
status checks. Changes to the check commands or global models require `/reload`:
the extension snapshots config at session start.

## Environment and validation

Use the Node major in `.nvmrc`, the Go toolchain in `go.mod`, Docker Compose,
`psql`, shellcheck, and authenticated `gh`. Install web dependencies and Chromium
once (`npm ci` and `npx playwright install chromium` in `web/`). If using mise:

```bash
mise exec node@"$(tr -d '[:space:]' < .nvmrc)" -- bash .pi/autoflow-check.sh
mise exec node@"$(tr -d '[:space:]' < .nvmrc)" -- pi
```

Inside Pi, `/reload` then `/autoflow preflight` validates config, role-model
availability, clean git state, and GitHub authentication **without launching a
flow**. Commit this setup through the normal reviewed PR process first: uncommitted
configuration makes the clean-tree preflight fail. Preflight does not run tests
or verify the publication-policy differences listed above.

`make test` and `make e2e` start dev Postgres when `DATABASE_URL` is unset. Use only
a local disposable test database with create/drop privileges, never production.
Do not run another flow or `make e2e` concurrently in this checkout: Playwright
shares `<database>_e2e`, `/tmp/fold-e2e`, and its port. One-off UI proof uses the
isolated verify-ui/verify-fold helpers instead.

Once publication compatibility and review approval are resolved, start with one
small, already-decided slice, not a whole high-complexity milestone. Follow
`AGENTS.md` and `docs/plan/PLAN.md`; this setup grants no deployment permission.
