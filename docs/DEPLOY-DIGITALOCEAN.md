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

This repo's `docker-compose.yml` builds the image on the droplet
(`build: .`, `--allow-onbox-build`). Use the 2 GB size for that build.
A 1 GB droplet is enough only after a published image exists to pull
(`--image`). Building the Node and Go stages on a 1 GB machine can run
out of memory. No separate build machine is part of this guide.

| Droplet | Slug | Disk / transfer | When |
|---|---|---|---|
| 2 GiB, 1 vCPU, $12/mo | `s-1vcpu-2gb` | 50 GiB, 2,000 GiB | Default. Room for Postgres, Caddy, and the app. |
| 1 GiB, 1 vCPU, $6/mo | `s-1vcpu-1gb` | 25 GiB, 1,000 GiB | Only after an image exists to pull. Add `--swap-mb 1024`. |
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

`--allow-onbox-build` matches this repo today: Compose builds the image
on the droplet. When `ghcr.io/grierj/fold` is published, drop that flag
and pass `--image ghcr.io/grierj/fold:x.y.z` instead. Pin the tag. Do
not deploy `:latest`.

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
8. Copy the stack to `/opt/fold`, write `/opt/fold/.env` mode 600 with a new `SESSION_SECRET` and `DB_PASSWORD`, and refuse to continue if that file is not mode 600 or is committed in git.
9. Install `/opt/fold/backup.sh` and a cron job at 02:30 in the droplet's timezone.
10. After Compose is up, run `smoke.sh`: wait until the db and app containers are healthy, request `https://$DOMAIN/healthz`, check the TLS certificate, and fail if 5432 or 8080 is reachable. The same script runs from cron every five minutes (`logger -t fold-health` on failure).
11. Read `sshd -T` and fail if root login or password login is still on. Fail if `ufw` is not active.

A second run does not rotate `.env` secrets, does not duplicate SSH keys, and does not replace `/swapfile` or `backup.env`. Pass the same `--domain`. To change the hostname, edit `.env` yourself; the script stops if the flag and the file disagree. `:latest` is refused.

Set the church's time zone so the 02:30 backup is local. `--swap-mb 1024` adds a swap file on a 1 GiB droplet. `--smtp-url` and `--email-from` are written only when `.env` is first created; editing `.env` avoids leaving the SMTP password in shell history.

From the second terminal:

```bash
ssh deploy@groups.example.org
sudo /opt/fold/smoke.sh
```

The first browser visit to `https://groups.example.org/` is the setup wizard (site name and the first admin). `GET /healthz` answers only after Postgres is up.

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

Put the SMTP URL in `/opt/fold/.env` (Postmark's host is
`smtp.postmarkapp.com`, port 587) and reload:

```bash
FOLD_SMTP_URL=smtp://SERVER_TOKEN:SERVER_TOKEN@smtp.postmarkapp.com:587
FOLD_EMAIL_FROM=Fold <noreply@groups.example.org>
```

`sudo docker compose --project-directory /opt/fold up -d` reloads them.
Leave SMS unset until the church has a Telnyx account and wants texting.

`SESSION_SECRET` must be at least 32 characters. The script generates one
with `openssl rand -base64 48`. Store `.env` in the church password manager.
Losing it signs everyone out and, later, loses encrypted provider secrets.

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

On a pulled image:

```bash
sudo /root/fold/deploy/digitalocean/upgrade.sh \
  --image ghcr.io/grierj/fold:x.y.z \
  --source-dir /root/fold
```

On a build-based install, name the commit:

```bash
sudo /root/fold/deploy/digitalocean/upgrade.sh \
  --commit abcdef1234567 \
  --source-dir /root/fold
```

The script runs `backup.sh`, writes the current tag or commit and the new
dump path to `/opt/fold/release.prev` (mode 600), then runs `provision.sh`,
which runs `smoke.sh`. `.env` is left in place.

Roll back to that previous tag or commit and restore the dump it recorded:

```bash
sudo /root/fold/deploy/digitalocean/upgrade.sh --rollback
```

Rollback stops the app, runs `pg_restore` of the recorded dump into the
`fold` database, starts the previous image or commit, and runs `smoke.sh`
again. Take a Droplet snapshot before a risky upgrade (the paid command
in section 5). Migrations run when the app starts. A failed migration
exits non-zero and `smoke.sh` fails; roll back with the command above.

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

- **Images.** Keep the Compose build on the droplet (`--allow-onbox-build` on the 2 GB size). `--image` is for a later published tag. No separate build setup.
- **Ubuntu.** The create command uses `ubuntu-26-04-x64`. The script also runs on 24.04 (`ubuntu-24-04-x64`). Hetzner uses the same script on those Ubuntu images.
- **Services.** One recommendation each, and another provider still works. Postmark for email (any SMTP host works). Telnyx for SMS when a church wants texting (Fold runs without it). Backblaze B2 for offsite dumps (any S3-compatible bucket, including DigitalOcean Spaces, works).
