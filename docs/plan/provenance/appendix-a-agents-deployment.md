# Fold provenance: original AGENTS.md deployment outline

## 16. Appendix A: AGENTS.md "Deployment" section outline

Add this to `AGENTS.md` in M5.4. Each item is a heading with short, exact instructions. The Render outline at the end is reference only and is not added to `AGENTS.md` unless M5.5 is started (DEC-26).

0. **Rules for agents**
   - Never buy, upgrade plans, or create accounts without the operator's typed confirmation in this session.
   - Provider and hosting accounts belong to the church (DEC-2). Ask the church to create them and give you an API key or a user seat. Never open them in your own or the helper's name.
   - Fold uses PostgreSQL only (DEC-1, DEC-24). Never configure SQLite. No Python anywhere.
   - Never echo secrets. Write `.env` with mode 600.
   - Always run `fold setup --dry-run` before `fold setup`. Run `docker compose exec backup backup.sh now` before upgrades.
   - Stop and ask when a step fails twice.
1. **Ask the operator first** (customization prompts)
   1. Church legal name and display name
   2. Domain or subdomain for Fold (for example `groups.example.church`), and who controls its DNS
   3. Church time zone and postal address
   4. First admin's name and email (the email is the login, DEC-23)
   5. Email provider (Postmark default; Resend, Mailgun, or SMTP) and whether they already have an account
   6. SMS: needed now? Which provider (Telnyx default; Twilio from v1.0)? Does the church have an EIN? (On Telnyx it is required; suggest the free IRS EIN.) 10DLC or toll-free? Never ask for 501(c)(3) proof (DEC-3).
   7. Account policy: self-signup with admin approval (default) or off (DEC-4); SMS send window (default 8 AM to 8 PM recipient-local, DEC-9, DEC-25); guardian copies on or off (default off, DEC-19)
   8. Storage: local or S3-compatible (and which), and offsite backup bucket plus an `age` public key
   9. Hosting: VPS provider and size, or an existing server, and whose account it is on

**Deployment: one VPS with Docker Compose (the only recommended path, DEC-26)**

2. **Preflight**: check the VPS is reachable over SSH; at least 2 GB RAM and 20 GB free disk; ports 80 and 443 open; Docker and the compose plugin installed (`docker compose version`); the domain's A/AAAA records point at the server (`dig +short`).
3. **Server prep**: create the `/opt/fold` directory; enable unattended security updates; firewall allows 22, 80, 443 only.
4. **DNS**: create the app record (DNS-only if on Cloudflare); optionally create a Cloudflare API token limited to DNS edit on this one zone, for the wizard.
5. **Install files**: download `docker-compose.yml`, `Caddyfile`, `.env.example`, and `init-env.sh` from the release matching `FOLD_VERSION`; run `init-env.sh <domain>`; add any provider env overrides.
6. **Start**: `docker compose pull && docker compose up -d`; wait until `docker compose ps` shows the app healthy; check `https://<domain>/healthz`.
7. **Configure**: write `setup.yaml` from the operator's answers; run `docker compose exec app fold setup --config /setup.yaml --dry-run`; show the plan to the operator; run it for real after they agree; or hand the operator the first-admin page and the `/admin/setup` wizard instead.
8. **Hand-off**: tell the operator to store `FOLD_SECRET_KEY` and `.env` in a password manager; confirm a test email arrived; show the setup checklist status; list anything pending (for example SMS registration review).
9. **Upgrades**: `docker compose exec backup backup.sh now`; set the new `FOLD_VERSION`; `docker compose pull && docker compose up -d`; check `/healthz` and the version; read `docs/UPGRADING.md` first.
10. **Troubleshooting**: `docker compose logs app --tail 200`; certificate problems (DNS not pointing, port 80 blocked, Cloudflare proxy on); email not verified (DNS propagation, check records); SMS blocked (registration status); restore from backup (`docker compose exec backup restore.sh <file>`, 11.8).

**Reference only, not recommended yet: Render (future M5.5, DEC-26)**

Kept so the work is ready if a church asks. Do not offer this path until M5.5 is done.

2. **Account**: the church creates a Render workspace (Hobby, or Pro if a helper needs their own seat) and a payment method. For an agent, the church creates an API key and shares it; the agent never logs in as the church.
3. **Blueprint**: copy `deploy/render.yaml` from the release matching `FOLD_VERSION`, fill in the domain and region, and validate it (`render blueprints validate`, or Render's API). Outline:

```yaml
services:
  - type: web
    name: fold
    runtime: image
    image:
      url: ghcr.io/grierj/fold:0.8.0          # pin x.y.z
    plan: 0.5c-512mb                          # $7/mo (12.5)
    region: oregon                            # or ohio, virginia, frankfurt, singapore
    healthCheckPath: /healthz
    domains:
      - groups.example.church
    disk:                                     # omit when using S3 storage (keeps zero-downtime deploys)
      name: uploads
      mountPath: /data
      sizeGB: 5
    envVars:
      - key: APP_ENV
        value: prod
      - key: SITE_URL
        value: https://groups.example.church
      - key: DATABASE_URL
        fromDatabase:
          name: fold-db
          property: connectionString
      - key: SESSION_SECRET
        generateValue: true                   # base64, 256-bit
      - key: FOLD_SECRET_KEY
        generateValue: true                   # copy it to the church's password manager
      - key: FOLD_TRUSTED_PROXIES
        value: render                         # app preset for Render's proxy (M5.5)
      - key: FOLD_BACKUP_CHECK                # M5.5 would add this skip switch for Render only
        value: "off"                          # Render PITR covers upgrades
      - key: FOLD_POSTMARK_SERVER_TOKEN
        sync: false                           # typed into the dashboard at creation
databases:
  - name: fold-db
    plan: 0.1c-256mb                          # $6/mo, or 0.5c-1g for $19/mo
    postgresMajorVersion: "16"                # match CI
    diskSizeGB: 5
    ipAllowList: []                           # no access from outside Render
```

4. **Create**: create the Blueprint from the repo or file in the church's workspace (dashboard or API). Enter the `sync: false` secrets when prompted. Migrations run when the app starts (advisory lock), so no pre-deploy command is needed.
5. **DNS**: add the CNAME (or A record) Render shows for the custom domain, remove any AAAA record, and wait for the certificate.
6. **Configure**: open the first-admin page and the `/admin/setup` wizard, or run `fold setup` as a Render one-off job.
7. **Hand-off**: store `FOLD_SECRET_KEY` (from Render's environment page) in the church's password manager; confirm a test email; note the backup coverage (PITR 3 days on Hobby, 7 on Pro; disk snapshots daily) and, if wanted, add the optional cron job for an offsite `pg_dump` with the `fold-backup` image.
8. **Upgrades**: change the image tag (in `render.yaml` or the dashboard) and deploy. A failed migration fails the deploy, and the previous version keeps running.
9. **Troubleshooting**: Render's service logs and events; `/healthz`; certificate status on the custom domain page; Admin > System for the client IP check.

