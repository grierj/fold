# Fold milestones: Phase 5

### 14.8 Phase 5: Release engineering

**M5.1 Prebuilt images**
- Brief: [`briefs/audit-2026-11/R1-deployment.md`](../briefs/audit-2026-11/R1-deployment.md) (R1; hosting verdict DEC-142).
- Depends on: M0.1
- Scope: GitHub Actions builds `linux/amd64` and `linux/arm64` images of `fold` and `fold-backup` and pushes them to GHCR on tags (C10). The deploy compose uses `image:`; a `docker-compose.build.yml` override keeps local builds for developers.
- Acceptance:
  - [ ] `docker compose pull && docker compose up -d` works on a fresh VPS without Go or Node
  - [ ] Images are signed or have checksums published in the release notes
- Tests: CI smoke test starts the stack and hits `/healthz`.

**M5.2 Backups**
- Status: future portable container/restore scope. The existing DigitalOcean host-cron dump and optional encrypted offsite helper are useful partial groundwork, not completion of this milestone.
- Depends on: M5.1
- Scope: 11.8 for Compose: a small `ghcr.io/grierj/fold-backup` image (postgres:16-alpine plus `age` and `rclone`) with `backup.sh` (nightly loop and `now`) and `restore.sh`, `LAST_OK`, retention, `age`-encrypted offsite upload to any S3-compatible bucket.
- Acceptance:
  - [ ] Nightly dump and uploads tar appear with the retention rule applied
  - [ ] `docker compose exec backup backup.sh now` makes a backup on demand
  - [ ] `restore.sh` brings back a working site (CI restore test)
  - [ ] Offsite copies are `age`-encrypted, and the server holds only the public key
  - [ ] The System page warns when the last backup is older than 36 hours
- Tests: CI restore test; retention unit test.

**M5.3 Upgrades**
- Status: the DigitalOcean upgrade helper exists and dumps before upgrading; the app's mandatory pre-migration backup gate and release-to-release CI remain future work.
- Depends on: M5.2
- Scope: 11.7. Backup check before migrating (skipped on first install or with no pending migrations; no off switch), exit non-zero on failure, `docs/UPGRADING.md`.
- Acceptance:
  - [ ] Upgrading from the previous release with seeded data passes in CI (from v0.5 on)
  - [ ] A first install with an empty `backups` volume starts without a backup
  - [ ] With pending migrations and no backup newer than 24 hours, the app exits with a clear message
  - [ ] A failing migration leaves the data unchanged, and the app exits non-zero and shows unhealthy
- Tests: upgrade test in CI.

**M5.4 Deployment docs (Compose)**
- Status: partial groundwork exists in `docs/DEPLOY-DIGITALOCEAN.md`, AGENTS.md and the provision/smoke/upgrade scripts. The full release workflow below depends on unbuilt M5.1–M5.3 and `fold setup`; its acceptance remains open. Compose passes mail configuration through from `.env`; recreate the app after changes and verify delivery before relying on email flows.
- Depends on: M5.1–M5.3
- Scope: `docs/DEPLOY.md` for humans, and the AGENTS.md Deployment section (Appendix A).
- Acceptance:
  - [ ] A person following DEPLOY.md on a fresh 2 GB VPS reaches the wizard in under 30 minutes (manual check, recorded in the PR)
  - [ ] An agent following AGENTS.md completes the same run with `fold setup --dry-run` first
- Tests: docs link check.

**M5.5 Render Blueprint (optional, future; not committed)**
- Status: cut from the roadmap (DEC-140, DEC-142); the 12.5 evaluation stays as reference. Re-enters only as a new milestone for a named church. Was: start only when a church asks (DEC-26).
- Depends on: M5.4, M4.1
- Scope: `deploy/render.yaml` (the Render reference in Appendix A), `docs/DEPLOY-RENDER.md`, and the app details Render needs: `PORT`, the Render value for `FOLD_TRUSTED_PROXIES`, a way to skip the Compose backup check where Render's PITR covers upgrades, base64 secrets.
- Acceptance:
  - [ ] `render.yaml` passes `render blueprints validate` (or validation through Render's API) in CI or in the PR notes
  - [ ] A person following DEPLOY-RENDER.md, in a Render workspace the church owns, reaches the wizard with a custom domain and TLS (manual check, recorded in the PR)
  - [ ] Client IPs are real (the Admin > System IP check) behind Render's proxy
  - [ ] Docs list the monthly cost from 12.5, the disk trade-off, and the S3 option
- Tests: blueprint validation; docs link check.

