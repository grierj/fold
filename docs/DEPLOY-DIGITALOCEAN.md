# Deploy Fold on one DigitalOcean droplet

Fold runs as one Docker Compose stack on one virtual machine: Caddy for
HTTPS, the Fold app, and Postgres 16. That is the recommended shape. The
church owns the DigitalOcean account, the domain, and the email account.

`deploy/digitalocean/provision.sh` hardens a fresh Ubuntu droplet and
starts the stack. It is idempotent. `--dry-run` prints every action and
changes nothing. It never calls `doctl` and never creates a droplet, a
Cloud Firewall, a snapshot, a Droplet Backup plan, or a Spaces bucket.
Those commands are below so a person can run them in the church's account.

Prices below were checked in October 2026 against
[DigitalOcean droplet pricing](https://www.digitalocean.com/pricing/droplets).
Re-check before telling a church a number.

## What you need

- A domain the church controls (`groups.example.org`).
- An SSH key pair. Droplets are created with that public key and no root password.
- The church's DigitalOcean account. A helper can be added as a team member.
  The helper does not open the account in their own name.
- An email provider the church already pays for, or a new one in the
  church's name. Postmark is the usual choice; any SMTP host works.
  Fold starts without it and then refuses to send password resets and
  invitations until `FOLD_SMTP_URL` is set.
- SMS is optional. Fold runs with email alone. When a church wants
  texting, Telnyx is the provider to set up, on the church's own account.
  Twilio is a later adapter and is not part of bringing a site up.

The database is Postgres in the Compose file (`postgres:16-alpine`, user
`church`, database `fold`). That service is the only database.

## Size

Pull a published image. Building the Node and Go stages on the droplet
can exhaust a 1–2 GB machine, and this repo's `docker-compose.yml` still
says `build: .` until prebuilt images are published (plan C10, open
question below).

| Droplet | Slug | Disk / transfer | When |
|---|---|---|---|
| 2 GiB, 1 vCPU, $12/mo | `s-1vcpu-2gb` | 50 GiB, 2,000 GiB | Default. Room for Postgres, Caddy, and the app. |
| 1 GiB, 1 vCPU, $6/mo | `s-1vcpu-1gb` | 25 GiB, 1,000 GiB | Only after an image exists to pull. Add `--swap-mb 1024`. |
| 2 GiB, 2 vCPU, $18/mo | `s-2vcpu-2gb` | 60 GiB, 3,000 GiB | When the 1 vCPU size is busy. |

Weekly Droplet Backups add 20% of the droplet price. Daily adds 30%.
Snapshots are $0.06 per GB-month. Spaces object storage starts at $5/month.
Cloud Firewalls and the metrics agent are free. Pick a region near the
church; `nyc3` below is only an example.

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

A Cloud Firewall is the edge. Docker publishes ports 80 and 443 by
editing iptables itself, so host `ufw` does not see those packets.
`ufw` still limits anything that binds on the host. Use both.

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
8. Copy the stack to `/opt/fold`, write `/opt/fold/.env` mode 600 with a new `SESSION_SECRET` and `DB_PASSWORD`, and start Compose.
9. Install `/opt/fold/backup.sh` and a cron job at 02:30 in the droplet's timezone.
10. Curl `https://$DOMAIN/healthz` every five minutes and log failures with `logger -t fold-health`.

A second run does not rotate `.env` secrets, does not duplicate SSH keys, and does not replace `/swapfile` or `backup.env`. Pass the same `--domain`. To change the hostname, edit `.env` yourself; the script stops if the flag and the file disagree.

Set the church's time zone so the 02:30 backup is local. `--swap-mb 1024` adds a swap file on a 1 GiB droplet. `--smtp-url` and `--email-from` are written only when `.env` is first created; editing `.env` avoids leaving the SMTP password in shell history.

From the second terminal:

```bash
ssh deploy@groups.example.org
sudo docker compose --project-directory /opt/fold ps
curl -fsS https://groups.example.org/healthz
```

The first browser visit to `https://groups.example.org/` is the setup wizard (site name and the first admin). `GET /healthz` answers only after Postgres is up.

Then edit `/opt/fold/.env` and add the church's mail settings:

```bash
FOLD_SMTP_URL=smtp://user:pass@smtp.example.org:587
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
The plan's default store is Backblaze B2. Spaces is the same-vendor
option and has a $5/month minimum. Either is S3-compatible. Create the
bucket and a write-only key in the church's account (control panel).
The script will not create them.

On a laptop, not the server:

```bash
age-keygen -o fold-backup.key
```

Put `fold-backup.key` in the password manager. Copy the `age1...` public
key into `/opt/fold/backup.env` (start from
`deploy/digitalocean/backup.env.example`), mode 600. Set the bucket,
endpoint, and region. Spaces endpoints look like
`https://nyc3.digitaloceanspaces.com`. B2 endpoints look like
`https://s3.us-west-004.backblazeb2.com`.

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

## 6. Upgrades

1. Read the release notes. `README.md` records operator steps (for example the `SESSION_SECRET` length). `docs/UPGRADING.md` starts when the first church release exists.
2. `sudo /opt/fold/backup.sh` and, before a risky upgrade, a snapshot (the paid command above).
3. On a build-based install: `git -C /root/fold pull`, then re-run `provision.sh` with the same flags. `.env` is left in place, and Compose rebuilds.
4. On a pulled image: re-run with the new `--image ghcr.io/grierj/fold:x.y.z`. The script rewrites `/opt/fold/docker-compose.yml` from the repo file and runs `docker compose pull && docker compose up -d`.
5. `curl -fsS https://groups.example.org/healthz` and `sudo docker compose --project-directory /opt/fold ps`.

Migrations run when the app starts. A failed migration exits non-zero and
the container stays unhealthy. The rollback is the dump from step 2 plus
the previous image tag or git revision.

## 7. Monitoring

- The metrics agent (`--enable-monitoring`) graphs CPU, memory, and disk in the control panel. Add alert policies there for sustained CPU, memory above about 85%, and disk above about 80%. Alerts are free; the script does not create them.
- `/etc/cron.d/fold-health` requests `https://$DOMAIN/healthz` every five minutes. Failures go to the system log (`journalctl -t fold-health`).
- `sudo docker compose --project-directory /opt/fold ps` and `logs app --tail 200` are the first place to look when the site is down.
- fail2ban's sshd jail is in `fail2ban-client status sshd`. Fold's own login throttling is separate and lives in Postgres.

Certificate problems are almost always DNS (the name does not point here yet), port 80 blocked, or a Cloudflare proxy in front of Caddy. `sudo docker compose --project-directory /opt/fold logs caddy --tail 100` shows the ACME error. The shipped Caddyfile does not set a contact email, so Let's Encrypt will not mail you when a certificate is close to expiry. The health check and the Caddy log are how you notice.

## Choices still open

- **Images.** Plan C10 publishes `ghcr.io/grierj/fold` and a backup image. This repo still builds on the host (`build: .` in `docker-compose.yml`). The script refuses to build unless you pass `--allow-onbox-build`. `--image` is ready for the day a tag exists.
- **Ubuntu.** The script accepts 24.04 and 26.04. The create command above uses `ubuntu-26-04-x64` because that is the current LTS DigitalOcean offers. `ubuntu-24-04-x64` is also tested.
- **Backup destination.** Local dumps always land in `/var/backups/fold`. Offsite is whatever S3-compatible bucket the church creates. The product plan recommends Backblaze B2. Spaces stays in the same DigitalOcean account and costs more at the low end. The script ships to whichever endpoint is in `backup.env`.
