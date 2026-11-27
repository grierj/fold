# Fold: groups and events for a church

One place for a church's small groups, events, RSVPs
and announcements, run by the church on its own small server. Free, MIT-licensed.

- Installing: `docs/INSTALL.md`
- Running it (backups, restore, upgrades): `docs/OPERATIONS.md`
- How the project works: `AGENTS.md`, then `docs/PLAN.md` and `docs/DECISIONS.md`

## Developing

Go 1.27. `make dev` runs a local site at http://localhost:8080 with mail kept at
`/dev/outbox`. `make check` runs vet, staticcheck, govulncheck, the tests and the stylesheet
check; `make e2e` runs the browser gates (needs Node, for developer tooling only).
