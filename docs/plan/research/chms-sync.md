# Fold: roster sync with church management software (ChMS)

Status: historical research for **far-future M6.15**, not scheduled implementation. Written Oct 10, 2026 from public docs only (no accounts, trials, or vendor contact). Anything not confirmed in a primary source is marked **[unverified]**.

Reconciled 2026-10-25 with DEC-40: **one-way ChMS → Fold only; no write-back**. Vendor capabilities below are historical research, not implementation commitments; recheck before building. Field ownership and local-control locks remain open design work.

Context from `PLAN.md` that shapes this note:

- Fold is Go + Gin + GORM + PostgreSQL 16, one binary and single-tenant. `go.mod` owns the toolchain; no Python (3.2).
- The future provider architecture uses small interfaces, encrypted ProviderConfig secrets, contract tests and authenticated webhooks (5.6, C8, C17). This is not installed ChMS infrastructure.
- One email = one `User` account (DEC-23). Shared emails become `adult` `HouseholdPerson` rows. No accounts under 13; children are `HouseholdPerson(kind=child)`; teens 13–17 need `TeenConsent`; every guardian link to a minor needs an admin to confirm it with a `check_note` (DEC-8, DEC-32). Leaders never see teen email/phone (DEC-31). Imported phones are never SMS-opted-in (10.4).
- ChurchTrac CSV import is already "Later" and blocked on a real sample export (DEC-15, M6.11; the sample-import prerequisite to M6.15). People CSV import is M4.5.

---

## 1. Comparison table

