# Fold design: 12. Third-party services and hosting

## 12. Third-party services and hosting

Prices were checked on the vendor URLs listed, as of October 2026, unless marked **pricing not verified**. Prices change, so re-check before quoting them to a church. "API setup" means Fold can configure the service through an API after the admin pastes credentials. Creating the account itself is always done by a person, and the account belongs to the church, even when someone else hosts Fold (DEC-2). Churches are assumed to be cost-conscious, not feature-seeking, so defaults favor the cheapest option that works well. Fold uses no AWS service directly, because of vendor lock-in (DEC-21, DEC-26). Vendors that run on AWS underneath are fine. Fold is self-contained by default (DEC-43): a service appears here only when it keeps deployment simple or a church asks for it. Notifications need no service; they are built in (DEC-42, 5.12).

### 12.1 Email (required)

Postmark is the default (DEC-21). Fold builds the SMTP and Postmark adapters first; Resend and Mailgun adapters come when a church asks.

| Option | Price (cited) | API setup | Lock-in notes |
|---|---|---|---|
| **Postmark** (default) | Free developer tier 100 emails/mo; Basic $15/mo for 10k, then $1.80 per extra 1k; Pro $16.50/mo for 10k, then $1.30 per 1k. https://postmarkapp.com/pricing | Yes: servers, sender domains (DKIM/return-path), message streams (transactional vs broadcast), webhooks | Low. Plain REST plus SMTP. Strong deliverability, and separate broadcast streams fit 7.4. |
| **Resend** | Free 3,000/mo, capped at 100/day; Pro $20/mo for 50k. https://resend.com/pricing | Yes: domains, webhooks, API keys | Low. Simple API. The free daily cap is too low for an all-church blast. Adapter later. |
| **Mailgun** | Free 100/day; Basic $15/mo for 10k; Foundation $35/mo for 50k. https://www.mailgun.com/pricing/ | Yes: domains, DNS records, webhooks | Low to medium. Adapter later. |
| **Any SMTP** (Fastmail, Google Workspace, Microsoft 365, the church's existing mail host) | Whatever the church already pays; pricing not verified | No (Fold just sends) | None. No bounce webhooks, and sending limits are low, so it is a fallback for small churches, for M1.2 before the wizard exists, and for tests. |

Not recommended: Amazon SES. It is cheap at volume, but Grier avoids using AWS directly because of vendor lock-in, and its account, IAM, and sandbox steps are heavy for a church office (DEC-21).

### 12.2 SMS (optional, but needed for F5 and F9)

Telnyx is the default because churches are cost-conscious, not feature-seeking. Twilio is the second adapter (M3.10). Plivo is listed for comparison; an adapter only if a church asks. None is required: Fold runs without SMS.

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Telnyx** (default) | $0.004 per message part plus carrier fees; numbers from $1/mo. https://telnyx.com/pricing/messaging (Telnyx's 10DLC guide lists a local SMS number at $1.10/mo: https://telnyx.com/resources/what-is-10dlc). 10DLC: brand registration $4.50 one-time; campaign review $15 per submission; campaign fee $1.50/mo (Low-Volume Mixed), $10/mo (standard use cases), or $2/mo (sole proprietor), with the first 3 months billed up front. https://support.telnyx.com/en/articles/5634625-10dlc-fees-and-charges. Optional standard brand vetting $41.50. https://developers.telnyx.com/docs/messaging/10dlc/brand-registration | Yes: numbers, messaging profiles, 10DLC brand and campaign API | About 40 to 50% cheaper per segment than Twilio. Smaller ecosystem. |
| **Twilio** | $0.0083 per outbound segment plus carrier fees (AT&T $0.0035, T-Mobile $0.0045, Verizon $0.005 per segment). Long-code number $1.15/mo; toll-free $2.15/mo. https://www.twilio.com/en-us/sms/pricing/us. 10DLC: brand registration $4.50 one-time, standard brand vetting $41.50, campaign vetting $15 one-time; monthly campaign fees pricing not verified. https://www.twilio.com/docs/messaging/compliance/a2p-10dlc | Yes: number search and purchase, webhooks, Trust Hub brand and campaign registration, toll-free verification | Most documentation and community help. Built-in STOP handling. |
| **Plivo** | $0.0077 per message (US); long code $0.50/mo; toll-free $1/mo. https://www.plivo.com/sms/pricing/us/ | Yes: numbers, applications, 10DLC API | Cheaper numbers. Carrier pass-through fees apply; pricing not verified for the exact amounts. |

**The 10DLC burden, in plain words.** US carriers block business texting from ordinary local numbers unless the sender is registered with The Campaign Registry (TCR). A church has to:

1. Register a **brand**: legal name, EIN, address, website.
2. Register a **campaign**: what the messages are for, sample messages, how people opt in, and links to a privacy policy and SMS terms (Telnyx's campaign guidance asks for both; Twilio requires both URLs on new campaigns since June 30, 2026; Fold generates both pages for every provider).
3. Wait for review. Telnyx reviews first, then carriers take about 72 business hours. Twilio quotes 10 to 15 days for vetting. Rejections are common when the website, opt-in, and samples don't match.
4. Live with throughput caps tied to the brand type, use case, and vetting score.

**Registration paths for a church (DEC-3).** Fold assumes the church is **not** a 501(c)(3) organization. It never asks for 501(c)(3) proof and never picks a charity use case (Twilio says its `CHARITY` use case does not include religious organizations; Telnyx's Charity use case covers fundraising and donations, which Fold keeps out of SMS).

| Path | Needs | Limits | When to use |
|---|---|---|---|
| **A. EIN-based standard brand (10DLC)**, default | The church's legal name and EIN, address, website (Fold's pages are enough) | Telnyx: T-Mobile daily caps by brand vetting tier, from 2,000 a day (Basic) to 10,000 (Medium) and higher; Low-Volume Mixed campaigns are limited to 75 messages a minute. Twilio Low-Volume Standard: up to 2,000 segments a day to T-Mobile (about 6,000 across US carriers), up to 5 campaigns. | Almost every church. Use case: Low-Volume Mixed (cheapest monthly fee) unless the church sends more than about 2,000 segments a day to T-Mobile, then Mixed. |
| **B. Toll-free verification with an EIN** | A toll-free number; the EIN as business registration number (required for all business types except sole proprietors on new Twilio submissions since February 17, 2026) | Provider-specific; no TCR campaign fees | When 10DLC is rejected, or the church prefers one national number. |
| **C. Sole-proprietor 10DLC (no EIN)**, Twilio only | A real person's name (for example the pastor's), address, email, and a mobile number that gets a one-time code. | 1 campaign and 1 number per brand; 1,000 segments a day to T-Mobile; 15 messages a minute (Twilio and Telnyx both document these limits). | Last resort, and **not available on Telnyx for a church**: Telnyx says an entity whose name or website contains "Church", "Ministry", "Ministries", "Group", "Center", "Charity", or ".org" does not qualify as a sole proprietor and must register a standard brand with an EIN. On Twilio, the named person is the registered sender, and a church may still be rejected. |
| **D. Toll-free as a sole proprietor (no EIN)**, Twilio only | Business type `SOLE_PROPRIETOR`, so no registration number is needed (Twilio) | Provider-specific | Same caveats as C. |

If a church has no EIN, Fold recommends getting one before any of this; on Telnyx it is required. The IRS issues EINs free, online, in minutes, and says a church must use an EIN for any tax-related purpose even without employees (https://www.irs.gov/businesses/small-businesses-self-employed/get-an-employer-identification-number, https://www.irs.gov/pub/irs-pdf/fss4.pdf). Paths C and D are offered only on Twilio, and only after the wizard shows their limits.

Fold adapts to the path:
- **Daily caps.** The SMS adapter's capabilities include the path's daily caps, and the pipeline spreads sends across days when needed. The composer warns, for example: "This text goes to 2,400 people; about 400 will go tomorrow because of the carrier's daily limit."
- **No donation asks.** Messages and samples avoid donation asks, which can trigger charity classification and 501(c)(3) demands (Twilio error 30487: "Charity must provide 501(c)(3) verification").

**Cost example**: one church sending 800 SMS segments a month comes to about $7 on Telnyx (800 × ($0.004 + about $0.004 carrier fee)) plus $1 to $1.10 for the number, plus $1.50 a month for a Low-Volume Mixed campaign and the one-time fees ($4.50 brand, $15 per campaign review). On Twilio, about $10 plus $1.15 for the number, plus its campaign fees (not verified). Estimates only.

### 12.3 Object storage (optional; local disk is the default)

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Local disk / VPS block volume** | Included with the VPS; block volume pricing per vendor, not verified | n/a | Zero lock-in. Default. |
| **Backblaze B2** | $6.95 per TB per month; first 10 GB free; free egress up to 3× the amount stored. https://www.backblaze.com/cloud-storage/pricing | Yes: S3 API plus native API for buckets and keys | Recommended for offsite backups. |
| **Cloudflare R2** | $0.015 per GB per month; 10 GB free; no egress fees. https://developers.cloudflare.com/r2/pricing/ | Yes: S3 API; buckets and tokens via the Cloudflare API | Recommended for public assets (galleries) because there are no egress fees. |
| **Wasabi** | $7.99 per TB per month, 1 TB minimum charge, 90-day minimum retention. https://wasabi.com/pricing | Yes: S3 API | The 1 TB minimum makes it poor value for one small church. |
| **Vultr Object Storage** | From $18/mo (1 TB). https://www.vultr.com/pricing/ | Yes: Vultr API plus S3 API | Convenient if the VPS is on Vultr. |
| **MinIO (self-hosted)** | Free software | S3 API | Used in CI. Not recommended for production on the same VPS (it adds nothing over local disk). |

All of these work with one S3-compatible adapter, so switching is a config change plus `fold storage migrate`.

### 12.4 Video (F11; link-out is the default)

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Link-out** (any Zoom, Meet, Teams, or Jitsi URL) | $0 from Fold | n/a | Zero lock-in. Default. |
| **meet.jit.si generated links** | Free. https://jitsi.org/jitsi-meet/ (no price page) | No API needed; Fold makes a random room URL | Embedding meet.jit.si inside another site is reportedly limited to 5 minutes (secondary source; not verified on a Jitsi page). Link-out avoids this. |
| **8x8 Jitsi as a Service (JaaS)** | Free up to 25 monthly active users; $99/mo for 300; $499/mo for 1,500; $0.99 per extra MAU. https://jaas.8x8.vc/#/pricing | Yes: JWT-signed rooms | Not used: embedded video is out of scope for now (DEC-16). Listed in case a later revision revisits it. |
| **Zoom** | Basic free (40-minute meetings); Pro $16.99/mo monthly or about $14.16/mo billed yearly (from a search summary; pricing not verified on the vendor page). https://zoom.us/pricing | Yes: Server-to-Server OAuth app, meeting create API | Many churches already have Zoom. Later adapter. |
| **Google Meet** | Needs Google Workspace for API meeting creation; pricing not verified | Reportedly yes (Meet REST API); not verified | Later, if requested. |
| **Self-hosted Jitsi** | Second VPS; pricing per VPS vendor | n/a | Out of scope: it breaks D5 (no extra deployment complexity). |

### 12.5 Hosting

Fold runs on one VPS with Docker Compose. That is the only recommended deployment for now (DEC-26). The step-by-step guide is [`docs/DEPLOY-DIGITALOCEAN.md`](../../DEPLOY-DIGITALOCEAN.md) (one droplet; Ubuntu 24.04 and 26.04 tested), with `deploy/digitalocean/provision.sh`, `smoke.sh`, `upgrade.sh`, and `backup.sh` (#53).

**Audit recheck (DEC-142, Nov 2026).** Cloudflare was evaluated as a host and rejected: Workers cannot run the Go binary, D1 is not PostgreSQL, and Containers leave PostgreSQL/uploads on snapshot disk or add a second vendor. Cloudflare's roles stay narrow: optional DNS (app record DNS-only), R2 as one encrypted offsite-backup target, Tunnel later for church-owned boxes; authenticated responses are never cached. Full comparison with vendor pages fetched 2026-11-02: [`briefs/audit-2026-11/R1-deployment.md`](../briefs/audit-2026-11/R1-deployment.md) Part 1.

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **DigitalOcean** (VPS) | 1 GB $6/mo; 2 GB $12/mo. https://www.digitalocean.com/pricing/droplets | Yes: full API, cloud-init | Good docs; US data centers. |
| **Vultr** (VPS) | Regular 1 GB $5/mo, 2 GB $10/mo; High Performance 1 GB $6/mo, 2 GB $12/mo. https://www.vultr.com/pricing/ | Yes: full API | Also offers object storage on the same account. |
| **Hetzner** (VPS) | After the June 15, 2026 price change: US CPX11 $20.49/mo excluding a $0.60 IPv4 fee; EU CX23 $6.49/mo. https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/ | Yes: full API | Very good value in the EU; US plans are now much dearer. |
| **Akamai/Linode** (VPS) | 1 GB $5/mo, 2 GB $12/mo per third-party trackers; pricing not verified (the vendor page blocked access) | Yes: full API | Fine alternative. |
| **Church-owned box** (old PC, mini PC, on-site server) | Hardware the church owns | n/a | Supported for power users; needs ports 80/443 open or a tunnel. |

**Later / on request: Render (managed PaaS).** Not recommended or tested yet and not in the committed roadmap (DEC-26). This evaluation and the `render.yaml` outline in Appendix A are kept as reference, for when a church asks or when Grier wants an easy path to host for others (future M5.5). Facts are from Render's own pages, October 2026. Pricing: Hobby workspace $0 + compute; Pro workspace $25/mo + compute. Web service 0.5 CPU / 512 MB $7/mo (1 CPU / 2 GB $25/mo). Postgres 0.1 CPU / 256 MB $6/mo, 0.5 CPU / 1 GB $19/mo, plus storage at $0.30/GB/mo. Persistent disk $0.25/GB/mo. Bandwidth: 5 GB included on Hobby (25 GB on Pro), then $0.15/GB. https://render.com/pricing. Configurable by REST API and `render.yaml` Blueprints (https://render.com/docs/api, https://render.com/docs/blueprint-spec).

- **Typical cost for one small church** (Hobby workspace, prices from https://render.com/pricing):
  - Web service 0.5 CPU / 512 MB: $7. Fold's single Go binary fits.
  - Postgres 0.1 CPU / 256 MB: $6, plus about $1.50 for 5 GB of storage.
  - Disk, 5 GB at `/data` for uploads: $1.25.
  - Total: **about $16/mo**. With the 1 GB Postgres plan ($19), about $29/mo.
  - The Hobby workspace has one team seat. A helper who needs their own login requires Pro, which adds $25/mo (about $41 to $54/mo in total). Otherwise the church shares an API key with the helper, not its password.
  - A 2 GB VPS is $10 to $12/mo, plus a few cents for offsite backups (12.3).
- **Free tier: not suitable.** Free web services spin down after 15 minutes idle (about a minute to wake) and cannot have disks. Free Postgres expires after 30 days and has no backups. Free web services also cannot send on ports 25, 465, or 587, which SMTP uses (https://render.com/docs/free). Postmark's HTTPS API is not affected.
- **Containers and background jobs: yes.** A web service can run a prebuilt image from GHCR (`runtime: image`) or build the Dockerfile. Fold's job worker runs inside the web service process, which is always on for paid plans, so it needs no separate worker service. Render also offers background workers and cron jobs, billed per minute, if ever needed.
- **Persistent disk** (https://render.com/docs/disks):
  - Paid services only, with daily snapshots kept at least 7 days.
  - A service with a disk is limited to **one instance**, and deploys have **a few seconds of downtime** (zero-downtime deploys are turned off).
  - Disks are not reachable from pre-deploy commands, one-off jobs, or cron jobs, and can grow but not shrink.
  - For one church, one instance is fine. To keep zero-downtime deploys, use S3-compatible storage (B2 or R2, 12.3) instead of a disk once M4.7 ships.
- **Managed Postgres** (https://render.com/docs/databases, https://render.com/docs/postgresql-backups, https://render.com/docs/postgresql-extensions):
  - Versions 13 to 18 are available (18 is the default). Pin `postgresMajorVersion: "16"` to match CI.
  - The `citext` and `btree_gist` extensions Fold needs are supported.
  - Paid databases get point-in-time recovery: 3 days on Hobby, 7 days on Pro. A recovery restores into a new database. Logical exports are kept 7 days, and `pg_dump` works over the external URL.
- **Custom domains and TLS: yes** (https://render.com/docs/custom-domains). Certificates are automatic. Hobby includes 2 custom domains. Render serves over IPv4, so any AAAA record for the Fold host must be removed.
- **Agent-deployable: yes.** A `render.yaml` Blueprint can declare the web service (image, plan, region, disk, health check path, domains, env vars, generated secrets) and the database (version, plan, disk size, IP allow list), and wire `DATABASE_URL` from the database. Blueprints can be validated before use, and the REST API covers services, databases, deploys, env groups, Blueprints, and custom domains. Secrets marked `sync: false` are prompted for only when the Blueprint is first created, so the operator types provider keys into Render's dashboard, not into the repo.
- **Lock-in: low.** Fold's image, standard Postgres (`pg_dump`), and S3 storage all move. Only `render.yaml` and Render's environment settings are Render-specific. Moving to a VPS is `pg_dump` + `pg_restore` plus copying the uploads.
- **Fit with Fold's goals:**
  - Simple for a power user: yes, arguably simpler, because there is no OS, firewall, Caddy, or backup container to look after.
  - Church owns its accounts (DEC-2): yes, if the church creates the Render workspace and pays Render directly.
  - Few dependencies (D3, D6): it is one vendor for compute, database, and TLS, instead of a VPS.
  - Things Fold must handle: trust Render's proxy for client IPs (C17); turn off the Compose backup check (`FOLD_BACKUP_CHECK=off`); accept the generated secret format (base64) for `SESSION_SECRET` and `FOLD_SECRET_KEY`; read `PORT`.

**If Render is added later (M5.5):**

- A VPS with Compose stays the default either way: cheapest (about $10 to $12/mo) and fully under the church's control.
- Render would suit a church where nobody wants to look after a Linux server and about $16 to $29/mo (more on Pro) is acceptable for managed TLS, Postgres backups with point-in-time recovery, and push-to-deploy. Prefer S3 storage over a disk there, so deploys stay zero-downtime.

### 12.6 DNS and TLS

| Option | Price (cited) | API setup | Notes |
|---|---|---|---|
| **Caddy automatic HTTPS** (Let's Encrypt / ZeroSSL) | Free (open source). https://caddyserver.com/docs/automatic-https | n/a; automatic | Already in the repo. Keep it. |
| **Cloudflare DNS** | Free plan $0. https://www.cloudflare.com/plans/ | Yes: DNS records API with a scoped token | Optional. Lets the wizard create email DNS records automatically. Use DNS-only (grey cloud) for the app host so Caddy can get its certificate the simple way. |
| **Any registrar DNS** | Whatever the church pays now | Usually no | The wizard shows records to copy and checks them by DNS lookup. |

### 12.7 Recommended picks

- **Email**: Postmark by default (DEC-21): good deliverability, broadcast streams, simple setup. Any SMTP as the fallback. Resend or Mailgun when a church prefers them (adapter added on request).
- **SMS**: Telnyx by default, because churches are cost-conscious rather than feature-seeking. Twilio second (most documentation, built-in compliance tools, and a sole-proprietor path). Plivo only on request. All go through the same adapter interface, and none is required.
- **Storage**: local disk by default. Backblaze B2 for offsite backups. Cloudflare R2 when public galleries grow.
- **Video**: link-out plus generated Jitsi links. Zoom adapter later.
- **Hosting**: a VPS with Compose by default: DigitalOcean or Vultr (2 GB) in the US, Hetzner in the EU. This is the only recommended deployment for now; Render is a later, on-request option (12.5, DEC-26).
- **DNS/TLS**: Caddy for TLS. Cloudflare DNS optional, for automatic email records.
- **AWS check (DEC-21, DEC-26)**: Fold uses no AWS service directly. "S3-compatible" storage is only a protocol (B2, R2, and the others are not Amazon). Vendors that run on AWS underneath are fine.

## 17. Appendix B: Sources and unverified facts

### 17.1 Sources

- Repository: https://github.com/grierj/fold (read at commit `aa992e2`; README, AGENTS.md, go.mod, models, storage, setup, compose, Caddyfile, merged PRs #1–#6; re-checked read-only at `e7c0e8f` for PR #7 and the review's line references: `main.go:48`, `main.go:129`, `main.go:147-148`, `api_auth.go`, `models/models.go`, `web/package.json`)
- FCC consent revocation rule, 47 CFR 64.1200(a)(10) (per-se keywords, 10 business days): https://www.federalregister.gov/documents/2024/03/05/2024-04587/strengthening-the-ability-of-consumers-to-stop-robocalls
- FCC delay of the revoke-all provision to January 31, 2027 (DA 26-12): https://docs.fcc.gov/public/attachments/DA-26-12A1.pdf and https://www.fcc.gov/document/cgb-extends-effective-date-tcpas-consent-revocation-rule
- TCPA calling hours, 47 CFR 64.1200(c)(1): https://www.ecfr.gov/current/title-47/chapter-I/subchapter-B/part-64/subpart-L/section-64.1200
- Telnyx 10DLC fees: https://support.telnyx.com/en/articles/5634625-10dlc-fees-and-charges and https://support.telnyx.com/en/articles/6325731-register-for-10dlc-messaging
- Telnyx brand registration, entity types, and vetting: https://developers.telnyx.com/docs/messaging/10dlc/brand-registration
- Telnyx sole proprietor rules: https://developers.telnyx.com/docs/messaging/10dlc/sole-proprietor and https://support.telnyx.com/en/articles/13545282-guide-to-sole-proprietor-10dlc-brand-and-campaign-registration (church and ministry names do not qualify)
- Telnyx 10DLC rate limits: https://developers.telnyx.com/docs/messaging/10dlc/10dlc-rate-limits
- Telnyx campaign use cases (Charity, privacy and terms links): https://developers.telnyx.com/docs/messaging/10dlc/campaign-use-cases
- Telnyx 10DLC overview (fee table, number price): https://telnyx.com/resources/what-is-10dlc
- Telnyx messaging pricing: https://telnyx.com/pricing/messaging
- Twilio brand types and limits (sole proprietor, low-volume standard): https://www.twilio.com/docs/sms/a2p-10dlc and https://www.twilio.com/docs/trust-hub/registrations/a2p-10dlc-brand
- Twilio sole-proprietor requirements (OTP, AT&T 15/min): https://www.twilio.com/en-us/blog/products/launches/sole-proprietor-api-and-console-experience-for-10dlc-registration
- Twilio toll-free business registration number rule (from February 17, 2026): https://www.twilio.com/en-us/changelog/business-registration-numbers-required-for-toll-free-messaging-p and https://www.twilio.com/docs/api/errors/30527
- Twilio error 30487 (charity must provide 501(c)(3) verification): https://www.twilio.com/docs/api/errors/30487
- Twilio A2P 10DLC docs (fees, vetting time, use cases, privacy and terms URL rule): https://www.twilio.com/docs/messaging/compliance/a2p-10dlc
- Twilio toll-free verification docs: https://www.twilio.com/docs/messaging/compliance/toll-free
- Twilio SMS pricing: https://www.twilio.com/en-us/sms/pricing/us
- Plivo SMS pricing: https://www.plivo.com/sms/pricing/us/
- IRS EIN (free; churches must use an EIN for tax purposes): https://www.irs.gov/businesses/small-businesses-self-employed/get-an-employer-identification-number and https://www.irs.gov/pub/irs-pdf/fss4.pdf
- Postmark pricing: https://postmarkapp.com/pricing
- Resend pricing: https://resend.com/pricing
- Mailgun pricing: https://www.mailgun.com/pricing/
- Render pricing (workspaces, web services, Postgres, disks, bandwidth, AWS Private Links): https://render.com/pricing
- Render persistent disks (single instance, no zero-downtime deploys, snapshots): https://render.com/docs/disks
- Render Postgres (versions, upgrades, backups and PITR, extensions): https://render.com/docs/databases, https://render.com/docs/postgresql-upgrading, https://render.com/docs/postgresql-backups, https://render.com/docs/postgresql-extensions
- Render free tier limits (spin-down, 30-day database, SMTP ports): https://render.com/docs/free and https://render.com/docs/postgresql-refresh
- Render custom domains and TLS: https://render.com/docs/custom-domains
- Render Blueprint spec (`render.yaml`): https://render.com/docs/blueprint-spec
- Render API: https://render.com/docs/api
- Cloudflare R2 pricing: https://developers.cloudflare.com/r2/pricing/
- Backblaze B2 pricing: https://www.backblaze.com/cloud-storage/pricing
- Wasabi pricing: https://wasabi.com/pricing
- Vultr pricing (VPS and object storage): https://www.vultr.com/pricing/
- 8x8 JaaS pricing: https://jaas.8x8.vc/#/pricing
- DigitalOcean Droplet pricing: https://www.digitalocean.com/pricing/droplets
- Hetzner price adjustment: https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/
- Cloudflare plans: https://www.cloudflare.com/plans/
- Caddy automatic HTTPS: https://caddyserver.com/docs/automatic-https
- ChurchTrac pricing and features: https://www.churchtrac.com/pricing
- Meetup Help Center (events, waitlist, guests, hybrid, check-in): https://help.meetup.com
- RFC 8058 (one-click unsubscribe): https://www.rfc-editor.org/rfc/rfc8058
- Web push (M3.13; details and marks in `research/web-push.md`):
  - WebKit, Web Push for Web Apps on iOS and iPadOS (iOS 16.4, Home Screen requirement, `display: standalone|fullscreen`, user gesture, third-party browsers' Add to Home Screen, badging): https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/
  - WebKit, Meet Declarative Web Push (`web_push: 8030`, no service worker needed, iOS 18.4 and macOS 15.5, backward compatible): https://webkit.org/blog/16535/meet-declarative-web-push/
  - RFC 8030, Generic Event Delivery Using HTTP Push (TTL, Urgency, Topic, 404 for an expired subscription): https://www.rfc-editor.org/rfc/rfc8030.html
  - RFC 8291, Message Encryption for Web Push (aes128gcm; Appendix A test vector): https://www.rfc-editor.org/rfc/rfc8291.html
  - RFC 8292, VAPID: https://www.rfc-editor.org/rfc/rfc8292.html
  - webpush-go `go.mod` (the library option not chosen): https://raw.githubusercontent.com/SherClockHolmes/webpush-go/master/go.mod
  - Go 1.24 release notes draft (`crypto/hkdf`): https://go.googlesource.com/website/+/2a8b895514c4e52860e9c0b1ef8626db3ea36bf5/_content/doc/go1.24.md
  - webpush-java wiki, Endpoints (FCM and Mozilla hosts): https://github.com/web-push-libs/webpush-java/wiki/Endpoints
  - OneSignal, Mobile Web Push for iOS/iPadOS (vendor): https://documentation.onesignal.com/docs/web-push-for-ios
  - PushAlert, Chrome on iOS 113 web push (vendor): https://pushalert.co/blog/?p=2506
  - iOS 17.4 EU Home Screen web app removal and reversal (press): https://www.macrumors.com/2024/02/15/ios-17-4-web-apps-removed-apple/ and https://www.engadget.com/apple-backtracks-on-plans-to-get-rid-of-web-apps-on-iphones-in-the-eu-195232177.html
  - Push-service host lists for Apple, Windows and Samsung (secondary): https://kiwix.lema.org/stackoverflow.com_en_all_2023-11/questions/52252877/which-host-ports-should-be-open-for-web-push and https://github.com/hackutd/harp/pull/153 (unmerged)

### 17.2 Not verified (re-check before relying on them)

- Twilio monthly 10DLC campaign fees
- Plivo carrier pass-through amounts
- Akamai/Linode prices (the vendor page blocked access; from third-party trackers)
- Zoom Pro price (the vendor page returned 404; from a search summary)
- Google Meet API needing Workspace, and Meet pricing
- The 5-minute limit on embedded meet.jit.si calls (secondary source)
- Block volume prices on each VPS vendor
- Exact state rules on texting hours (stricter than federal in some states; this plan is not legal advice)
- Whether federal calling-hour rules apply to a church's informational texts to members who opted in (Fold applies the stricter default anyway)
- Telnyx local number price: $1/mo on the pricing page vs $1.10/mo in Telnyx's 10DLC guide
- Throughput limits for toll-free numbers, which are provider-specific
- How each provider's brand "entity type" options treat a church that is not a 501(c)(3); ask the provider rather than claim nonprofit tax status
- Whether Telnyx's Charity use case excludes religious organizations as Twilio's does
- Postmark inbound processing pricing (inbound is documented, but its price was not checked; pricing not verified)
- Whether Resend and Mailgun inbound routing can carry the reply relay (7.8) when their adapters are built
- Web push (M3.13): iOS answers 404 or 410 for a revoked Home Screen web app subscription; FCM answers 410; third-party iOS browsers' Home Screen apps receive push; `navigator.standalone` outside Safari; exact Settings paths on each OS; Firefox for Android and Samsung Internet receiving push (the directions don't promise it); the Apple, Microsoft and Samsung push hostnames
- The Render items below matter only if M5.5 is ever started:
- Exactly which header and proxy addresses Render uses for client IPs, so the `render` preset for `FOLD_TRUSTED_PROXIES` must be checked against Render's docs in M5.5
- Whether Render Postgres storage pricing includes the first GB (the estimate in 12.5 assumes it does not)
- Whether an empty `ipAllowList` in `render.yaml` blocks all outside access to the database (check the Blueprint spec in M5.5)
- Whether Render API keys can be limited in scope (assume a key has the creating user's full access)

