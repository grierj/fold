# Fold design: 13. Extra features from research

## 13. Extra features from research

### 13.1 What ChurchTrac and Meetup offer (research summary)

- **ChurchTrac** (https://www.churchtrac.com/pricing): plans from $29/mo for up to 75 names to $119/mo for unlimited. "Enhanced Messaging" is $7 for 250 credits. The all-in-one product covers people and households, a member directory, groups, events and calendar, room and resource scheduling, attendance, child check-in kiosks with name tags, volunteer scheduling, forms (prayer requests, connection cards, sign-ups), email and text messaging, online giving, contribution statements and fund accounting, worship planning, a website builder and member app, MinistrySafe background-check integration, and automations.
- **Meetup** (https://help.meetup.com): groups with organizers and co-hosts; repeating events that auto-announce 6 days and 1 day before; an attendee limit with a waitlist that auto-moves people in (skipping RSVPs whose guests don't fit); up to 5 guests per RSVP; RSVP open and close times; custom RSVP questions; hybrid events with separate in-person and online limits; check-in mode (from 1 hour before to 24 hours after); discussions and photo albums; and QR check-in on its 2026 roadmap.

### 13.2 Recommendations

The tag is the product call; the parenthesis says where it stands (built, or the milestone that
plans it).

| Feature | Tag | Why |
|---|---|---|
| Recurring events with per-date changes | **Core** (built, M1.1a) | Every small group meets weekly. |
| RSVP with reminders (email 24 h, SMS 2 h) | **Core** (email built, M3.6; 2-hour push built, M3.13; SMS planned, M3.8) | Meetup's main value; cuts no-shows. |
| Capacity, waitlist, auto-promotion | **Core** (built, M1.5) | F10. |
| Guest registration for public events (no account) | **Core** (built, M1.4) | Visitors are the point of public events. |
| Hybrid capacity (separate remote limit) | **Core** (built, M1.8) | Pairs with F12 (remote flag). |
| RSVP open/close windows; +N guests (0 to 5) | **Core** for +N guests (built); **Later** for open/close windows (no milestone) | Cheap to build; Meetup parity. |
| Simple attendance (leader ticks a roll) | **Core** (planned, M6.3) | Churches track attendance; ChurchTrac does. |
| Volunteer and potluck sign-up sheets (one model: sheets, slots, claims) | **Core** (planned, M6.2) | ChurchTrac volunteer scheduling and forms. High value, small model. |
| ICS feeds and add-to-calendar | **Core** (built, M1.7) | F2. |
| Church directory with per-field privacy | **Later** (M6.1) | Wanted, but privacy-sensitive (10.2). |
| QR / self check-in | **Later** (M6.4) | Meetup roadmap item; builds on attendance. |
| Prayer requests (group or church-wide, optional anonymous, expiring) | **Later** (M6.5) | Very common church use; ChurchTrac form. |
| Group discussion board (posts and comments) | **Later** (M6.6) | Meetup discussions; replaces group-text sprawl. |
| Photo galleries (private by default) | **Later** (M6.9) | Needs private assets and minors' photo rules. |
| Households, household people (children, teens, and adults who share an email), guardian links, teen consent, Teen and Child badges | **Core** (built, M1.9, M1.10) | Teens 13 to 17 need consent, children under 13 exist only as household people (DEC-8), and shared emails need a household (DEC-23). |
| Pruning by leaders (no sign-up approval step) | **Core** (built, M1.5) | Leaders watch their own attendee lists and remove people, including teens (DEC-20, DEC-30). |
| Moderator role (between member and leader) | **Later** (note only) | Could prune attendee lists without editing the group. Do not build yet (DEC-17). |
| Guardian copies of messages to teens | **Core** (built, M3.5; setting off by default) | Many safe-church policies expect it; the church decides (DEC-19). |
| Background-check status flags on volunteer slots | **Later** (M6.8) | Child safety. Stores status only, never reports. |
| Child check-in kiosk with name tags | **Out of scope** (DEC-140) | High value for churches with nurseries, but needs hardware, printing, and security codes. |
| Contact proxy: one-to-one messages and relayed replies; no teen contact details for leaders | **Core** (contact privacy built, M1.11; one-to-one messages and relay planned, M3.11) | Safeguarding and privacy (DEC-31). |
| Two-way SMS inbox (shared, with history) | **Later** | Until then, replies relay to the author through Fold (7.8, M3.11). |
| Keyword auto-replies ("text CAMP for info") | **Later** | Cheap once inbound SMS exists. |
| Guest follow-up workflow (first-time visitor tasks) | **Later** | ChurchTrac automations-lite. |
| Custom registration questions | **Later** (M6.12) | Meetup RSVP questions (dietary needs, T-shirt size). |
| ChurchTrac CSV import | **Later** (M6.11: a column preset on the generic people import, M4.5) | Eases switching. ChurchTrac has no public API, so CSV is its only path. The preset needs a real sample ChurchTrac export, which nobody has yet; the generic import ships first without one (DEC-15, DEC-143). |
| Roster sync with church management software (ChMS) | **Out of scope** (cut, DEC-140; was M6.15) | Generic CSV import (M4.5) is the path. The research is kept in [`research/chms-sync.md`](../research/chms-sync.md) (DEC-40). |
| OIDC sign-in (Google/Microsoft) | **Later** | Nice; adds a dependency, so optional. |
| Admin TOTP | **Core** (planned, M4.9, v0.5) | Fold is on the public internet (final review, finding 7). |
| Durable, tamper-evident audit log | **Out of scope** (cut, DEC-140; was M6.14) | Legal and safeguarding record (DEC-33, 10.7). The regular `AuditLog` covers it. |
| PWA with web push | **Core** (built, M3.13) | Free alternative to SMS for churches that can't pay for texting; iOS needs Add to Home Screen (16.4+), so the directions page matters as much as the code. |
| Multi-campus (one church, several sites) | **Later** | Single-tenant still; rooms per campus. |
| Spanish or any other translation; an i18n framework | **Out of scope** | No plans for a non-English version. Plain English strings in the code are fine (DEC-13). |
| Embedded video calls inside Fold | **Out of scope** for now | Link-out and generated Jitsi links only. Revisit in a later revision if requested (DEC-16). |
| Paid event registration | **Out of scope** | Link out to the church's payment page instead. Same reasons as giving. |
| Online giving, contribution statements, fund accounting | **Out of scope** (DEC-140) | See 13.3. A "Give" link in the menu is fine. |
| Worship planning (songs, service order) | **Out of scope** | Planning Center and others do this well. Not social. |
| Website and app builder | **Out of scope** | Fold is the social layer; link from the church site. |
| Automated phone calls (phone tree) | **Out of scope** | Another provider and more TCPA rules. |
| Running background checks | **Out of scope** | Use MinistrySafe or a similar service; Fold stores only the status. |

### 13.3 Why giving and accounting stay out

- **Dependency**: giving needs a payment processor (Stripe or similar). That is a new proprietary dependency with its own onboarding, KYC, fees, and lock-in, which goes against D3 and D6.
- **Risk**: handling money means PCI scope, refunds, disputes, fraud, and payouts. A bug becomes a financial loss, not a UI glitch.
- **Paperwork**: churches need year-end contribution statements that meet IRS acknowledgment rules, plus fund accounting (restricted funds, budgets, bank reconciliation). That is a whole accounting product with legal stakes.
- **Already solved**: most churches already have a giving platform and a bookkeeper. Replacing them is a hard sell, while replacing the social features is the gap Grier named.
- **Scope**: it would roughly double the project and slow the core.

What Fold does instead: a configurable "Give" link in the menu and emails, and link-outs for paid events. Revisit only if many churches ask, and then as an optional adapter. The menu and email "Give" link is not built and has no milestone. The "Giving page link" admin setting that arrives with the SMS tab (M3.7, DEC-120) only feeds the donation-ask warning.