| System | Public API? | Auth | People / households / groups: read · write | Change detection | Rate limit | API cost / tier | CSV fallback |
|---|---|---|---|---|---|---|---|
| **ChurchTrac** | **No** ("API is closed") | n/a | n/a | n/a | n/a | n/a | Yes: People → Custom Reports → Export to CSV |
| **Planning Center People** (+ Groups) | Yes, JSON:API | Personal Access Token (Basic) for one org; OAuth 2 (+PKCE) for multi-church apps | People, emails, phones, households, household memberships: **R/W**. Groups: read; group create **not** in API (PATCH only); memberships: **create** | **Webhooks** (HMAC-SHA256, retries) + `where[updated_at]` polling | 100 req / 20 s per user (headers; don't hard-code) | API free; People product free; Groups priced separately | Yes (People lists/exports) [unverified exact UI] |
| **Breeze** | Yes (all GET, query params) | Account API key in `Api-Key` header (Account Owner) | People, families, tags (= groups), events, attendance: **R/W** | **Polling** only; `account/list_log` gives `person_updated` etc. since a date; no webhooks found | **20 req/min**, ~3.5 s between calls | Included in the single plan ($72/mo [secondary source]) ; no API support from Breeze since Feb 2020 | Yes |
| **Pushpay ChMS (CCB)** | Yes: v1 (XML) and v2 (JSON) | v1: HTTP Basic with a per-church "API user"; v2: OAuth 2 auth-code, credentials issued by Pushpay on request | v1 `individual_profiles` (incl. family, allergies, privacy); v2 individuals, families (create), group members (PUT): **R/W** | v1 `modified_since` polling; ChMS webhooks not documented (only Giving webhooks) | v1: 60 burst, 1 req/s sustained per subdomain+endpoint, plus a daily per-user cap; v2: [unverified] | v1 "available for all organizations"; some services need "purchased permission"; v2 needs approved app | Yes [unverified UI] |
| **Realm (ACS)** | **No public API found** (partner integrations only) | n/a [unverified partner auth] | n/a | n/a | n/a | n/a | Yes: Custom Query → List (CSV) |
| **Elvanto** (Tithe.ly) | Yes | OAuth 2 (scopes `ManagePeople`, `ManageGroups`…) or API key (Basic) | People (incl. `family_id`, `family_relationship`, birthday), groups, `groups/addPerson`/`removePerson`: **R/W** | Webhooks only for `peopleflow:member:add` and `form:submission:create`; otherwise poll (`date_modified` returned) | Not documented [unverified] | [unverified] | Yes [unverified UI] |
| **Tithe.ly ChMS** (non-Elvanto) | Giving API by request; no public people API found | Keys by email request | [unverified] | [unverified] | [unverified] | By request | [unverified] |
| **Rock RMS** | Yes (v1 and v2 REST, self-hosted) | REST key (`Authorization-Token`) | Every entity (Person, Group, GroupMember, Family = Group type): full **CRUD** | No native outbound webhooks; **Workflow Triggers** (post-save) + "Web Request" action can push [unverified wiring]; else poll `ModifiedDateTime` [unverified] | Self-hosted; none documented [unverified] | Free (Rock Community License; not OSI open source) | Yes (reports/data views) |
| **Fellowship One** | Yes (REST v1, old) | OAuth 1.0a-style (1st/2nd/3rd-party consumer keys) | People, households, addresses, communications: **R/W** (3rd party). Groups realm: groups CRUD; members search | `People/Search?lastUpdatedDate=` polling; no webhooks found | [unverified] | Keys by emailing F1 support; fees [unverified] | [unverified] |
| **ChMeetings** | Yes (OpenAPI spec public) | API key in `apikey` header | People, families (+members), groups (+memberships): full **CRUD** | **Webhooks** (People, Contributions, Funds, Attendance) | 100 req/s | Listed on paid plans (V. Small and up); free plan unclear [unverified] | Yes [unverified UI] |
| **One Church Software** | Yes | API key `Ocs-Api-Key` | People, groups read; "read and write" stated | Webhooks menu exists ("API / Webhooks") [details unverified] | 30 req / 10 s per key | Account required | [unverified] |
| **Subsplash** | Yes, docs gated | OAuth credentials provisioned by Subsplash | People: profiles, households; [writes unverified] | Webhooks | [unverified] | Via Client Success Manager ("eligibility, options, or pricing") | [unverified] |
| **PowerChurch Plus** | No | n/a | n/a | n/a | n/a | n/a | Yes: Export Data (Excel/CSV) or Access MDB |
| **Church Windows** | No (desktop) | n/a | n/a | n/a | n/a | n/a | Yes: Mail Merge/Export → CSV |
| **Servant Keeper** | No public API found | n/a | n/a | n/a | n/a | n/a | Yes (export tool) |
| **ShelbyNext** | No general public API found (only Membership↔Financials sync); Arena (old) has `/api.svc/help` | [unverified] | [unverified] | [unverified] | [unverified] | [unverified] | [unverified] |
| **Faithlife Equip** | **Discontinued** (ChMS sunset June 30, 2023) | n/a | n/a | n/a | n/a | n/a | n/a |

Takeaway: of the systems most common with small churches, **Planning Center** and **Breeze** have real, self-serve APIs; **ChMeetings** has the cleanest small-church API; **ChurchTrac, Realm, PowerChurch, Church Windows, Servant Keeper** need CSV.

---

## 2. Per-system notes

### 2.1 ChurchTrac (Fold's secondary UI reference)

- **API:** none. ChurchTrac's own FAQ: "ChurchTrac's API is closed. You can use one of our many integrations offered with MinistrySafe, Stripe, SongSelect, and more." (https://www.churchtrac.com/support/getting_started/churchtrac-faqs). A third-party API catalog reached the same conclusion: no REST API, keys, OAuth, or webhooks (https://github.com/api-evangelist/churchtrac).
- **Export:** People/Family → Custom Reports → "+" → include "Everyone" (or a Smart List) → add fields → **Export to CSV**; giving via Giving Reports → Export to CSV (https://support.breezechms.com/hc/en-us/articles/360024510154-ChurchTrac-Export-Instructions; also https://www.churchtrac.com/support/people/using-reports). Because the user picks the fields, **column names vary per church** — Fold's importer needs a column-mapping step, which matches DEC-15's "get a real sample first".
- **ToS on sync:** nothing found about third-party sync; with no API it is moot. Two-way sync is impossible; at most a repeatable CSV import.

### 2.2 Planning Center People (and Groups)

- **Docs:** https://api.planningcenteronline.com/docs (redirect from https://developer.planning.center/docs/). Single JSON:API 1.0 API across products (https://www.planningcenter.com/developers).
- **Auth:** PAT (HTTP Basic, app id + secret) for "a script that runs on … a server you control", one org; OAuth 2 for apps used by multiple churches; access tokens last 2 h with refresh tokens; scopes `people`, `groups`, `check_ins`, etc.; since Mar 22, 2023 only Organization Administrators can create OAuth apps (https://api.planningcenteronline.com/docs/overview/authentication). Every request acts as a user and sees only what that user can see.
  - *Fit for Fold:* single-tenant, self-hosted Fold matches the **PAT** model (the church admin creates a PAT for a dedicated Planning Center user). OAuth would need church/operator credential configuration; never ship a shared client secret. Recheck vendor terms before choosing PAT authentication. Use a dedicated "Fold sync" PCO user so permissions are explicit.
- **People data (R/W):** `POST/PATCH/DELETE /people/v2/people` with attributes incl. `first_name`, `last_name`, `birthdate`, `child`, `grade`, `gender`, `medical_notes`, `membership`, `status`, `remote_id` (https://api.planningcenteronline.com/docs/apps/people/versions/2026-10-05/vertices/person). Emails are child resources (`POST /people/v2/people/{id}/emails`, `PATCH /people/v2/emails/{id}`, `address`, `primary`). Query filters include `where[updated_at]`, `where[created_at]`, `where[child]`, `where[remote_id]`, `where[search_name_or_email]`.
- **Households (R/W):** `POST /people/v2/households` with `primary_contact` and `people` relationships; `POST /people/v2/households/{id}/household_memberships` (`household_role`, `pending`) (https://api.planningcenteronline.com/docs/apps/people/versions/2026-10-05/vertices/household).
- **Groups:** list/read groups, memberships, group types, events. **Groups cannot be created via the API**: only `PATCH /groups/v2/groups/{id}` (name, group_type_id, schedule, tag_ids) plus a `duplicate` action (https://api.planningcenteronline.com/docs/apps/groups/versions/2023-07-10/vertices/group). Memberships can be created: `POST /groups/v2/groups/{group_id}/memberships` (`joined_at`, `role`, `person_id`) (https://api.planningcenteronline.com/docs/apps/groups/versions/2023-07-10/vertices/membership); it now works for people only in People, with a short delay (https://github.com/planningcenter/developers/issues/755). Membership delete/update: [unverified].
- **Change detection:** webhooks, configured per user at https://api.planningcenteronline.com/webhooks; POST only; HMAC-SHA256 in `X-PCO-Webhooks-Authenticity`; up to 16 retries over ~5 days; 410 deactivates; no webhook rate limit; example event `people.v2.events.person.updated` (https://api.planningcenteronline.com/docs/overview/webhooks). Full list of event names (household, email, groups membership events): [unverified]. Webhook subscriptions can be created by OAuth apps; by API with a PAT: [unverified]. Fallback: poll `where[updated_at]`.
- **Rate limit:** 100 requests / 20 s per authenticated user; offsets > 30,000 get 75 / 20 s; headers `X-PCO-API-Request-Rate-*`, `Retry-After` on 429; "should never hard-code rate limit values" (https://api.planningcenteronline.com/docs/overview/rate-limiting). Pagination max 100/page.
- **Cost:** People is free for every church (https://help.planningcenter.com/en/136790-planning-center-pricing.html). Groups is a paid product with a small free tier; exact tiers [unverified] (https://www.planningcenter.com/pricing).
- **ToS:** automated access is prohibited "unless said means is by an API that we have specifically approved of" (https://www.planningcenter.com/terms) — using the public API is fine; scraping is not. Add-on guidelines require OAuth for add-ons that embed in PCO (https://api.planningcenteronline.com/docs/overview/add-ons/guidelines); Fold would not be an add-on.
- **Child data present:** `child`, `grade`, `medical_notes`, check-ins. See section 3.5 for what not to import.

### 2.3 Breeze ChMS

- **Docs:** https://app.breezechms.com/api. Base `https://{subdomain}.breezechms.com/api`. Wrappers (PHP/Python/Node) "not necessarily created or maintained by Breeze".
- **Auth:** account API key, obtained by the Account Owner at Manage Account → API Key (https://support.breezechms.com/hc/en-us/articles/360001324153-API-Advanced-Custom-Development); sent as `Api-Key` header (https://github.com/api-evangelist/breeze-chms). One key = full account access; no scopes found.
- **Data:** People list/show/add/update/delete, profile fields (custom), `filter_json`; Tags + tag folders with assign/unassign (Breeze uses tags as its group mechanism); Families create/destroy/add/remove; Events, calendars, attendance add/delete/list; Volunteers; Forms; Account summary and **log** (from the docs page endpoint list).
- **Change detection:** no webhooks found. `GET /api/account/list_log?action=person_updated&start=YYYY-MM-DD` returns actions with object ids — a usable change feed for incremental sync (doc example). Other action names: [unverified].
- **Rate limit:** **20 requests/minute**; exceed = temporary block; wait ~3.5 s between calls (official support article above). A full sync of 500 people with `details=1` is one or a few calls, but per-person writes are slow (≈3 per minute-ish with retries) — write-back must be batched and patient.
- **Cost:** one plan, $72/mo, all features ([secondary] https://support.breezechms.com/hc/en-us/articles/41089545597207-Learn-More-About-Breeze-ChMS). API included.
- **ToS / support:** "Post Feb 1, 2020, our Support team is unable to provide support for the utilization of the API." Third-party sync restrictions: none found [unverified]. Note Breeze is now owned by Tithe.ly [unverified].

### 2.4 Pushpay ChMS (Church Community Builder)

- **Docs:** https://docs.pushpay.io/ — "ChMS API v1: Bulk data & onboarding", "ChMS API v2: Everyday church management".
- **v1:** XML over HTTPS, HTTP Basic with API logins created in the church's **API Admin**; each login can be limited to specific services; error 111 = "service is restricted and requires purchased permission" (https://docs.pushpay.io/chms-v1/docs/authentication). "Access to the API is available for all Pushpay ChMS organizations" (https://docs.pushpay.io/chms-v1/docs/getting-started). `individual_profiles?modified_since=` returns profiles created or modified since a date, with family, family position, birthday, allergies, privacy settings (https://docs.pushpay.io/chms-v1/reference/individual_profiles).
- **v1 rate limits:** token bucket 60 burst, 1 req/s sustained, keyed by subdomain + endpoint, plus a daily per-user limit (`api_status`) (https://docs.pushpay.io/chms-v1/docs/rate-limiting).
- **v2:** JSON at `https://api.ccbchurch.com`, OAuth 2 auth code; you must **submit an API access request form** to get an app; a church admin with "edit system-wide settings" authorizes once; 2 h tokens; rotating single-use refresh tokens (https://docs.pushpay.io/chms-v2/docs/getting-started). Endpoints include create family (https://docs.pushpay.io/chms-v2/reference/createfamily) and update group members (https://docs.pushpay.io/chms-v2/reference/updategroupmembers). v2 rate limits: [unverified]; a Pushpay page lists tiered limits but appears to be for the Giving API (https://pushpay.io/docs/guidance/rate_limiting) [unverified for ChMS].
- **Webhooks:** documented only for giving ("Stream new giving transactions") (https://docs.pushpay.io/). ChMS change webhooks: not found.
- **Fit:** v1 is self-serve per church (good for self-hosted Fold); v2 needs Pushpay to approve a vendor app (awkward for open source).

### 2.5 Realm (ACS Technologies)

- **API:** no public Realm developer API found. ACS's published REST API is for **MinistryPlatform**, a different ACS product (https://help.acst.com/en/ministryplatform/developer-resources/developer-resources/rest-api) — don't confuse the two. Realm's integration page lists named partners only (Constant Contact, Planning Center People one-way sync, Growth Method, Go Method, Verified First…) (https://www.acstechnologies.com/realm/tools/integrations/). A partner API presumably exists behind those [unverified].
- **Export:** Custom Query → List (CSV) → "Individual List" (https://help.acst.com/en/realm/how-to/reporting/custom-queries/export-query-lists-to-other-apps).
- **Fit:** CSV only.

### 2.6 Elvanto (Tithe.ly) and Tithe.ly ChMS

- **Docs:** https://www.elvanto.com/api/getting-started. Endpoint `https://api.elvanto.com/v1/{method}.json`.
- **Auth:** OAuth 2 (app registered under the church's Settings → Integrations; scopes `ManagePeople`, `ManageGroups`, `ManageCalendar`, `AdministerAccount`…; tokens ~14 days + refresh) **or** an API key via HTTP Basic.
- **Data:** people getAll/search/getInfo/create/edit/remove (https://www.elvanto.com/api/people/); getAll returns `family_id`, `family_relationship`, `birthday`, `date_modified`, `archived`, `contact` (https://www.elvanto.com/api/people/getAll/); groups getAll/getInfo/addPerson (with `position`, e.g. Leader)/removePerson (https://www.elvanto.com/api/groups/, https://www.elvanto.com/api/groups/addPerson/).
- **Webhooks:** only `peopleflow:member:add` and `form:submission:create` (https://help.elvanto.com/hc/en-us/articles/7625995285143-How-to-Set-up-Webhooks). Signature verification: not documented [unverified]. Everything else: poll.
- **Rate limit:** not documented [unverified].
- **Tithe.ly ChMS** (the US product, distinct from Elvanto) — the public Tithe.ly API is giving-focused and "available by request" via support@tithe.ly (https://docs.tithe.ly/reference/introduction). A people/groups API for Tithe.ly ChMS: not found [unverified]. Tithe.ly → Elvanto giving sync exists (https://help.tithe.ly/hc/en-us/articles/7793227557911-Setting-Up-The-Elvanto-ChMS-Integration).

### 2.7 Rock RMS

- **Docs:** https://community.rockrms.com/documentation/supporting-rock/data/api/intro-to-the-rock-api?Version=v19.0. API v1 and v2 (v2 recommended), with Get/Put/Patch/Post/Delete per entity; interactive docs inside each install (Admin Tools → Settings → API v2 Docs).
- **Auth:** REST keys (Admin Tools → Settings → REST Keys), sent as `Authorization-Token` (https://community.rockrms.com/documentation/supporting-rock/data/api/rest-keys?Version=v19.0). The key is tied to a Rock person whose security roles decide access.
- **Data:** everything (Person, Group, GroupMember; families are a Group type) is read/write.
- **Change detection:** inbound webhooks launch workflows (https://community.rockrms.com/documentation/core-concepts/workflows/advanced-workflows/configure-a-webhook-to-a-workflow); entity **Workflow Triggers** (pre/post save) exist (https://community.rockrms.com/documentation/core-concepts/workflows/entry-forms/launch-a-workflow). A post-save trigger + Web Request action could notify Fold, but that is church-side configuration [unverified as a supported pattern]. Polling on modified dates [unverified field name].
- **Cost/licence:** free to self-host on Windows/IIS + SQL Server; Rock Community License limits use to faith-based organizations and is not OSI open source (https://www.rockrms.com/license, https://github.com/SparkDevNetwork/Rock/issues/5068).
- **Fit:** Rock skews to larger churches with IT staff [unverified market claim]; a Rock church is likely to want Rock as hub and might build its own Fold connector.

### 2.8 Fellowship One (Ministry Brands)

- **Docs:** https://developer.fellowshipone.com/docs/v1/util/docs.help (People realm) and https://developer.fellowshipone.com/docs/groups/v1/util/docs.help (Groups realm). Auth: OAuth 1.0-style with 1st/2nd/3rd-party consumer keys and a vendor-to-vendor mode (https://developer.fellowshipone.com/docs/v1/util/authdocs.help).
- **Data:** Households and People search/show/create/update; addresses, communications (emails/phones), attributes, requirements (background checks) — all "3rd party". Groups: search/list/show/create/update; group members search (https://developer.fellowshipone.com/docs/groups/v1/Members.help). People search supports `lastUpdatedDate` [per search summary; unverified on page].
- **Access:** request staging/production keys by emailing F1Support@fellowshipone.com with church code, app name, and realms (People, Groups, Events) (https://support.fellowshipone.com/hc/en-us/articles/19311064751515-Questions-about-the-FellowshipOne-API). Churches enable/disable third-party apps (https://support.fellowshipone.com/hc/en-us/articles/18730448362523-Enabling-Disabling-3rd-Party-Applications). Fees, rate limits, webhooks: not found [unverified].
- **Fit:** legacy API, mid/large churches; low priority.

### 2.9 ChMeetings

- **Docs:** https://api.chmeetings.com/scalar/ (OpenAPI at https://api.chmeetings.com/openapi/v1.json), guide at https://help.chmeetings.com/hc/en-us/articles/4407466673937-Developer-API-Guide, developer page https://www.chmeetings.com/developers/.
- **Auth:** account API key (Settings → Integrations → API Integration → Get API), `apikey` header. Rotating the key breaks all apps.
- **Data (from the OpenAPI spec):** `GET/POST/PUT/DELETE /api/v1/people` (filters: email, mobile, name, group_ids, has_email, `do_not_email`, `do_not_text`, family_roles, `include_family_members`, `include_groups`); `/families` with members add/update/remove and `family-roles`; `/groups` CRUD and `/groups/{id}/memberships` add/remove; events, occurrences, attendance, check-ins; contributions. No "modified since" filter on people [as read from the spec].
- **Webhooks:** `/api/v1/integrations/webhooks` CRUD and `/webhooks/events`; event categories "People, Contributions, Contribution Funds or Attendance". Signature scheme: [unverified].
- **Rate limit:** 100 requests/second (guide).
- **Cost:** guide lists V. Small through Diocese plans; whether the free plan includes the API is [unverified] (https://www.chmeetings.com/pricing).
- **ToS:** third-party integrations are invited ("Submit Integration"); restrictions [unverified].

### 2.10 Others (brief)

- **One Church Software:** API key `Ocs-Api-Key`, managed under "API / Webhooks"; "read and write data"; **30 requests / 10 s per key**, `Ocs-Rate-Limit-Remaining`, 429 + `Retry-After`; base `https://{tenant}.onechurchsoftware.com/api` (https://developers.onechurchsoftware.com/docs/getting-started, https://developers.onechurchsoftware.com/reference/get_people). Households/groups writes and webhook events [unverified].
- **Subsplash:** REST API with People (Profiles, Households) and Webhooks; docs access by request and credentials via the church's Client Success Manager ("eligibility, options, or pricing") (https://support.subsplash.com/en/articles/11610463-subsplash-api). Paid/gated — low priority.
- **PowerChurch Plus:** no API; export to Excel/CSV or Access MDB (https://www.powerchurch.com/forum/viewtopic.php?t=3056, https://support.breezechms.com/hc/en-us/articles/360025643773-PowerChurch-Export-Instructions).
- **Church Windows:** desktop; Membership → Reports → Directory → Export → CSV (https://help.churchteams.com/knowledge/how-to-export-church-windows-information-to-a-csv-file-for-importing-into-churchteams).
- **Servant Keeper:** export tool (https://help.servantkeeper.com/v8/ImportTool/exporting_your_data.htm); public API not found.
- **ShelbyNext:** only a Membership↔Financials sync is documented (http://www.shelbyinc.com/Documents/PDFs/ShelbyNext/How_to_Use_Membership_and_Financials_API_Sync_Integration.pdf); Arena exposes `/api.svc/help` to clients (https://community.shelbysystems.com/arena/f/arena-public-forum/4413/api-documentation). General API [unverified].
- **Faithlife Equip:** ChMS, check-in, and giving sunset June 30, 2023; data not exported by then was deleted (https://churchtechtoday.com/faithlife-end-of-faithlife-equip-chms/, https://www.logos.com/grow/faithlife-strategic-direction-moving-forward/). Faithlife's remaining API covers accounts and groups for its Bible/community products (https://developer.faithlife.com/). Not a sync target.

---

## 3. Recommendation for Fold

### 3.1 Sync adapter interface (matches 5.6)

A new provider kind `chms` in `ProviderConfig` (one active row), adapters in `internal/providers/chms/<name>`, registered by name, secrets encrypted per C8. Sketch (Go-flavoured but language-neutral):

```go
// What an adapter can do; drives UI, job scheduling, and the rate limiter.
type ChMSCaps struct {
    ReadPeople, ReadHouseholds, ReadGroups, ReadMemberships bool
    ChangeFeed   ChangeFeedKind // none | modified_since | action_log | webhook
    Webhooks     bool
    RateLimit    RateLimit      // tokens, per duration, burst; overridden by response headers
    MaxPageSize  int
    ChildFlag    bool           // source marks children/minors explicitly
}

// Normalized records. ExternalID is the provider's stable id; never a Fold id.
type ExtPerson struct {
    ExternalID, HouseholdExternalID, HouseholdRole string // head|spouse|adult|child|other
    FirstName, LastName, Nickname string
    Emails []ExtEmail   // address, primary
    Phones []ExtPhone   // number, kind (mobile|home|work)
    BirthDate *Date; IsChild *bool
    Status string       // active|inactive|deceased|visitor (mapped)
    UpdatedAt time.Time
    Raw json.RawMessage // kept only in the sync staging table, purged after review
}
type ExtHousehold struct { ExternalID, Name string; PrimaryContactExternalID string; UpdatedAt time.Time }
type ExtGroup     struct { ExternalID, Name, Kind string; Archived bool; UpdatedAt time.Time }
type ExtMembership struct { GroupExternalID, PersonExternalID, Role string } // leader|member

// Required for every adapter (CSV included).
type RosterSource interface {
    Capabilities() ChMSCaps
    VerifyCredentials(ctx context.Context) error
    ListPeople(ctx context.Context, cur Cursor) (Page[ExtPerson], error)
    ListHouseholds(ctx context.Context, cur Cursor) (Page[ExtHousehold], error)
    ListGroups(ctx context.Context, cur Cursor) (Page[ExtGroup], error)
    ListMemberships(ctx context.Context, groupExtID string, cur Cursor) (Page[ExtMembership], error)
}

// Optional: incremental pulls.
type ChangeFeed interface {
    Changes(ctx context.Context, since Checkpoint) (ChangeSet, Checkpoint, error) // ids to refetch + deletions
}

// Optional: push notifications, same shape as the email/SMS WebhookParser.
type ChMSProvisioner interface {
    EnsureWebhook(ctx context.Context, url, secret string, events []string) error
}
// WebhookParser (existing) returns []ProviderEvent{Kind: person.updated|person.deleted|membership.*, ExternalID}.
// Handlers only enqueue a refetch job; they never trust payload contents as final.
```

Adapters: `csv` (file upload + saved column mapping; covers ChurchTrac, Realm, PowerChurch, Church Windows, Servant Keeper), `planningcenter`, `breeze`, later `chmeetings`, `ccb` (v1), `elvanto`. A `devfake` adapter backs tests; each real adapter gets an `httptest.Server` contract test built from the docs above, and `make live-test PROVIDER=planningcenter` for humans, as 5.6 requires.

Supporting tables (sketch):

- `ExternalLink(provider, external_type person|household|group, external_id, fold_type user|household_person|household|group, fold_id, last_seen_hash, last_synced_at)` — unique `(provider, external_type, external_id)`.
- `SyncRun(provider, kind full|incremental, started_at, finished_at, stats_json, error)`.
- `SyncProposal(run_id, action create_account|create_household_person|link|merge|update_field, payload_json, status pending|applied|rejected, decided_by, decided_at)` — the review queue.
- Field ownership/locking remains to be designed. ChMS-owned fields may be pulled; Fold-owned fields are never pushed.

Future jobs: `chms.full_sync` (nightly, advisory-locked), `chms.incremental` (where a change feed exists), and `chms.refetch` (verified webhooks). Reads honor provider limits and response headers. No write-back job or sink interface (DEC-40).

### 3.2 One-way import only (DEC-40)

Start with admin-reviewed CSV import, sharing M4.5's machinery. Later add
read adapters and scheduled or webhook-driven refresh. Preserve Fold's account
approval, consent and safeguarding boundaries. Fold never writes people,
memberships, attendance or identifiers back to the ChMS. Vendor write APIs
are comparison facts only, not future Fold scope.

### 3.3 Source of truth and conflict rules

Use **field ownership**, not last-write-wins:

| Data | Owner | Rule |
|---|---|---|
| Person name, address, household structure | ChMS (DEC-40) | Pull; design local-control locks before implementation. Age/identity changes must preserve Fold's approval and safeguarding checks. |
| Email used for login | **Fold** after the account exists | A ChMS email change becomes a proposal; applying it runs Fold's verified email-change flow (10.1). Never silently change a login. |
| Phone | ChMS for the value; **Fold** for SMS consent | A changed number clears `phone_verified_at` and SMS consent in Fold (number ownership must be re-proved, 7.4). |
| Consent (email/SMS), suppression, `TeenConsent`, guardian-link confirmation, contact grants | **Fold only** | Never imported as granted; never exported. |
| Fold roles (admin/leader/host), registrations, RSVPs, waitlists | Fold only | Not synced. |
| Group memberships | Fold (DEC-40) | No push or automatic takeover of the roster; settle imported metadata and local-control locks before implementation. |
| Deletions / inactivation in ChMS | ChMS | Become a proposal to disable the Fold account (never auto-delete; audit and consent history are kept, 5.3). |

Conflicts (same field changed on both sides since the last sync, detected via `last_seen_hash`) never auto-resolve; they become `SyncProposal(update_field)` rows. All applied changes write `AuditLog` rows (`chms.*` actions).

### 3.4 Matching and de-duplication of people

1. **Existing link wins:** a local `ExternalLink` stores the external id. Never write Fold's id into the ChMS (DEC-40).
2. **Email match (case-insensitive, `citext`)** for unlinked records. One Fold account per email (DEC-23):
   - If one ChMS person has the email → propose link/create account.
   - If several ChMS people in **one household** share it → the household's primary contact (PCO `primary_contact`, Breeze/CCB/Elvanto "Primary Contact"/"Head") becomes the account; the others become `adult` `HouseholdPerson`s on that account (exactly what 5.10 already says for CSV). Children sharing a parent's email are children, not adults (3.5).
   - If people in **different households** share an email → no automatic action; admin decides.
   - If the email already belongs to a Fold guest row → link and offer the guest upgrade path (C5), still with admin approval.
3. **No email:** adults with no email become `HouseholdPerson(adult)` only if their household has an account holder; otherwise they stay in staging as "roster only" (visible to admins, not in Fold). Never invent placeholder emails (5.10).
4. **Fuzzy matches** (same normalized name + birth date, or same name + phone) are suggestions only, shown side by side; never auto-merged.
5. Multiple emails per ChMS person: use the one marked primary; keep others as match keys only.

### 3.5 Minors' data and consent

Fold's rules (no accounts under 13; teens need consent; admin confirms every guardian link; leaders never see teen contact info; audit everything) mean imports must be **more** conservative than the ChMS:

- **Age gate on import:** anyone the source marks as a child (PCO `child`, grade set, household role child) or with a birth date under 18 is treated as a minor. Under 13 → only `HouseholdPerson(kind=child)` under a household with an account holder. 13–17 → `HouseholdPerson(kind=teen)` by default, **never** an account, even if the ChMS has their own email. A teen account is created only through Fold's normal teen flow (`pending_consent` → `TeenConsent account`). Missing birth date + child flag → treat as child; missing both → treat as adult only if the admin confirms.
- **Guardian links:** ChMS household membership is *evidence*, not confirmation. Import creates `GuardianLink(status=pending)`; an admin confirms (with DEC-79's self-confirmation restriction; bulk confirmation has not been approved) with a `check_note` such as "From Planning Center household 'Smith', reviewed by <admin>" (DEC-32 allows no auto-confirm).
- **Consent is never imported.** ChMS "photo release", "can text", "receive email" flags do not create `TeenConsent`, `Consent`, or `photo_consent`. Imported phones are not opted in (10.4). (Optionally show the ChMS flag to the admin as a hint.)
- **Do NOT import about children/teens:**
  - medical notes, allergies, medications, special needs (PCO `medical_notes`, CCB `allergies`, check-in notes) — these stay in the ChMS/check-in system; if a church wants them in Fold `care_notes`, a guardian enters them;
  - custody / legal / restraining-order notes, pastoral or counseling notes, background or incident notes;
  - child check-in data: security codes, pickup authorizations, kiosk labels, attendance history;
  - photos of minors;
  - school name, grade, graduation year (Fold needs only birth date);
  - a minor's own email/phone (household people have no contact fields in Fold);
  - giving, contribution, or pledge data for anyone (out of scope per 13.3);
  - ChMS privacy settings and custom fields, by default.
- **No write-back for anyone**, including adults; never push consent, contact grants or Fold messages (DEC-40).
- **Leaders:** imported people still follow Fold's contact/badge policy regardless of what the ChMS exposes. Import does not take ownership of Fold group memberships (DEC-40).
- **Audit:** every import decision about a minor (create, link proposed/confirmed, field skipped) is an `AuditLog` row that is never purged, as 5.3 already requires for minors.
- **Staging hygiene:** the `Raw` payload is kept only until the proposal is decided, then purged, so ChMS medical fields don't linger in Fold's DB or backups. Better: adapters drop those fields at parse time.

### 3.6 Which systems first

1. **Generic CSV adapter (with saved column mappings)** — first. It reuses M4.5, covers ChurchTrac (Fold's own reference, closed API), Realm, PowerChurch, Church Windows, and Servant Keeper, and unblocks DEC-15 once a sample export exists. One-way only.
2. **Planning Center** — first API adapter. People is free, so it's extremely common among small churches [market share unverified]; it has the best API (JSON:API, R/W people and households, webhooks with HMAC, clear rate-limit headers); evaluate PAT auth for single-tenant read-only access; Fold never pushes group memberships.
3. **Breeze** — second API adapter. Popular with small churches [unverified], flat price with API included, simple key auth, R/W people/families/tags, and `account/list_log` for incremental pulls. Design reads around 20 req/min (bulk `details=1`, no documented webhooks).

Next candidates: **ChMeetings** (cheapest, cleanest API: full CRUD, webhooks, 100 req/s — great if Fold churches use it), then **CCB v1**. Skip Realm (no public API), Faithlife (gone), Subsplash (gated/paid), F1 (legacy), Rock (its churches will want Rock as the hub).

---

## 4. Gaps and things to verify before building

- Planning Center: full webhook event list; whether a PAT user can create webhook subscriptions by API; group membership delete/role update; Groups pricing tiers and read-only authentication terms.
- Breeze: complete list of `list_log` action names (deletions, family changes, tag assignment); any ToS clause on third-party sync; current ownership.
- CCB/Pushpay v2: rate limits and whether ChMS webhooks exist; whether v1 will be retired.
- Elvanto: rate limits; webhook signatures; whether the US "Tithe.ly ChMS" has any people API.
- ChMeetings: whether the free plan includes the API; webhook signing; whether there's a "modified since" filter elsewhere.
- Realm: whether a partner API program exists and its terms.
- Rock: change-detection field names and API rate limiting.
- ChurchTrac: a real Custom Report CSV sample (DEC-15).
- Market share for small churches: no reliable public numbers found; the priority order relies on API quality and cost, not share.

---

## 5. Sources

- ChurchTrac FAQ (closed API): https://www.churchtrac.com/support/getting_started/churchtrac-faqs
- ChurchTrac reports: https://www.churchtrac.com/support/people/using-reports
- ChurchTrac export (Breeze guide): https://support.breezechms.com/hc/en-us/articles/360024510154-ChurchTrac-Export-Instructions
- API Evangelist ChurchTrac profile: https://github.com/api-evangelist/churchtrac
- Planning Center developer overview: https://www.planningcenter.com/developers
- PCO API docs: https://api.planningcenteronline.com/docs
- PCO authentication: https://api.planningcenteronline.com/docs/overview/authentication
- PCO rate limiting: https://api.planningcenteronline.com/docs/overview/rate-limiting
- PCO webhooks: https://api.planningcenteronline.com/docs/overview/webhooks
- PCO Person: https://api.planningcenteronline.com/docs/apps/people/versions/2026-10-05/vertices/person
- PCO Household: https://api.planningcenteronline.com/docs/apps/people/versions/2026-10-05/vertices/household
- PCO Group: https://api.planningcenteronline.com/docs/apps/groups/versions/2023-07-10/vertices/group
- PCO Membership: https://api.planningcenteronline.com/docs/apps/groups/versions/2023-07-10/vertices/membership
- PCO membership issue #755: https://github.com/planningcenter/developers/issues/755
- PCO add-on guidelines: https://api.planningcenteronline.com/docs/overview/add-ons/guidelines
- PCO terms: https://www.planningcenter.com/terms
- PCO pricing: https://help.planningcenter.com/en/136790-planning-center-pricing.html, https://www.planningcenter.com/pricing
- Breeze API reference: https://app.breezechms.com/api
- Breeze API support article (20/min): https://support.breezechms.com/hc/en-us/articles/360001324153-API-Advanced-Custom-Development
- Breeze pricing: https://support.breezechms.com/hc/en-us/articles/41089545597207-Learn-More-About-Breeze-ChMS
- Breeze API profile: https://github.com/api-evangelist/breeze-chms
- Pushpay developer home: https://docs.pushpay.io/
- Pushpay ChMS v1 getting started / auth / rate limiting / individual_profiles: https://docs.pushpay.io/chms-v1/docs/getting-started, https://docs.pushpay.io/chms-v1/docs/authentication, https://docs.pushpay.io/chms-v1/docs/rate-limiting, https://docs.pushpay.io/chms-v1/reference/individual_profiles
- Pushpay ChMS v2 overview / create family / update group members: https://docs.pushpay.io/chms-v2/docs/getting-started, https://docs.pushpay.io/chms-v2/reference/createfamily, https://docs.pushpay.io/chms-v2/reference/updategroupmembers
- Pushpay rate-limit guidance (likely Giving): https://pushpay.io/docs/guidance/rate_limiting
- Realm integrations: https://www.acstechnologies.com/realm/tools/integrations/
- Realm export: https://help.acst.com/en/realm/how-to/reporting/custom-queries/export-query-lists-to-other-apps
- MinistryPlatform REST API (not Realm): https://help.acst.com/en/ministryplatform/developer-resources/developer-resources/rest-api
- Elvanto API: https://www.elvanto.com/api/getting-started, https://www.elvanto.com/api/people/, https://www.elvanto.com/api/people/getAll/, https://www.elvanto.com/api/groups/, https://www.elvanto.com/api/groups/addPerson/
- Elvanto webhooks: https://help.elvanto.com/hc/en-us/articles/7625995285143-How-to-Set-up-Webhooks
- Tithe.ly API access: https://docs.tithe.ly/reference/introduction
- Tithe.ly–Elvanto integration: https://help.tithe.ly/hc/en-us/articles/7793227557911-Setting-Up-The-Elvanto-ChMS-Integration
- Rock API intro / REST keys: https://community.rockrms.com/documentation/supporting-rock/data/api/intro-to-the-rock-api?Version=v19.0, https://community.rockrms.com/documentation/supporting-rock/data/api/rest-keys?Version=v19.0
- Rock webhooks to workflow / triggers: https://community.rockrms.com/documentation/core-concepts/workflows/advanced-workflows/configure-a-webhook-to-a-workflow, https://community.rockrms.com/documentation/core-concepts/workflows/entry-forms/launch-a-workflow
- Rock licence: https://www.rockrms.com/license, https://github.com/SparkDevNetwork/Rock/issues/5068
- Fellowship One API index / Groups / auth / members: https://developer.fellowshipone.com/docs/v1/util/docs.help, https://developer.fellowshipone.com/docs/groups/v1/util/docs.help, https://developer.fellowshipone.com/docs/v1/util/authdocs.help, https://developer.fellowshipone.com/docs/groups/v1/Members.help
- F1 API key requests / third-party apps: https://support.fellowshipone.com/hc/en-us/articles/19311064751515-Questions-about-the-FellowshipOne-API, https://support.fellowshipone.com/hc/en-us/articles/18730448362523-Enabling-Disabling-3rd-Party-Applications
- ChMeetings: https://help.chmeetings.com/hc/en-us/articles/4407466673937-Developer-API-Guide, https://www.chmeetings.com/developers/, https://api.chmeetings.com/scalar/, https://api.chmeetings.com/openapi/v1.json, https://www.chmeetings.com/pricing
- One Church Software: https://developers.onechurchsoftware.com/docs/getting-started, https://developers.onechurchsoftware.com/reference/get_people
- Subsplash: https://support.subsplash.com/en/articles/11610463-subsplash-api
- PowerChurch: https://www.powerchurch.com/forum/viewtopic.php?t=3056, https://support.breezechms.com/hc/en-us/articles/360025643773-PowerChurch-Export-Instructions
- Church Windows export: https://help.churchteams.com/knowledge/how-to-export-church-windows-information-to-a-csv-file-for-importing-into-churchteams
- Servant Keeper export: https://help.servantkeeper.com/v8/ImportTool/exporting_your_data.htm
- ShelbyNext / Arena: http://www.shelbyinc.com/Documents/PDFs/ShelbyNext/How_to_Use_Membership_and_Financials_API_Sync_Integration.pdf, https://community.shelbysystems.com/arena/f/arena-public-forum/4413/api-documentation
- Faithlife Equip end: https://churchtechtoday.com/faithlife-end-of-faithlife-equip-chms/, https://www.logos.com/grow/faithlife-strategic-direction-moving-forward/, https://developer.faithlife.com/
