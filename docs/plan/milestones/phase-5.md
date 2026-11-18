# Fold milestones: Phase 5

### 14.8 Phase 5: Release engineering

**M5.1 Prebuilt images**
- Brief: [`briefs/audit-2026-11/R1-deployment.md`](../briefs/audit-2026-11/R1-deployment.md) (R1; hosting verdict DEC-142).
- Partly done (PR #251, #252): the `fold` image. `release.yml` builds `linux/amd64` and `linux/arm64` on `v*` tags, pushes exact versions to `ghcr.io/grierj/fold` (never `latest`), boots the image, attests its build provenance and writes the digest into the release notes. `docker-compose.yml` uses `image: ghcr.io/grierj/fold:${FOLD_VERSION}`, `docker-compose.build.yml` keeps local builds, and CI's `compose` job (`make compose-smoke`) boots the shipped stack and checks `/healthz`. The `fold-backup` image is not built (it is M5.2's container).
- Depends on: M0.1
- Scope: GitHub Actions builds `linux/amd64` and `linux/arm64` images of `fold` and pushes them to GHCR on tags (C10); the `fold-backup` image is M5.2's. The deploy compose uses `image:`; a `docker-compose.build.yml` override keeps local builds for developers. Also carried from M0.3b: any security headers the Caddy and image layers should add (10.6), and pinned base-image digests in releases.
- Acceptance:
  - [ ] `docker compose pull && docker compose up -d` works on a fresh VPS without Go or Node (the compose file and release workflow are built; this needs a published release)
  - [ ] Images are signed or have checksums published in the release notes (built: a build provenance attestation and the digest)
- Tests: CI smoke test starts the stack and hits `/healthz` (built: `scripts/compose-smoke.sh`).

**M5.2 Backups**
- In progress: the `fold-backup` image (`deploy/backup/`) with nightly runs and `backup.sh now`; the `backup` Compose service and its `backup-status` volume; `LAST_OK` and `status.json`; a System page warning at 36 hours; retention; host-owned archives adopted into the `backups` volume at cutover; `restore.sh` restoring the dump and the uploads together inside the recovery fence. The host cron (`deploy/digitalocean/backup.sh`) stays for older deployments. Real DigitalOcean checks on Nov 17, 2026 covered provision, a backup-service run, `restore.sh --dump --uploads`, a second-server restore, and `upgrade.sh` and `--rollback` between two revisions with the same application tree. They did not cover a schema-changing upgrade. `docs/FIRST-CHURCH-CHECKS.md` keeps checks for a real church's domain, bucket and devices.
- Depends on: M5.1
- Scope: 11.8 for Compose: a small `ghcr.io/grierj/fold-backup` image (postgres:16-alpine plus `age` and `rclone`) with `backup.sh` (nightly loop and `now`) and `restore.sh`, `LAST_OK`, retention.
- Acceptance:
  - [ ] Nightly dump and uploads tar appear with the retention rule applied
  - [ ] `docker compose exec backup backup.sh now` makes a backup on demand
  - [ ] `restore.sh` brings back a working site on the standard real deployment
  - [ ] The System page warns when the last backup is older than 36 hours
- Tests: real deployment restore; retention unit test. A release CI restore test remains outstanding.
- Not an acceptance requirement (deferred, no milestone): encrypted offsite uploads. See the "Desired, no milestone" row in section 14.2 of the plan and DEC-152. The code that can upload `age`-encrypted pairs stays in `backup.sh` and the image and was exercised once against a disposable S3-compatible server; its completion (a real producer-only bucket policy, real `age` decryption on a workstation, B2 and Spaces behaviour) is not part of this milestone.

**M5.3 Upgrades**
- Partly done: `deploy/digitalocean/upgrade.sh` dumps the database before a forward upgrade and can roll back (`--rollback`). Not built: the app's own backup check before migrating, `docs/UPGRADING.md`, and the release-to-release CI test.
- Depends on: M5.2
- Scope: 11.7. Backup check before migrating (skipped on first install or with no pending migrations; no off switch), exit non-zero on failure, `docs/UPGRADING.md`.
- Acceptance:
  - [ ] Upgrading from the previous release with seeded data passes in CI (from v0.5 on)
  - [ ] A first install with an empty `backups` volume starts without a backup
  - [ ] With pending migrations and no backup newer than 24 hours, the app exits with a clear message
  - [ ] A failing migration leaves the data unchanged, and the app exits non-zero and shows unhealthy
- Tests: upgrade test in CI.

**M5.4 Deployment docs (Compose)**
- Partly done: `docs/DEPLOY-DIGITALOCEAN.md` (the operator guide for one VPS), `docs/agents/deploy.md` (the agent guide that AGENTS.md points to), the README install steps, and the provision, smoke and upgrade scripts in `deploy/digitalocean/`. `docs/DEPLOY.md` does not exist yet.
- Depends on: M5.1–M5.3
- Scope: `docs/DEPLOY.md` for humans, and the agent deployment guide (`docs/agents/deploy.md`; the original outline is [Appendix A](../provenance/appendix-a-agents-deployment.md)).
- Acceptance:
  - [ ] A person following DEPLOY.md on a fresh 2 GB VPS reaches the wizard in under 30 minutes (manual check, recorded in the PR)
  - [ ] An agent following `docs/agents/deploy.md` completes the same run (`fold setup --dry-run` joins this check once M4.8 lands in v0.8)
- Tests: docs link check.

**M5.5 Render Blueprint (cut, DEC-140)**
- Cut from the roadmap (DEC-140, DEC-142); the 12.5 evaluation stays as reference. It re-enters only as a new milestone for a named church.
- Depends on: M5.4, M4.1
- Scope: `deploy/render.yaml` (the Render outline in [Appendix A](../provenance/appendix-a-agents-deployment.md)), `docs/DEPLOY-RENDER.md`, and the app details Render needs: `PORT`, the Render value for `FOLD_TRUSTED_PROXIES`, a way to skip the Compose backup check where Render's PITR covers upgrades, base64 secrets.
- Acceptance:
  - [ ] `render.yaml` passes `render blueprints validate` (or validation through Render's API) in CI or in the PR notes
  - [ ] A person following DEPLOY-RENDER.md, in a Render workspace the church owns, reaches the wizard with a custom domain and TLS (manual check, recorded in the PR)
  - [ ] Client IPs are real behind Render's proxy (Admin → System has no IP check today; this milestone would add one)
  - [ ] Docs list the monthly cost from 12.5, the disk trade-off, and the S3 option
- Tests: blueprint validation; docs link check.
