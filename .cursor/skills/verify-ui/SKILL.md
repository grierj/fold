---
name: verify-ui
description: Prove Fold's web UI looks and behaves right at phone, tablet and desktop widths. It boots an isolated Fold with verify-fold, seeds a member, a leader and an admin, loads the main pages at 390/768/1280 in Chromium, saves screenshots, and checks horizontal overflow, clipped selects, tap targets, axe (serious/critical) and token contrast. Use for any styling or layout change, a new page or control, or a UI review.
---

# Verify UI

This is a layer on top of `verify-fold`. That skill owns launch, doctor, env and cleanup, and this one never starts or kills a server itself. Read `.cursor/skills/verify-fold/SKILL.md` first if you haven't. Its isolation rules apply here too: never use the dev database `fold`, `make dev`, or `make e2e`.

**Toolchain:**
- The repo targets Node 24 (CI, Docker).
- Go builds the binary.
- Postgres comes from the dev container, or from `VERIFY_PG_URL`.
- Chromium comes from `web/node_modules` (`npx playwright install chromium` once).
- `@axe-core/playwright` is already a dev dependency.

## Launch

From the repo root:

```bash
.cursor/skills/verify-fold/verify-fold up          # prints RUN_ID, URL, EVIDENCE
export RUN_ID=<printed id>
```

Without Docker, start a throwaway Postgres first and export its URL for **every** later command:

```bash
export VERIFY_PG_URL='postgres://<user>@127.0.0.1:<port>/postgres?sslmode=disable'
```

- **Ready:** `GET /healthz` returns 200.
- **Fresh database:** it opens on `/setup`. `drive-ui.mjs` completes the wizard and the seed itself, so there's no separate seed step.

## Doctor

```bash
.cursor/skills/verify-fold/verify-fold doctor "$RUN_ID"   # must print ok
```

Run it first whenever a page times out or looks wrong. Don't drive an instance that doctor rejects.

## Drive

```bash
eval "$(.cursor/skills/verify-fold/verify-fold env "$RUN_ID")"
node .cursor/skills/verify-ui/drive-ui.mjs                     # all pages, 390/768/1280
UI_PAGES=schedule-event,admin UI_WIDTHS=390 node .cursor/skills/verify-ui/drive-ui.mjs   # a subset
(cd web && npm run contrast)                  # token pairs
```

**What `drive-ui.mjs` does:**
1. **Seeds the instance.** This is precondition setup, not proof.
   - **Setup wizard:** completed through the real form (`[name=site_name]` …, button "Create site").
   - **Admin:** `verify-admin@example.com`, password `password1234`.
   - **Other people:** `verify-leader@example.com` (Lee Leader, leader) and `verify-member@example.com` (Sam Member, member), created with the same `/api/v1/admin/users` call the Admin "New user" form makes.
   - **Groups:** "Tuesday Bible Study" (public, limit 12, tags `bible-study` and `adults`) and "Youth Choir".
   - **Event:** a weekly 4-date series "Romans 8" at 7:00 PM America/Boise.
   - **Re-runs:** the seed is idempotent, so the script can run again on the same instance.
2. **Signs in through `/login`** for each role (labels "Email" and "Password", button "Log in"), in a context with locale `en-US` and timezone `America/Boise`.
3. **Loads each page and checks it at each width.**

   | id | role | path |
   |---|---|---|
   | home-signed-out | none | `/` |
   | login | none | `/login` |
   | join | none | `/join` |
   | home | member | `/` |
   | my-groups | member | `/my-groups` |
   | group-member | member | `/groups/<id>` |
   | event | member | `/events/<id>` |
   | profile | member | `/profile` |
   | notifications | member | `/notifications` |
   | group-leader | leader | `/groups/<id>` |
   | schedule-event | leader | `/groups/<id>/events/new` |
   | admin | admin | `/admin` |
   | admin-users | admin | `/admin?tab=users` |
   | new-group | admin | `/groups/new` |
   | church-event | admin | `/events/new` |

   Each page waits for a visible handle (heading, label or group name) and for network idle, then takes a full-page screenshot and runs the checks.

