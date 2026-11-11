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

Practice with `backup.sh --restore-test` before you need a real restore; it
loads the newest dump into a throwaway database and never touches `fold`. It
does not rehearse `restore.sh`.

The full `restore-drill.sh` is a development verification, not a routine backup
check. Run it only when changes have a high chance of altering the restore behavior it checks,
when the drill itself changed, or when its last run was more than 30 days ago.
Record the reason and date with the evidence; use targeted checks otherwise.

### Restoring a nightly dump (interim notes)

These notes are interim and the steps are recommended, not a promise. They
describe `/opt/fold/restore.sh` as written in this tree. Local drills on a
disposable Compose stack have exercised it; the checks on a real droplet in
[FIRST-CHURCH-CHECKS.md](FIRST-CHURCH-CHECKS.md#live-install-m51-m53) have not
been done (#310). No release that reports `recoveryPolicy` 1 or higher may be
published until they are. Account erasure still has open work (see `STATUS.md`,
M7.4); nothing here says a restore brings back or removes every trace of anyone.

**Why a procedure.** A dump knows nothing about people deleted after it was
taken, so restoring it brings them back as active accounts. `restore.sh` keeps
the public site closed and queued background deliveries paused while you delete them again from the
church's own records. It never restores into the live `fold` database
(`pg_restore --clean` into a live database can leave newer objects behind).
The old manual `docker compose stop app` plus `pg_restore --clean` block is
retired; do not use it.

Run the installed `/opt/fold/restore.sh` as root. On its first use it checks
that the complete tool set sits beside it (otherwise it stops, names the missing
file and tells you to run the current `provision.sh`), copies the set into a
root-owned `/opt/fold/recovery-tools`, and re-runs from there. A rejected
preflight leaves the installed recovery tools unchanged.

**Before you start** (every item is checked before anything stops, unless
noted):

- The dump is a `pg_dump -Fc` file readable on the droplet. Decrypt offsite
  `.age` files first (see above). `pg_restore -l` must read it.
- Free space in the database volume is at least twice the live `fold` database
  plus ten times the dump.
- `127.0.0.1:18080` is free, and no `fold_restore_*` or `fold_pre_restore_*`
  database is left over from another run (it names them and stops; read what it
  says before you drop anything).
- `recovery.lock` does not exist. If it does, run `restore.sh --status`.
- The pinned app image exists in the local Docker store. If it does not, the
  floor check stops with a message to recover that exact image: pull the pinned
  release, or rebuild the pinned source commit with its recorded label. Do not
  guess a tag or commit, edit the floor or change the pin.
- To restore with a particular release, add `--image REF` (needs a source
  directory with `docker-compose.yml`, `Caddyfile` and `.env.example`, default
  `/root/fold`) or `--commit SHA` (needs a clean checkout of that commit in
  `--source-dir`). With neither, the recovery instance runs the image already
  pinned and nothing is pinned.
- A `backup.sh` run in progress delays this preflight for at most 120 seconds;
  nothing has been stopped yet. If it is still busy the restore exits 75: wait
  and retry.
- Keep a current dump if the live data matters. The old database is removed
  when you reopen, and this tool cannot bring it back.

**Run it:**

```bash
sudo /opt/fold/restore.sh --dump /var/backups/fold/fold-YYYYMMDD-HHMM.dump
```

In order, `restore.sh`:

1. Writes `/opt/fold/recovery.lock` (its durable state) before stopping
   anything. From then until the lock is removed, `backup.sh` and its expiry
   refuse (see "Backups are suspended" below).
2. Stops Caddy and the app, then requires that no other client is connected to
   `fold` (it waits up to 120 seconds for a running `backup.sh` dump to finish,
   otherwise it stops with the services still stopped).
3. Restores the dump into a new database, `fold_restore_<id>`, in one
   transaction. If that fails, `fold` has not been touched. The failed database
   is dropped and the phase is `collect-failed`; run `--abort` to reopen the
   original, or `--resume` to try again.
4. Counts the accounts the dump would bring back: the accounts that are deleted
   in the stopped live `fold` database and active in the restored copy. The ids
   are streamed and never written to disk. Selection never uses audit
   timestamps. An account created and deleted after the dump is not in the dump
   and is not counted. If the live database cannot be read, the count is
   `unknown`, and the church's records are the only list.
5. If you gave `--image` or `--commit`, pins that release (no service start),
   checks its recovery level and refuses if it is below the host's level.
6. Swaps the databases whole: `fold` becomes `fold_pre_restore_<id>` (closed to
   connections) and the restored database becomes `fold`, in one transaction.
   If the swap fails, `fold` is as it was, and nothing old lingers in the
   restored one.
7. Starts a private recovery instance: the pinned image, background work off
   (queued mail, push and scheduled jobs do not run), restart policy `no`, reachable only at
   `127.0.0.1:18080` on the droplet. Caddy and the app stay stopped.

Exit codes: 0 means the step finished (a plain `--dump` that exits 0 leaves the
site **closed**, in private recovery); 1 a refusal or failure. Check `--status`: `cleanup-failed` after a successful smoke check can leave the site open with backups suspended; run `--resume`. If closure failed, closure was not verified: inspect `--status` and resolve the stop or exclusion failure before relying on private recovery;
3 the dump was restored with `--reopen-if-none` but the site is **closed** in
private recovery (see below); 75 another maintenance operation holds
`/opt/fold/ops.lock`.

**Private recovery.** On your own computer:

```bash
ssh -N -L 18080:127.0.0.1:18080 <admin>@<droplet>
```

Open `http://localhost:18080` in Chrome or Firefox (they keep the secure
session cookie on `localhost`; Safari is not recommended), sign in as an admin
and delete again, in Admin → Users, every account that was deleted after the
dump, using the church's records. `restore.sh` printed how many; it saves no
list of names or ids. A list view of previously deleted accounts is not built
(it waits on an open owner question, R4-6e-R), so for an `unknown` count or a
nonzero one the church's records are the only source. Stay in this state as
long as you need while the public site stays closed. Background deliveries are paused, but some actions can send mail or push synchronously. Use this instance only for the account-deletion workflow described here; do not use email changes, composer test sends or push tests.

**Reopen.** When every one is deleted again:

```bash
sudo /opt/fold/restore.sh --reopen --checked-church-records
```

`--checked-church-records` is required when the count is above zero or unknown.
It is your statement that you re-deleted everyone; the script cannot check it.
It prints the count again. With a count of exactly zero, plain `--reopen`
works. Reopening removes the recovery instance, starts the app (background work
starts, still not public) and then Caddy, runs `smoke.sh`, removes the old
database `fold_pre_restore_<id>` after checking its identity, records the new
recovery floor (it never goes down) and removes `recovery.lock` last. If any
step up to smoke fails, Caddy and the app are stopped again, the phase is
`reopen-failed`, and `--resume` returns to private recovery.

`--reopen-if-none` (used by `upgrade.sh --rollback`) runs the same reopen on its
own only when the count is exactly zero. Otherwise it stops after the recovery
instance is running, prints `SITE IS CLOSED` and exits 3.

**Backups are suspended during recovery.** While `recovery.lock` exists,
`backup.sh` (and the 02:30 cron run) exits 75 without a dump, a restore test,
expiry or an offsite upload, and nothing retries it. `LAST_OK` goes stale and
old dumps are not expired until the first backup after reopen. That first
backup is not automatic: run `sudo /opt/fold/backup.sh` by hand once the site
is reopened. `backup.sh` uses its own `backup.lock`, never `ops.lock`.

**One operation at a time, and never delete a lock.** Another provision,
upgrade, rollback or restore exits 75 while one runs; `/opt/fold/ops.owner`
shows who. Never delete, rename or replace `ops.lock`, `backup.lock` or
`recovery.lock` to get past a refusal. A killed run frees `ops.lock` by itself;
`recovery.lock` is the record `--resume` and `--abort` read.

**If something stops partway.**

- `sudo /opt/fold/restore.sh --status` is read-only and takes no lock. It prints
  the phase, which databases exist (name, oid, size, whether they accept
  connections), the containers and their restart policies, the ports, the
  count and the operations holder. Its observations may be mid-change if an
  operation is running.
- `--resume` never trusts the recorded phase or a command's exit status. It
  first observes the databases (by name and oid), the release pin and the
  containers, and then continues. It needs the dump file again (same
  checksum) only until the restore has been collected; after that it does not.
  If a reopen was interrupted before the site became public, `--resume`
  closes the site first (stops Caddy, the app and any recovery instance,
  checks the ports and the connections), then returns to private recovery,
  and you run `--reopen` again. This holds even if the old database had
  already been removed. Once smoke passed and cleanup began, `--resume`
  instead finishes the reopen (it reruns smoke); if smoke fails there, it
  closes again and the next `--resume` returns to private recovery.
- `--abort` is allowed only before the swap, and only while the release pin is
  unchanged: from `quiesce` through `swap-intent`, including `collect-failed`
  and `swap-failed`. It re-enables `fold`, drops the half-built restored
  database, removes any recovery container, starts the original release and
  runs smoke. It is refused after the swap, after a pin changed anything, and
  in `reconcile-needed`. An abort that is interrupted or fails (a failed drop,
  start or smoke) is finished only by `--abort`; `--resume` is refused during
  an abort. A drop that fails because a connection still holds the restored
  database is retried by `--abort` once that connection is gone.
- `reconcile-needed` means Fold found databases, a release pin or a container
  in a state it did not record. It changed nothing in any database and started
  nothing; it closed the site and removed any recovery container. Read
  `--status`. Never rename or drop `fold` databases by hand. `--resume` only
  rechecks: when the observations are accepted again it restores the earlier
  phase and tells you to run `--resume` once more. `--abort` is refused.
- After a droplet reboot or a Docker daemon restart in private recovery, the
  app and Caddy stay stopped and the recovery container is stopped (restart
  policy `no`); `docker compose ps -a` shows it. `--resume` removes and
  recreates it. If a reboot hits an interrupted reopen, the app and Caddy may
  come back running; `--resume` closes them before it restarts private
  recovery. These two droplet cases are listed in
  FIRST-CHURCH-CHECKS.md and have not been checked on a real droplet.

**A lower recovery level is refused on every scripted path.** `provision.sh`,
a forward `upgrade.sh --image` or `--commit`, `upgrade.sh --rollback` and
`restore.sh --image` or `--commit` refuse a target below the host's recovery
level, before any backup, checkout or pin, and name the restore barrier. There
is no override. Restore the data with the current release instead (run
`restore.sh --dump` with no lower `--image` or `--commit`).

**Rollback.** `upgrade.sh --rollback` no longer restores by itself. After its
own level check it runs `restore.sh --dump <the dump recorded in release.prev>
--reopen-if-none` with the previous image or commit. It reopens on its own only
at a count of exactly zero; otherwise it exits 3 with the site closed in private
recovery, and you finish as above. `--dry-run` prints that hand-off.

**Droplet Backups and snapshots are outside this procedure.** A disk rebuild
starts Docker with the restored disk, and Caddy, the app and mail start at once
with the backup's accounts and queued messages before anything on the host can
stop them. Use `restore.sh --dump` with a `backup.sh` dump as the recommended
restore. Use a Droplet Backup only when the machine and its dumps are gone; then
delete again, from the church's records, everyone deleted since that backup.
Treat the period between the rebuild and that cleanup as unprotected.

**What a dump brings back.** A dump taken after an erasure may restore the
scrubbed tombstone, which the daily legacy sweep processes again. A dump taken
before an erasure restores the person as an active account; neither that sweep
nor a count selects them, so you re-delete during recovery. No sentence here
promises every copy of anyone's data is gone: dumps, offsite copies, Droplet
Backups and snapshots keep what they held until they age out or you delete them.

**Gaps in this tooling, stated plainly.**

- `restore.sh` restores the database only. It does not touch the uploads volume
  or `uploads-*.tgz`; restore uploads yourself if files are missing, and the
  procedure for that is not covered or drilled here.
- After the swap there is no way back through this tool: `--abort` is refused.
  The old database exists only until `--reopen` removes it.
- Safari, and any browser that drops the secure cookie on `localhost`, is not
  recommended for the recovery instance.
- The local drills used a stand-in smoke test and disposable stacks. Caddy and
  TLS on a real domain, a droplet reboot and the browser steps are hand checks.

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

Rollback does not restore anything itself. After the recovery-level check it
hands off to `restore.sh --dump <recorded dump> --reopen-if-none` with the
previous image or commit, so the dump goes into a fresh database and the site
stays closed while you delete again anyone removed since that dump (see
"Restoring a nightly dump" in section 5). It reopens by itself only when the
count is exactly zero. Otherwise it exits 3 with the restore finished and the
site **closed** in private recovery, and you continue with `restore.sh`.
`upgrade.sh --rollback --dry-run` prints the hand-off. A Droplet snapshot taken
before a risky upgrade (the paid command in section 5) is a different, whole-disk
recovery that is outside this protection. Migrations run when the app starts. A
failed migration exits non-zero and `smoke.sh` fails. Inspect the failure before
choosing recovery. Rollback is subject to the recovery floor even after failed
smoke.

### Recovery refusal and interrupted operations

Every scripted deploy, forward upgrade and rollback refuses a target below
the host's recovery level. Level 1 requires the restore barrier; level 2 also
requires reason review. The scripts read integer `recoveryPolicy` from an
image's `/fold version --json` output, or read the one-line
`deploy/recovery-policy` file at a source commit. Missing release metadata means level 0;
malformed metadata refuses. The host floor is the greater of `recovery-policy-floor`
and the level of the image its current Compose pin names. There is no override.
The floor file is recorded only after smoke passes and never decreases.

This tree reports level 2 (`deploy/recovery-policy`). Never publish a release reporting `recoveryPolicy >= 1`
before R4-6c-R's current-release restore tooling and operator procedure land
and are verified. The local refusal drills do not verify that restore procedure
or complete the [real-droplet checks](FIRST-CHURCH-CHECKS.md#live-install-m51-m53).

If a higher-level image starts but smoke fails, its pin already contributes
to the floor. `--rollback` to a lower-level previous release is refused even
if no floor file was written. Keep the current release, inspect service health,
and repair it or restore data with that release's recovery tooling. Use
`/opt/fold/restore.sh --dump PATH` and the notes in section 5. If the installed
set has no `restore.sh`, run the current `provision.sh` to install the complete
tool set (a copy of `restore.sh` run by hand refuses and names the missing
file). Do not substitute the older rollback script, edit the floor, or change
the pin to bypass refusal. The same refusal applies to `restore.sh --image` and
`--commit`: restore with the current release, not a lower one.
Provision, upgrade and a new `restore.sh --dump` also exit non-zero before
changing anything while `/opt/fold/recovery.lock` exists; run `restore.sh --status`.

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
- **Rolling back.** `upgrade.sh --rollback` hands the dump taken before the
  upgrade to `restore.sh`, which restores it into a fresh database and starts
  the previous image or commit, as above. The test
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
