# Deploy Fold on one DigitalOcean droplet

Fold runs as one Docker Compose stack on one virtual machine: Caddy for
HTTPS, the Fold app, and Postgres 16. That is the recommended shape. The
church owns the DigitalOcean account, the domain, and the email account.

`deploy/digitalocean/provision.sh` hardens a fresh Ubuntu droplet and
starts the stack. It is idempotent. `--dry-run` prints every action and
changes nothing. It never calls `doctl` and never creates a droplet, a
Cloud Firewall, a snapshot, a Droplet Backup plan, or an object-storage bucket.
Those commands are below so a person can run them in the church's account.

Prices below were checked in October 2026 against
[DigitalOcean droplet pricing](https://www.digitalocean.com/pricing/droplets).
Re-check before telling a church a number.

## What you need

- A domain the church controls (`groups.example.org`).
- An SSH key pair. Droplets are created with that public key and no root password.
- The church's DigitalOcean account. A helper can be added as a team member.
  The helper does not open the account in their own name.
- Email, in the church's name. Postmark is the recommendation. Any SMTP
  host works, including one the church already pays for. Fold starts
  without it and then refuses to send password resets and invitations
  until `FOLD_SMTP_URL` is set.
- SMS is optional. Fold runs with email alone. Telnyx is the recommendation
  when a church wants texting, on the church's own account. Twilio can be
  added later and is not required to bring a site up.

The database is Postgres in the Compose file (`postgres:16-alpine`, user
`church`, database `fold`). That service is the only database.

## Size

This repo's `docker-compose.yml` pulls the published image
`ghcr.io/grierj/fold:${FOLD_VERSION}` (`--image`); it does not build.
With a pulled image the 1 GB size, with swap, is the floor and 2 GB stays
the recommendation. Building the Node and Go stages on a 1 GB machine can
run out of memory, so the on-droplet build (`--allow-onbox-build`, which
also copies `docker-compose.build.yml`) needs the 2 GB size. No separate
build machine is part of this guide.

**No release is published yet.** Until the first release exists there is
no image to pull: use the 2 GB size with `--allow-onbox-build`, as the
commands below do, and switch to `--image ghcr.io/grierj/fold:x.y.z` when
a release is announced.

| Droplet | Slug | Disk / transfer | When |
|---|---|---|---|
| 2 GiB, 1 vCPU, $12/mo | `s-1vcpu-2gb` | 50 GiB, 2,000 GiB | Recommended. Room for Postgres, Caddy, the app, and an on-droplet build. |
| 1 GiB, 1 vCPU, $6/mo | `s-1vcpu-1gb` | 25 GiB, 1,000 GiB | The floor, with a pulled image only. Add `--swap-mb 1024`. Needs a published release, so not before the first one. |
| 2 GiB, 2 vCPU, $18/mo | `s-2vcpu-2gb` | 60 GiB, 3,000 GiB | When the 1 vCPU size is busy. |

Weekly Droplet Backups add 20% of the droplet price. Daily adds 30%.
Snapshots are $0.06 per GB-month. Offsite dumps use Backblaze B2, a few
cents at this size (first 10 GB free, then $6.95 per TB per month:
[B2 pricing](https://www.backblaze.com/cloud-storage/pricing)). Spaces
works instead and starts at $5/month. Cloud Firewalls and the metrics
agent are free. Pick a region near the church; `nyc3` below is only an
example.

## 1. SSH key and the droplet

Create a key locally if you do not have one (`ssh-keygen -t ed25519`).
In the control panel: Settings → Security → Add SSH Key.

List the fingerprint, then create the droplet. This spends money. Run it
in the church's account. `provision.sh` will not run it for you.

```bash
doctl compute ssh-key list
doctl compute droplet create fold \
  --image ubuntu-26-04-x64 \
  --size s-1vcpu-2gb \
  --region nyc3 \
  --ssh-keys "$SSH_KEY_FINGERPRINT" \
  --enable-ipv6 \
  --enable-monitoring \
  --enable-private-networking \
  --enable-backups
```

`--enable-backups` turns on weekly Droplet Backups and the 20% charge.
Omit that flag to skip it. `ubuntu-24-04-x64` is the other image this
guide's script accepts. `--enable-monitoring` is the free metrics agent.

Wait until the address is assigned:

```bash
doctl compute droplet list fold
```

Log in once as root, with the key, to confirm the droplet is yours before
hardening it: `ssh root@THE_ADDRESS`.

## 2. DNS

At the church's DNS host, add:

- `A` for `groups.example.org` to the public IPv4 address.
- `AAAA` to the public IPv6 address when IPv6 is enabled.

Caddy gets a certificate over HTTP-01, which needs port 80 to reach this
droplet. If the domain is on Cloudflare, leave the record DNS-only (grey
cloud). An orange-cloud proxy answers on 80/443 itself and the certificate
request fails.

DigitalOcean DNS is free if the church wants the records there. The
script does not create them.

## 3. Cloud Firewall

A Cloud Firewall is the edge. Docker publishes ports by editing iptables
itself, so host `ufw` does not see those packets. `ufw` still limits
anything that binds on the host. Use both. The compose file does not
publish Postgres or the app. `smoke.sh` fails the deploy if 5432 or 8080
is published on a public address or accepts a connection there.

Inbound: TCP 22, 80, and 443. Outbound: the usual all-traffic default,
so the droplet can reach Ubuntu mirrors, Let's Encrypt, the email
provider, and an offsite bucket.

Restrict SSH to a known office address when the church has one
(`address:203.0.113.4/32` is the documentation range; use the church's
real address). The command below allows SSH from the public internet and
relies on key-only login plus fail2ban.

```bash
# Free, but still a resource in the church's account. Run it yourself.
doctl compute firewall create \
  --name fold \
  --inbound-rules "protocol:tcp,ports:22,address:0.0.0.0/0 protocol:tcp,ports:22,address:::0/0 protocol:tcp,ports:80,address:0.0.0.0/0 protocol:tcp,ports:80,address:::0/0 protocol:tcp,ports:443,address:0.0.0.0/0 protocol:tcp,ports:443,address:::0/0" \
  --outbound-rules "protocol:tcp,ports:all,address:0.0.0.0/0 protocol:tcp,ports:all,address:::0/0 protocol:udp,ports:all,address:0.0.0.0/0 protocol:udp,ports:all,address:::0/0 protocol:icmp,address:0.0.0.0/0 protocol:icmp,address:::0/0" \
  --droplet-ids "$DROPLET_ID"
```

`address:::0/0` is the key `address` and the value `::/0`. The same rules
can be clicked together in the control panel
([firewall create](https://docs.digitalocean.com/reference/doctl/reference/compute/firewall/create/)).

## 4. Harden and start Fold

Keep the root session open. Use a second terminal for the `deploy` user
at the end, and close root only after that login works.

On the droplet:

```bash
apt-get update && apt-get install -y git
git clone https://github.com/grierj/fold.git /root/fold
```

Preview the whole plan:

```bash
/root/fold/deploy/digitalocean/provision.sh --dry-run \
  --domain groups.example.org \
  --timezone America/Chicago \
  --allow-onbox-build
```

`--allow-onbox-build` is the path until the first release is published:
it copies `docker-compose.build.yml`, sets `COMPOSE_FILE` in `.env` so
plain `docker compose` commands include it, and Compose builds the image
on the droplet. Once `ghcr.io/grierj/fold` has a release, drop that flag
and pass `--image ghcr.io/grierj/fold:x.y.z` instead; the tag becomes
`FOLD_VERSION` in `.env`, the build override is removed, and nothing builds
on the droplet. Pin the exact version (`0.5.0`, not the moving `0.5`; the
scripts refuse it). Do not deploy `:latest`. Each GitHub release lists the
image digest and a link to its build provenance.

Apply it:

```bash
/root/fold/deploy/digitalocean/provision.sh \
  --domain groups.example.org \
  --timezone America/Chicago \
  --allow-onbox-build
```

The script will:

1. Install `ufw`, `fail2ban`, `unattended-upgrades`, and (when the package exists) `age`.
2. Create the `deploy` user (override with `--admin-user`), lock its password, and install the root SSH key for it. `sudo` does not ask for a password, because the account has no password.
3. Write `/etc/ssh/sshd_config.d/00-fold-hardening.conf` so root login and password login are off, then reload `sshd` only if `sshd -t` passes. The file is named `00-` so it is read before `50-cloud-init.conf` (sshd keeps the first value).
4. Enable `ufw` for 22, 80, and 443, with incoming denied otherwise.
5. Enable a fail2ban sshd jail (5 retries, 1 hour ban).
6. Turn on unattended security upgrades. The machine does not reboot itself. After a kernel update, `/var/run/reboot-required` appears; reboot in a quiet window.
7. Install Docker Engine and the Compose plugin from Docker's Ubuntu repository when `docker compose version` is not already working.
8. Copy the stack to `/opt/fold` (with `--image`: only `docker-compose.yml`, `Caddyfile` and `.env.example`; with `--allow-onbox-build`: the whole checkout, including `docker-compose.build.yml`), pin `FOLD_VERSION` in `.env`, write `/opt/fold/.env` mode 600 with a new `SESSION_SECRET` and `DB_PASSWORD`, and refuse to continue if that file is not mode 600 or is committed in git.
9. Install `/opt/fold/backup.sh` and a cron job at 02:30 in the droplet's timezone.
10. After Compose is up, run `smoke.sh`: wait until the db and app containers are healthy, request `https://$DOMAIN/healthz`, check the TLS certificate, and fail if 5432 or 8080 is reachable. It also runs `fold push check` and warns, without failing, if the app can't reach a browser push service on port 443. The same script runs from cron every five minutes (`logger -t fold-health` on failure).
11. Read `sshd -T` and fail if root login or password login is still on. Fail if `ufw` is not active.

A second run does not rotate `.env` secrets, does not duplicate SSH keys, and does not replace `/swapfile` or `backup.env`. Pass the same `--domain`. To change the hostname, edit `.env` yourself; the script stops if the flag and the file disagree. `:latest` is refused, both as `--image` and as `FOLD_VERSION` in `.env`.

Set the church's time zone so the 02:30 backup is local. `--swap-mb 1024` adds a swap file on a 1 GiB droplet. `--smtp-url` and `--email-from` are written only when `.env` is first created; editing `.env` avoids leaving the SMTP password in shell history.

From the second terminal:

```bash
ssh deploy@groups.example.org
sudo /opt/fold/smoke.sh
```

Open `https://groups.example.org/` and enter the setup code from `sudo docker compose --project-directory /opt/fold logs app`. Keep the code private. Then create the site and first admin. If the app restarts before setup is complete, use the new code from its logs. `GET /healthz` answers only after Postgres is up.

Then set the church's mail. Any SMTP host works. Postmark is the
recommendation. `email-dns.sh` does nothing unless the tokens are set,
and it never creates a Postmark account or a Cloudflare zone:

```bash
sudo --preserve-env=POSTMARK_ACCOUNT_TOKEN,CLOUDFLARE_API_TOKEN,CLOUDFLARE_ZONE_ID,FOLD_MAIL_DOMAIN \
  /opt/fold/email-dns.sh --domain groups.example.org
```

`POSTMARK_ACCOUNT_TOKEN` is the account token. The script creates the
sender domain if it is not there yet. With `CLOUDFLARE_API_TOKEN` and
`CLOUDFLARE_ZONE_ID` it writes the SPF TXT, the DKIM TXT, and the
return-path CNAME, DNS-only. A second run does not add a second copy.
If an SPF record already exists and does not include `spf.mtasv.net`,
the script stops and leaves it for you to edit. Without the Cloudflare
variables it prints the records instead. It then runs `dig`. The same
commands work from any machine:

```bash
dig +short TXT groups.example.org
dig +short TXT <selector>._domainkey.groups.example.org
dig +short CNAME pm-bounces.groups.example.org
```

The checked-in Compose app service passes `FOLD_SMTP_URL`, the Postmark settings and
`FOLD_EMAIL_FROM` from `.env` into the container. Recreate the app after changing
these values and verify delivery before relying on invitations or resets.

Put the SMTP URL in `/opt/fold/.env` (Postmark's host is
`smtp.postmarkapp.com`, port 587):

```bash
FOLD_SMTP_URL=smtp://SERVER_TOKEN:SERVER_TOKEN@smtp.postmarkapp.com:587
FOLD_EMAIL_FROM=Fold <noreply@groups.example.org>
```

Instead of SMTP, Fold can use Postmark's API directly, which keeps announcement
mail on its own message stream and lets Fold hear about bounces.
Put the same server token in `.env` instead of the SMTP URL:

```bash
FOLD_POSTMARK_SERVER_TOKEN=SERVER_TOKEN
FOLD_EMAIL_FROM=Fold <noreply@groups.example.org>
# Optional: the stream ids, if yours differ from Postmark's defaults.
# FOLD_POSTMARK_TRANSACTIONAL_STREAM=outbound
# FOLD_POSTMARK_BROADCAST_STREAM=broadcast
```

With a token set, Fold sends through the API. `FOLD_EMAIL_PROVIDER=smtp` (or
`postmark`) picks one explicitly when both are set.

To hear about bounces and spam complaints, add a webhook in Postmark (Servers →
your server → Webhooks) for **Delivery**, **Bounce** and **Spam Complaint**,
with Basic authentication in the URL, and put the same password in `.env`:

```bash
# Make the secret with `openssl rand -hex 24` (letters and digits only, so it
# fits in a URL), then use this webhook URL in Postmark:
# https://postmark:SECRET@groups.example.org/hooks/postmark/email
FOLD_POSTMARK_WEBHOOK_SECRET=SECRET
```

A hard bounce then stops all mail to that address; a spam complaint stops
optional mail, while account mail (password resets, sign-in links) still goes
from Fold. Postmark keeps its own list too: it refuses to send to an address
that hard-bounced until you reactivate it in Postmark, and to one that marked
mail as spam at all. For more protection, allow only Postmark's webhook IP
addresses to reach `/hooks/postmark/email` at your firewall or proxy.

Run `sudo docker compose --project-directory /opt/fold up -d` to recreate the
app with the changed mail configuration.
SMS is not implemented yet; do not provision texting just to run this version.

`SESSION_SECRET` must be at least 32 characters. The script generates one
with `openssl rand -base64 48`. Store `.env` in the church password manager.
Losing it signs everyone out and, later, loses encrypted provider secrets.
Every app container on one database must share the same SESSION_SECRET;
changing it turns off notifications on everyone's devices.

The Compose network is `172.30.0.0/24`, which is the default
`FOLD_TRUSTED_PROXIES`. Change the subnet and that variable together.

## 5. Backups and a restore test

Use three copies. They cover different failures.

**Droplet Backups or a snapshot** rebuild the whole disk, including a
Postgres data directory that may have been mid-write. Turn weekly backups
on at create time (`--enable-backups`) or later:

```bash
# Paid. Weekly Droplet Backups, 20% of the droplet price.
doctl compute droplet-action enable-backups "$DROPLET_ID"
```

Take a snapshot before an upgrade (stored until you delete it, billed
per GB):

```bash
doctl compute droplet-action snapshot "$DROPLET_ID" \
  --snapshot-name "fold-$(date +%Y%m%d)"
```

**Nightly `pg_dump`** is the consistent database copy. Cron runs
`/opt/fold/backup.sh` at 02:30. It writes mode-600 files in
`/var/backups/fold`:

- `fold-YYYYMMDD-HHMM.dump` from `pg_dump -Fc` of database `fold`
- `uploads-YYYYMMDD-HHMM.tgz` from the `fold_uploads` volume
- `LAST_OK` after a successful run

Daily files are kept for 14 days. A dump whose name falls on the 1st of
the month is kept for 180 days. Run one now:

```bash
sudo /opt/fold/backup.sh
sudo /opt/fold/backup.sh --restore-test
```

`--restore-test` loads the newest dump into a database named
`fold_restore_test`, counts public tables, and drops that database. It
does not replace the live `fold` database. Run it after the first real
dump and again after the first upgrade.

**Offsite.** A disk in the same region is not a backup of the region.
Backblaze B2 is the recommendation: it is the cheapest store in the plan
for a small backup. Any S3-compatible bucket works the same way, including
DigitalOcean Spaces. Create the bucket and a write-only key in the church's
account (control panel). The script will not create them.

On a laptop, not the server:

```bash
age-keygen -o fold-backup.key
```

Put `fold-backup.key` in the password manager. Copy the `age1...` public
key into `/opt/fold/backup.env` (start from
`deploy/digitalocean/backup.env.example`), mode 600. Set the bucket,
endpoint, and region. The example file uses B2:
`https://s3.us-west-004.backblazeb2.com`, region `us-west-004`.
A Spaces bucket uses `https://nyc3.digitaloceanspaces.com`, region `nyc3`.

The next backup encrypts with that public key and uploads only the
`.age` files, using the `amazon/aws-cli` image. Plaintext dumps stay on
the droplet. If `age` is missing or the recipient is empty, nothing is
uploaded.

Restore the live site only when you mean to replace it. Stop the app,
load a dump, start the app:

```bash
cd /opt/fold
sudo docker compose stop app
# Decrypt offsite ciphertext on a trusted machine first:
#   age -d -i fold-backup.key -o fold.dump fold.dump.age
sudo docker compose exec -T db \
  pg_restore -U church -d fold --clean --if-exists --no-owner < /var/backups/fold/fold-YYYYMMDD-HHMM.dump
sudo docker compose start app
curl -fsS https://groups.example.org/healthz
```

Practice that against `backup.sh --restore-test` before you need the
real one. A Droplet Backup restore is a control-panel action on the
whole disk; do it when the virtual machine itself is the thing that broke.

## 6. Upgrades and rollback

Pin every upgrade to an image tag or a git commit. `:latest` is refused.
Read the release notes first. `README.md` records operator steps (for
example the `SESSION_SECRET` length).

Run the installed `/opt/fold/upgrade.sh`. It uses its enforcing copy in
`/opt/fold/deploy-tools`, checks the target before backup or checkout, and
runs that copy's provisioner. A target checkout supplies build inputs, never
the provisioner. A script run by hand from an older checkout can bypass these
checks. Do not use it to recover a deployment.

On a pulled image (this changes `FOLD_VERSION` in `/opt/fold/.env`; there is
no image to pull until the first release is published):

```bash
sudo /opt/fold/upgrade.sh \
  --image ghcr.io/grierj/fold:x.y.z \
  --source-dir /root/fold
```

On a build-based install, name the commit:

```bash
sudo /opt/fold/upgrade.sh \
  --commit abcdef1234567 \
  --source-dir /root/fold
```

The script runs `backup.sh`, writes the current tag or commit and the new
dump path to `/opt/fold/release.prev` (mode 600), then runs `provision.sh`,
which runs `smoke.sh`. `.env` is left in place.

When the previous release meets the host's recovery floor, roll back to that
tag or commit and restore the dump it recorded:

```bash
sudo /opt/fold/upgrade.sh --rollback
```

Rollback stops the app, runs `pg_restore` of the recorded dump into the
`fold` database, starts the previous image or commit, and runs `smoke.sh`
again. Take a Droplet snapshot before a risky upgrade (the paid command
in section 5). Migrations run when the app starts. A failed migration
exits non-zero and `smoke.sh` fails. Inspect the failure before choosing
recovery. Rollback is subject to the recovery floor even after failed smoke.

### Recovery refusal and interrupted operations

Every scripted deploy, forward upgrade and rollback refuses a target below
the host's recovery level. Level 1 requires the restore barrier; level 2 also
requires reason review. The scripts read integer `recoveryPolicy` from an
image's `/fold version --json` output, or read the one-line
`deploy/recovery-policy` file at a source commit. Missing release metadata means level 0;
malformed metadata refuses. The host floor is the greater of `recovery-policy-floor`
and the level of the image its current Compose pin names. There is no override.
The floor file is recorded only after smoke passes and never decreases.

R4-RP is staged locally. Never publish a release reporting `recoveryPolicy >= 1`
before R4-6c-R's current-release restore tooling and operator procedure land
and are verified. The local refusal drills do not verify that restore procedure
or complete the [real-droplet checks](FIRST-CHURCH-CHECKS.md#live-install-m51-m53-agentsmd-deployment).

If a higher-level image starts but smoke fails, its pin already contributes
to the floor. `--rollback` to a lower-level previous release is refused even
if no floor file was written. Keep the current release, inspect service health,
and repair it or restore data with that release's recovery tooling. Use
`/opt/fold/restore.sh --dump PATH` once that tool is installed, following its
recovery instructions. If it is absent, stop and obtain the current release's
recovery procedure. Do not substitute the older rollback script, edit the
floor, or change the pin to bypass refusal.

If the floor check cannot resolve Compose configuration, repair
`/opt/fold/docker-compose.yml` and `/opt/fold/.env` before retrying. If the
pinned image is missing locally, recover that exact pinned image first.
For an image release, pull its pinned reference. For a source build, recover
the recorded source commit and rebuild it with the recorded image label.
Do not guess a replacement commit or tag. Version inspection uses the local
image with `--network none --pull never`; the floor check never pulls an image
or falls back to the floor file alone.

Exit 76 means the new stack deployed and smoke passed, but writing or syncing
the floor file failed. Keep the pin and local image. Repair storage or file
permissions, then rerun the installed `provision.sh` with the same release,
source directory, domain and deployment options. This retries the durable
floor write even if the file already contains the level. It is not a rollback
instruction.

Only one operation can hold `/opt/fold/ops.lock`. Another provision, upgrade
or rollback exits 75 before reading release or database state. `ops.owner`
shows an informational pid, mode and start time. Never delete, rename or
replace `ops.lock`. Its inode must stay the same. File descriptor 9 is inherited
by the enforcing tool and its children. A surviving helper can therefore keep
the lock held after the recorded pid exits. If exit 75 persists, run
`sudo fuser -v /opt/fold/ops.lock`, inspect those processes and stop only a
verified abandoned operation or helper. Retry after it exits. Do not kill
every process from `fuser` without checking what it is doing.

The enforcing tool set has an integer `deploy-tools-version`. The new tools
refuse to replace a higher installed version; missing metadata means version
0. This orders tool protocols, not commits within the same version. Use the
installed entry point rather than an older checkout to keep the current tools.
To install a newer tool set, first update the trusted source checkout, then run
that checkout's `deploy/digitalocean/upgrade.sh` or `provision.sh`. Its tool
version must be at least the installed version. An installed upgrade otherwise
continues to use its existing enforcing copy even when the target checkout is newer.
If that entry point itself reports an older tool version, use the newer
installed `/opt/fold/deploy-tools/upgrade.sh` named by the diagnostic.
The tool swap uses two directory renames, so it has an interruption window.
On the next installed `upgrade.sh` run, one complete saved
`.old-deploy-tools.*` set is restored if `deploy-tools` is absent. Stale stages
and saved sets are removed only after a live set is available. Ambiguous or
incomplete saved sets refuse with recovery instructions; preserve them and
restore the verified current tool set before retrying. The script does not
guess which of several saved sets is current.

`--dry-run` writes no files, takes no lock and never pulls or executes an
image. It reads a build target's recovery metadata and refuses a level below
the recorded floor. It explicitly marks the full host floor and image target
as unverified when they require executing an image. A missing checkout in a
dry-run rollback is also marked unverified. A successful dry run with these
messages does not prove that a real run will pass recovery checks.

`DEPLOY_DRILL=1` is test only. The disposable `deploy-drill.sh` harness creates
a root-owned mode-600 `.deploy-drill` marker bound to its scratch deployment
path. The scripts reject drill mode without that marker, under `/opt/fold`
including aliases and children, or beside a `backup.env`. Unset `DEPLOY_DRILL`
and `DEPLOY_DRILL_PAUSE` before operating a real deployment. Drill mode omits
host hardening, cron installation and email DNS setup; it uses local test
smoke and backup tools. It is not a production deployment option.

### Upgrade notes: suppression address hashes (M7.4 R4-S)

The release that adds migration `00002` adds a nullable `address_hash` column
to `suppressions` and fills it for every existing row. Read this before you
upgrade.

- **A lock for the length of the migration.** The migration takes an exclusive
  lock on the `suppressions` table and keeps it until the migration commits.
  The time grows with the number of rows; Fold keeps cleared suppressions
  forever, so a long-running church has more of them. The release PR records
  times measured on one host. Those are measurements, not limits.
- **On this guide's path nothing waits.** `upgrade.sh` and `provision.sh` run
  `docker compose up -d`, which stops the old app before the new one starts.
  The new app migrates before it listens, so `/healthz` does not answer
  until the migration finishes. The Compose health check may show the app as
  unhealthy for a while on a large table and recovers once it serves.
  `smoke.sh` waits 180 seconds by default; on a big table pass `--timeout`
  (up to 600) so it does not give up early.
- **Do not run `fold migrate up` by hand against a running server for this
  release.** Every mail send, message preview and notification email reads
  `suppressions`, so each one waits until the migration commits.
- **If the lock is not free within 5 seconds** the migration fails, applies
  nothing and the app does not start. That is the failed-migration case above:
  `smoke.sh` fails. Use `upgrade.sh --rollback` only if the previous release
  meets the recovery floor; otherwise follow the recovery instructions above.
- **Rolling back.** `upgrade.sh --rollback` restores the dump taken before the
  upgrade (`pg_restore --clean --if-exists`) and starts the previous image or
  commit, as above. The test
  `TestSuppressionExpandKeepsThePreviousBinaryWorking` also records that the
  previous release's migration runner treats the upgraded database (version 2)
  as current: it runs nothing and does not refuse it, so starting the previous
  image on that database needs no `fold migrate down-one`. Rows the previous
  release writes have no hash; the new release finds them by address.
- **The plaintext address stays.** This release stores the hash beside the
  plaintext address. The plaintext column is removed only by a later release, so
  suppressions are not hash-only yet.

## 7. Monitoring

- The metrics agent (`--enable-monitoring`) graphs CPU, memory, and disk in the control panel. Add alert policies there for sustained CPU, memory above about 85%, and disk above about 80%. Alerts are free; the script does not create them.
- `/etc/cron.d/fold-health` runs `/opt/fold/smoke.sh` every five minutes. Failures go to the system log (`journalctl -t fold-health`).
- `sudo docker compose --project-directory /opt/fold ps` and `logs app --tail 200` are the first place to look when the site is down.
- fail2ban's sshd jail is in `fail2ban-client status sshd`. Fold's own login throttling is separate and lives in Postgres.

Certificate problems are almost always DNS (the name does not point here yet), port 80 blocked, or a Cloudflare proxy in front of Caddy. `smoke.sh` fails when the certificate is missing, expired, for the wrong name, or issued by Cloudflare. `sudo docker compose --project-directory /opt/fold logs caddy --tail 100` shows the ACME error. The shipped Caddyfile does not set a contact email, so Let's Encrypt will not mail you when a certificate is close to expiry. The smoke check and the Caddy log are how you notice.

## Hetzner

Same stack, for a church that wants a European host. A CX23 is the small
size in the plan (about $6.49/mo after the June 2026 price change). US
locations cost more; use a DigitalOcean 2 GB droplet there instead.

In the Hetzner Cloud console, create a server with Ubuntu 24.04 or 26.04
and the church's SSH key. Add a firewall that allows inbound TCP 22, 80,
and 443, and attach it to the server. Point the DNS A and AAAA records at
the server. From section 4 on, the commands are the same `provision.sh`.
The script does not call the Hetzner API and does not create servers.

## Decisions

- **Images.** Pull a published, pinned release (`--image ghcr.io/grierj/fold:x.y.z`, from the GitHub release workflow). Until the first release exists, build on the droplet with `--allow-onbox-build` on the 2 GB size. No separate build setup.
- **Ubuntu.** The create command uses `ubuntu-26-04-x64`. The script also runs on 24.04 (`ubuntu-24-04-x64`). Hetzner uses the same script on those Ubuntu images.
- **Services.** One recommendation each, and another provider still works. Postmark for email (any SMTP host works). Telnyx for SMS when a church wants texting (Fold runs without it). Backblaze B2 for offsite dumps (any S3-compatible bucket, including DigitalOcean Spaces, works).