**Checks.** The first four are hard failures and make the exit code 1.

| Check | Fails when | Why |
|---|---|---|
| Overflow | `documentElement.scrollWidth > innerWidth` | Sideways scroll on phones |
| Clipped select | The selected option's text is wider than the select's inner width (canvas measure) | The PR #51 "Church time zone: America/Los…" bug |
| Target under 24px | Any visible control (link, button, input, select, textarea, tab) is under 24×24 CSS px, counting its `<label for>`. Inline links inside running text are exempt. | WCAG 2.2 SC 2.5.8 (AA) |
| axe | Any serious or critical violation (WCAG 2.0–2.2 A/AA tags, which include `target-size`, `label` and `color-contrast`) | Same bar as `web/e2e/a11y.spec.ts` |
| Under 44px (report only) | Controls under 44px tall at 390 are listed per page in `results.json` | The design target (TOKENS.md). Not yet a gate; compare the count to master. |

**Token contrast.** `npm run contrast` (`web/scripts/token-contrast.mjs`) resolves the `@theme` roles in `web/src/index.css` and fails (exit 1) when any text pair is under 4.5:1. It also fails when a non-text edge is under 3:1: `border-strong`, `primary-border`, `focus`, and every `border-<role>` used in a `.btn-*` or `.chip` rule.

## Evidence

Everything goes to `$VERIFY_EVIDENCE/ui/` (that is, `/tmp/fold-verify-proof/<RUN_ID>/ui/`):
- `<page>-<width>.png`: a full-page screenshot for every page and width (45 files for a full run).
- `results.json`: the URL, the seed ids, and per page and width: failures, the screenshot name, and the under-44px list at 390.
- `result.txt`: the feature id (`ui-checks`), the entry point, the hard-failure count, and one line per page and width.

**Proof standards:**
- Pages are reached the way a person reaches them: signed in through `/login`, with no test endpoints.
- For a styling PR, run `drive-ui.mjs` on master and on the branch against two separate `up` instances. Then compare screenshots (`node web/e2e/compare-screens.mjs <dirA> <dirB>` works on these folders too) and paste the failure lines from both runs.
- When a change alters a control's state (selected chip, pressed button), capture both states. Add the step to the page's feature file rather than trusting one screenshot.

**What passing means:**
- `drive-ui.mjs` exits 0: no hard failures on any page or width. Since PR 5 (nav shell) master has none, and `web/e2e/layout.spec.ts` gates the same three checks in CI.
- `npm run contrast` exits 0.
- The under-44px count at 390 is not higher than master's.
- A reviewer has looked at the 390 screenshots of every page the change touched.

## Cleanup

```bash
.cursor/skills/verify-fold/verify-fold down "$RUN_ID"
```

This kills only the pid recorded for that run and drops only `fold_verify_<run>`. It keeps `/tmp/fold-verify-proof/<RUN_ID>/ui/`. Afterwards, confirm the PNGs and `results.json` are still there. If you started a throwaway Postgres for `VERIFY_PG_URL`, stop it by its own data directory (`pg_ctl -D <dir> stop -m fast`), never by process name.

## Helpers

- `.cursor/skills/verify-ui/drive-ui.mjs`: seed, then pages, checks and evidence (env from `verify-fold env`; optional `UI_PAGES`, `UI_WIDTHS`). It exits 2 when the environment isn't set.
- `web/scripts/token-contrast.mjs` (`npm run contrast` in `web/`, also run in CI): token-pair contrast. It exits 1 on any failing pair.
- `web/e2e/ui-measure.mjs`: the overflow, clipped-select and target measurement, shared with `web/e2e/layout.spec.ts` (the CI gate on the e2e seed).

Both load Playwright and axe from `web/node_modules` via `createRequire`, so there's nothing to install.
