# Decisions

The product rules for this project, and who decided each one.

- **Section 1, Ruled:** Grier decided these for this project. They win over everything else here.
- **Section 2, Proposed:** the starting design. Grier asked for it to be written down but has not
  ruled on each item. Build to it, and treat every item as his to change.
- **Section 3, Carried:** rules Grier decided on the predecessor project (Fold) that still hold.
  The Fold decision number is in brackets, so the full text can be read in Fold's
  `docs/plan/decisions.md` (see `docs/CARRYOVER.md` for where that is).
- **Section 4, Deferred:** not built until a church asks.
- **Section 5, Left behind:** Fold decisions that do not come along, and why.

A new decision is added to section 1 with the next R number, the date, and Grier's words where
there are any. Only product decisions get a number. A choice about how to implement one belongs in
the code or the pull request.

---

## 1. Ruled by Grier

| # | Decision | Date and words |
|---|---|---|
| R1 | **SQLite.** One database file, one writer. | 2026-11-26. "SQLite is what I started with… I love sqlite." History: Fold began on SQLite and dropped it on 2026-10-10 for "one test matrix and real Postgres features" (job claiming, range exclusion for rooms, case-insensitive email, JSON columns, partial unique indexes, advisory locks). Every item on that list either exists in SQLite or was needed only because several writers could run at once. Do not argue this project back to Postgres without a limit measured at a real church. |
| R2 | **Push notifications and SMS wait for the first church.** When a church is live, ask whether they want push, SMS or neither. | 2026-11-26. "Let's defer push and sms until the first church and we'll see if they want push or sms or neither." Keep one seam where a delivery channel plugs in, so either can be added without touching callers. |
| R3 | **Making a group or event more private removes nobody.** It changes who can newly see and join. The confirmation lists the people signed up who would not be able to see it now, with a Remove beside each. | 2026-11-26. Confirmed: "Confirmed that making something private removes nobody." He expects it may change later: "I expect it'll need changed eventually, but I'm willing to wait for that to be requested." |
| R4 | **One review pass, security first.** | 2026-11-26. "One review pass is good, but with the caveat that since this will live on the edge and have untrusted people signing up for events and thus adding things to text boxes, security is of primary concern." |
| R5 | **Working name: Fellowship Facilitator, FelFac. It becomes Fold again.** *Done: see R25.* Keep the name in one place until then. Fold's name, mark and violet come with it. | 2026-11-26. It "will ultimately become 'Fold' again, but I have to juggle some repositories." |
| R6 | **A tamper-evident audit log, from the start.** It is simple, it runs in the background, and nobody can view it unless it is requested for a forensic purpose. Requesting it sets off something serious. | 2026-11-26. "I don't want the tamper-evident audit log deferred. It can be simple, it just runs in the background and it can be non-viewable unless requested for a forensic purpose. There should be something serious happen if it's requested." Fold had cut this (DEC-33, DEC-140). How it works here, and what "serious" means, is proposal P17. |
| R7 | **The audit log works as P17 describes, and opening it works as P17 describes when the church has more than one admin.** *Amended by R30: the log keeps the text of communications.* The one-admin case was left open here and settled by R16. | 2026-11-26. "Your mechanism for the log is good, the something serious is good if there is more than one admin. If there's only one admin I have to think about who to tell, but this is a good start." |
| R8 | **Sign-in is by emailed link. A link lasts 24 hours. A session lasts a week and is renewed each time the person visits from the same browser.** | 2026-11-26. "The link should last 24 hours, the session should last a week, but be renewed when visiting the page again from the same browser." This settles the sign-in part of P3. A link is still single use, and asking for a new one cancels the old. |
| R9 | **Caddy sits in front as the proxy and handles HTTPS.** The app does not obtain its own certificate. | 2026-11-26. "Caddy as a proxy, it'll streamline the SSL setup with Let's Encrypt and it doesn't make us responsible for bleeding edge issues with HTTP handling." Decides P15 the other way: a church runs two containers. |
| R10 | **No component framework to start.** Own design tokens, one template partial per control, a gallery, and the browser gates (P18). | 2026-11-26. "The no-framework is where we'll start, we can always overhaul the UI later if need be." |
| R11 | **Migrations go forward only**, with an automatic copy of the database first (P9). | 2026-11-26. "Yes, forward-only migrations." |
| R12 | **The pilot church is Cloverdale Church of God in Boise, Idaho.** | 2026-11-26. It "will ultimately be the pilot." Its time zone is `America/Boise`. |
| R13 | **Budgets: 35 tables and 200 routes.** | 2026-11-26. "Raise the budget to 35 and 200. I would rather have more of these than try to work with less than we need." The other budgets in `AGENTS.md` are still proposals (P14). |
| R14 | **The Go module is named `fold`.** | 2026-11-26. "'fold' is fine for the module name, I'll shuffle the directories before I start so it's called 'fold'." |
| R15 | **Guests sign in by link, like everyone else.** A guest may do only what a guest may: see and manage their own RSVPs. | 2026-11-26. "Guests sign in by link, just like everyone else." Settles P4. |
| R16 | **Opening the audit log when the church has one admin: every group leader is emailed.** | 2026-11-26. "If there's one admin, email all the leaders of groups. Presumably this is a small church and the group leaders are likely leaders in the church in their own right." Settles the case R7 left open. |
| R17 | **The one-admin case in full.** The email goes to leaders of approved, active groups. There is no hold: the log is released once the notices have gone out. A lone admin with no other leader of any active group is refused, whether or not an office address is set. | 2026-11-26. "Approved active groups." "No hold, the log is released after the notifications go out." "If the admin is the only one active he's probably just messing around, but this is fine so he doesn't get used to being able to pull up the audit log." And: "A lone admin with no other leaders of any groups at all should just be refused to the audit log. Likely he's the one that answers the office email, so it's functionally the same." |
| R18 | **Grier is present for the pilot install.** He hopes a volunteer from the church does the hands-on work with him there; that person is not chosen yet. | 2026-11-26. "I will do the install at cloverdale as part of them being the first church, or at least I will be present with an as-yet-undecided person that will facilitate the install. My hope is they will do the hands on, but I don't have a 'they' yet." The install guide is therefore written for a volunteer to follow, and the pilot install is its first real test. |
| R19 | **The audit log records what people do, not upkeep.** Its purpose is discovering or proving serious wrongdoing: inappropriate communication with a minor, or disruptive or malicious use of the service. Session renewal and similar bookkeeping are not written to it. | 2026-11-27. "Session renewal doesn't need to be in the audit log. The audit logs purpose is for discovering or proving serious malfeasance like inappropriate communications with a minor or disruptive/malicious interactions with the service itself. So the focus should be on user behaviors." In the code, such a command is marked `write.Upkeep`. |
| R20 | **System rows that explain people's actions or protect the log belong in it.** Backups taken, restores, and keys re-sealed are logged; plain upkeep is not (R19). | 2026-11-27. "Yes, system behaviors that support understanding human behaviors or the integrity of the audit log are good and proper to log." |
| R21 | **The server's secret is `SECRET_KEY` in the church's `.env`**, made with `openssl rand -hex 32` during install, outside the data volume, so a copied backup does not carry the key to its mail settings. | 2026-11-27. "Secret key in .env is good." |
| R22 | **Setup connects to whatever mail server the person setting it up types in.** That is how setup works, and it is the one place the server reaches an address a person supplied. | 2026-11-27. "The setup *should* connect to whatever mail server the person setting it up types in, that's how the setup works." |
| R23 | **The send-from address is church configuration, not personal data.** The default is the church's, never an individual's. Mail sent by a particular person may later get its own from address. | 2026-11-27. "The sent-from address is a church configuration item, yes. We may want to allow communications that have different sent-from based on who is sending it, but the default should certainly be specific to the church and not an individual." |
| R24 | **Slice 0's implementation calls stand:** job bookkeeping writes no audit row; sign-in requests wait in an in-memory queue (for now); the component gallery is served in production to admins; link tokens are random and exist only sealed in the job that emails them. | 2026-11-27. "That's fine." "Fine for now." "Fine." "Fine." |
| R25 | **The product is called Fold.** The working name is retired everywhere. Pages and emails lead with the church's name and carry "Powered by Fold" at the foot. | 2026-11-27. "'fellowship facilitator' shows up in a few places like the footer, that needs to go, that was just a temporary name while I figured out what to do with the fold repositories. It needs to be replace with 'Fold' or 'Powered by Fold' or a similar branding." |
| R26 | **An emailed sign-in or setup link is one click in the browser that asked for it.** Asking for a link marks that browser for the address for 24 hours; opening the link there signs in at once. Anywhere else (another device, a mail scanner) it shows the page with a button, so a scanner cannot use it up. | 2026-11-27. "I'm clicking two things to sign it, it should really just be one thing (the link in the email)." Then, on the same-device option: "Ok, yes, build one click on the same device, that is a good option. That's a good compromise." |
| R27 | **A declined account request is deleted.** Nothing about the person is kept; if they ask again, admins see a new request, and rate limits stop floods. | 2026-11-27. Chose "Delete them" over remembering the address or keeping a declined row. |
| R28 | **Nobody hides from name search, and the middle visibility is called "Church only".** Only signed-in leaders and admins search names, and results show a name only. The three visibilities are Public (anyone), Church only (signed-in members) and Invite only. Settles the two questions in HAZARDS section 12 about search and the word "private". | 2026-11-27. Chose "No hiding; call it 'Church only'". |
| R29 | **Slice 1 includes an admin deleting a group, with its members told (DEC-177).** Members inviting a friend (DEC-4) waits until the pilot church asks; a person's time zone (DEC-58) arrives with event times in slice 2. | 2026-11-27. Chose "Admin deletes a group" alone from the three. |
| R30 | **The audit log keeps the text of what one person sends another through Fold**: announcements, messages, and notes such as a group decision's "Other" reason, with the sender's and recipients' ids. Its first purpose is proving inappropriate contact with a minor when Fold was used for it; then proving who did what between adults; then finding and measuring attacks on the site. Erasing a person blanks their profile but never removes what they sent from the sealed log, which is opened only by the announced request (R6). Upkeep (R19), addresses and profile data stay out. Amends P17 and R7, which kept all free text out of the log. | 2026-11-27. "#1 is the most legally relevant. So decisions about the audit log should facilitate it." On the cost, that anything written in a message becomes permanent: "I absolutely understand the cost and I'm willing to accept it, the only other real option is to exclude minors and that's unacceptable because that's what every other platform does and I already have buy-in from two youth pastors that want *this*." The three uses, in his words: "1. A leader in the church (group or admin) has inappropriate contact with a minor. We must be able to provide support that this happened if Fold was used to facilitate it. If we delete a message that would have been a smoking gun, we're complacent in abuse. 2. Two adults are behaving inappropriately. This is still bad, but not usually criminal. It is preferred to have proof of who did what. 3. when someone does malicious things to the site. [...] we mostly want to know that someone did something and how much they did so they can be banned and the exploits fixed." |
| R31 | **A group decision gives a reason from a fixed list, or "Other" with a short message.** The message is about the group, not a person, so it is not treated as personal data. It is shown to the group's leaders and kept in the audit log (R30). | 2026-11-27. "What about offering fixed reasons and an 'other' that allows for a free text response? I think we'd want the text logged in the audit log specifically in case it provides proof of abuse or improper interactions. [...] Otherwise the communication *should* be about a group and not a person and does not need to be treated as identifiable information." Settles DEC-96 against P11 for group decisions. |
| R32 | **Adding someone to a group is an invitation they accept.** A leader who finds a person by name invites them; the person gets a notice with Join and No thanks, and nothing changes until they join, so their email shows on the roster only after they chose the group. An invitation by email works the same way: no account or membership exists until the person opens the link, says they are 18 or older, and accepts; the inviter's right is checked again then. Amends P6, under which an added adult was told and could leave. **Admins may still add someone directly**, for a person who has trouble with the site, doesn't follow invitations, or is away from a computer but needs to be counted; that person is told and can leave. | 2026-11-27. Chose "Adding is an invitation they accept" after a security review showed a leader could add any adult and see their email at once. Then: "Admins should be able to forcibly add people to groups though. For instance if they're having trouble with the site or don't understand invitations or are away from a computer but need to be counted as an attendee." |
| R33 | **A repeating event is weekly, every other week, or monthly on the Nth weekday** ("the 2nd Tuesday"), or happens once. | 2026-11-27. Chose "Weekly, biweekly, monthly" over weekly only, and over also picking several weekdays. |
| R34 | **A time that a daylight-saving change skips moves to the next real time for that date, and a time that happens twice uses the first.** The event form warns once when the chosen time can fall on such a night. Settles the first open question in `docs/HAZARDS.md` section 12. | 2026-11-27. Chose "Move it and say so" over refusing the time. |
| R35 | **A guest's RSVP counts when they open the emailed link** (one click in the browser that asked, a button anywhere else, as R26). Until then no person or answer exists, only the sealed link, which is erased when used or expired; so fake addresses cannot fill an event and no address is kept without the person acting. | 2026-11-27. Chose "When they open the email" over holding the seat for 24 hours. |
| R36 | **Until email choices exist (slice 3), the only event email is urgent**: being moved up off a waitlist, and a date the person said Going or Maybe to being cancelled or its event deleted. Every event notice still goes to the bell. | 2026-11-27. Chose "Urgent ones only" over bell only and over emailing everything. |
| R37 | **Slice 2's defaults stand**: changing the church's time zone moves no existing event; finding public events of other groups is the calendar's Everything view within 30 days and signed-out Home's "Browse public events" (DEC-76 stays open beyond that); monthly repeats offer the 1st to 4th weekday and the last; no plus-ones before households; places are text with a plain "Open in maps" link; no event pictures in slice 2. Settles two open questions in `docs/HAZARDS.md` section 12. | 2026-11-27. "Please proceed with Slice 2, your defaults are good." |
| R38 | **Dates are written the American way**: "Tue, Dec 8", "Tuesday, December 8, 2026". | 2026-11-27. "'Tue, Dec 8' is good." |
| R39 | **"Open in maps" is a Google Maps search** of the place's text, built as a plain link; the server fetches nothing. | 2026-11-27. "Google Maps is good." |
| R40 | **Answers do not follow a date that moves to another day.** When an edit moves a one-off event or a series' weekday, the old dates are cancelled and new ones made, and people signed up must answer the new date themselves. | 2026-11-27. "RSVPs should not follow, they'll need to confirm the new date." |
| R41 | **An event's video link is shown only to people who answered Going or Maybe, and to the people running the event.** Strangers who can read a public event's page do not get the link. It arrives with answers, in slice 2's milestone 2. | 2026-11-27. Chose "Those who said Going/Maybe" over anyone who can see the event and over a per-event choice. |

Standing yardsticks, in Grier's words from the Fold work. They apply to every design choice.

- **Scale.** "A church with 500 people and 6 staff members would be considered VERY LARGE for this
  software. Mega churches can vibe code their own version of this and/or buy from a SaaS vendor."
- **Least surprise.** "Any time we don't save and require someone to try again we need to make
  sure they're not confused as to what happened."
- **Same behaviour for like things.** "All conflicts should behave the same when it comes to
  closing editors."
- **No proof management.** "Outside of the audit log, there is no need for any sort of proof
  management for your average church administrator." Say "updated", never "now shows your changes".
- **Never close and reopen.** "I don't want any situations where a user is told to close and
  reopen something. If the UI no longer allows something to happen, it should close on its own."
- **Simple conflict avoidance.** "Make good faith, but simple, efforts for conflict avoidance."
- **Trust on workflow, never on security.** Once someone has an approved account they use their
  own judgement about what to join, and leaders remove people who should not be there. No approval
  gates on sign-ups or joins. Every security control stays and is hardened. (Fold DEC-30, DEC-34)

---

## 2. Proposed, not yet ruled

| # | Proposal | What it changes from Fold |
|---|---|---|
| P1 | Go, one static binary, standard router, SQL without an ORM. | Fold used Gin and GORM. |
| P2 | Server-rendered pages with small fragment updates. No JSON API, no separate client. | Fold was a React application over a JSON API. |
| P3 | **Sign in by emailed link.** No passwords. One mechanism covers invitation, sign-in, guest RSVP, email verification and "manage my RSVP". Sessions last long so a new link is rarely needed. Admins also need a second factor. Changing an email address starts with a fresh sign-in link sent to the current address (in place of typing the current password), is confirmed from the new address, and can be cancelled from the old one. | Fold used passwords of 10 to 72 characters with reset, lockout and set-password flows. **Sign-in by link and its lifetimes are ruled (R8).** Guests are ruled too (R15). Still proposed: the second factor for admins and the email-change steps. |
| P4 | **A guest signs in by link** and may do only what a guest may: see and manage their own RSVPs. A guest is a person an admin has not approved as a member. | Fold's rule was that a guest is never loaded as an actor. **Ruled: R15.** |
| P5 | **Contact details, no workflow.** Leaders never see a teen's email or phone and reach teens only through the app. An adult chooses whether leaders of their groups see their details (shown unless they hide them). There is no request, grant or reveal. | Fold had contact requests, grants and an audited reveal. |
| P6 | **Adding people.** *Amended by R32: adding is an invitation the person accepts.* A leader finds a person by name and adds them. An adult is told and can leave. Adding a teen needs the teen and an admin to agree, recorded as one approval. | Fold routed "Ask to add" through the contact-request flow. |
| P7 | **One date can be opened wider than its series**, never made narrower than it. | Fold allowed a per-date override in both directions. |
| P8 | **Inviting one person to one date is a link.** Once they answer, their registration is their access to that date and nothing else. | Fold kept a separate table of exact-date access. |
| P9 | **Migrations go forward only**, and the binary copies the database before it migrates. Rolling back is the old image plus that copy. | Fold used expand-then-contract across releases and numbered recovery levels. **Ruled: R11.** |
| P10 | **Erasure by structure.** Personal data lives in `people` and tagged columns. Every person has a random identifier that is never reused, separate from the row number. Deleting a person blanks their row to a tombstone, deletes their files and tagged text, and adds that identifier to an erased list kept beside the database and outside every backup. On start, the app erases anyone on the list, so the built-in restore cannot bring a person back. A restore done by hand from a whole-disk snapshot rolls the list back too; the delete confirmation and the restore guide say so. | Fold classified every column, reviewed free text by hand, and fenced restores. |
| P11 | **No free text about another person in a workflow.** Requests and decisions offer fixed choices. | Fold collected reasons as text, which then needed review at erasure. |
| P12 | **Notices store a kind and ids** and are worded when read. | Fold stored the finished text. |
| P13 | **Announcements only.** No group discussion board. Replies to an announcement go to the address the church sets. | Fold planned announcements versus discussion. |
| P14 | **The budgets** in `AGENTS.md`: lines of code, test time, install time. | New. Tables and routes are ruled (R13). |
| P15 | *Withdrawn.* The binary obtaining its own HTTPS certificate. | Grier chose Caddy as the proxy (R9), as Fold had. |
| P16 | **Save conflicts.** A form carries the version of the row it was opened with. If the row has changed, nothing is saved, the form is shown again with the current values, and one line says someone else changed it. | Same behaviour Grier chose for Fold; the mechanism is smaller. |
| P17 | **How the audit log works (under R6).** *Writing:* the write executor adds one row for every change, in the same transaction, so nothing can be changed without a row. A row holds a sequence number, the time, the kind of action, the actor's and subjects' random identifiers, and a few structured details. It never holds a name, an address or free text, so erasing a person never touches it; an erased person appears as "a deleted person". *Sealing:* each row stores a hash of itself and the row before it. The table refuses updates and deletes, and nothing purges it. *Proof from outside:* a chain inside one file proves nothing to someone who can rewrite the file, so the latest sequence number and hash (a checkpoint) are written into every backup and included in a short monthly email to every admin. Any older email or backup then exposes a rewrite. A daily job checks the chain and tells every admin if it is broken. *No viewing:* no page and no route reads the log. Pages that show history (who approved an account, who a message went to) read their own tables. *Opening it:* an admin signed in with their second factor asks for it, choosing a purpose from a fixed list (a safeguarding concern, a legal request, a security incident) and a scope (one person, a range of dates, everything). At once, every admin and the church's office address is emailed who asked, when and why; the request is itself written to the log; and admin pages carry a notice for 30 days. A second admin must approve before anything is released. *One admin (R16, R17):* the same request, with the same email to the church's office address, and the email also goes to every leader of an approved, active group. There is no second admin to approve and no hold: the log is released once the notices have gone out. If no one else leads an active group, the request is refused, even when an office address is set (the lone admin probably reads that inbox too), and the page says to add a second admin. What is released is one export file with a report on whether the chain is intact, downloadable once within 24 hours, and the download is logged. *Limit, stated to the church:* a person with the server itself can read or destroy the file. The log makes tampering evident; it cannot make it impossible. | New. Fold's regular audit log was viewable by admins and its sealed version was only a sketch. **Ruled by R7, R16 and R17.** |
| P18 | **How pages stay consistent.** No component framework. The look is Fold's design tokens and rules, carried over (`docs/CARRYOVER.md`). Every control is one template partial (button, field, chip, menu, dialog, empty state, segment control), built on native HTML where the browser already does the job (`dialog`, popover, `details`, form validation). A gallery page shows every partial in every state, and the browser checks screenshot it at phone and desktop widths beside the accessibility, layout and contrast gates. A check refuses a raw control written outside a partial. Developer agents use the browser vendors' `modern-web-guidance` skills as tooling; nothing from it ships. | Fold had the same idea as React components. If a ready-made look is wanted instead, daisyUI on the Tailwind standalone binary is the one that fits server-rendered pages without Node. **Ruled: R10.** |

---

## 3. Carried from Fold

### The project

- MIT licence. Given away. Hosting for others is case by case and not for profit. (DEC-10, DEC-11)
- One installation per church; no shared database and no billing. (DEC-139)
- The church brings and owns its server, domain and provider accounts. (DEC-2)
- A VPS with one compose file is the only recommended way to run it. (DEC-26, DEC-142)
- Build what is needed in the app; add an outside service only when it keeps installs simple or a
  church asks. (DEC-43)
- One adapter per kind of provider, plus a dev outbox for tests. A second only when a church asks.
  Postmark is the default mail service, with plain SMTP as the fallback. (DEC-21, DEC-140)
- No Python in anything a church runs. Developer tooling may use it. (DEC-151)
- English only; no translation framework. (DEC-13)
- Legal pages are templates the church owns. (DEC-14)
- A check only a person can run on real phones or live services does not block a merge. Build it
  best effort and list it in `docs/FIRST-CHURCH-CHECKS.md`. (DEC-128)

### People and accounts

- The email address is the login and is unique. A shared address is one account. (DEC-23)
- A person who signs themselves up, or is invited by an ordinary member, waits for an admin's
  approval. Invitations from leaders and admins skip it. There is no open signup. (DEC-4)
- Admins see everything. (DEC-5)
- Nobody under 13 has an account. Younger children exist only as people in a household. (DEC-8)
- A teen (13 to 17) has an account only with consent from a guardian and an admin. A leader can
  never give that consent. (DEC-8)
- A teen waiting for consent cannot sign in. They can still browse what the public can. The form
  that sends a sign-in link answers as it does for every address; the explanation that a
  guardian's consent is needed arrives in the email. (DEC-85, restated for P3)
- Teens cannot lead groups or host events. (DEC-18)
- Teens may sign up for anything they can see. Teens and children are badged on rosters, attendee
  lists and recipient lists, for the people who run the event. (DEC-20, DEC-82)
- Linking a teen or child to an adult always needs an admin. An admin cannot approve a link where
  they are the guardian, unless they are the only admin, and then the audit row says so.
  (DEC-32, DEC-79)
- Removing or disabling the only leader of a group requires choosing a successor first. (DEC-36)
- A disabled account keeps its memberships and sign-ups, hidden and not counted. Re-enabling
  restores each one where there is room; where there is not, it is dropped and the person is told
  to ask a leader. (DEC-38, DEC-47)
- Deleting an account erases the person. (DEC-144)
- Ending a session by logging out ends every session of that account. (Fold M7.2)
- Each person has a time zone for their emails, taken from their browser the first time. (DEC-58)

### Groups

- Three visibilities. **Public:** anyone. **Church only** (Fold's "Private", renamed by R28): signed-in members of the church.
  **Invite-only:** hidden from everyone but its members, the people it invited, and admins.
- A new group waits for an admin's approval. Its leaders hear the answer, with an optional message
  from the admin. (DEC-96)
- A group moves from waiting to active or archived, and between active and archived. It never goes
  back to waiting. Archiving and unarchiving are audited. (DEC-160)
- Leading a group that is waiting or archived grants nothing beyond it: no invitations, no adding
  people, no new events, no room bookings. (DEC-88, DEC-147)
- Leadership is a role on a membership. A group can have several leaders.
- Leaders remove members from their own groups, but never an admin or the last leader. A removed
  person cannot rejoin on their own. (DEC-35)
- Leaving is not removal: a person who left may join again. (DEC-86)
- Leaving or being removed takes the person off that group's upcoming dates. The confirmation
  gives the count first. (DEC-74)
- Admins are labelled on rosters. (DEC-38)
- Leaders find people by name, not email. Results show a name and a Teen badge, never contact
  details. (DEC-78)
- Deleting a group tells its members and the people signed up. (DEC-177)

### Events and dates

- An event stores its local start time and IANA zone. Dates are generated ahead and stored.
- An event repeats at most once a day, and a rule that would make more than 2,000 dates is
  refused. (DEC-89)
- A church-wide event has no group. Admins create it and name its hosts, who get a leader's rights
  over that event only. (DEC-7)
- An event stores its own visibility, copied from its group when created. Making a group more open
  never changes its events. (DEC-48)
- A leader may set their event to any visibility. Making one more open is audited and the other
  leaders and admins are told; a reason is optional. An opened event never shows its invite-only
  group's name, place or members. (DEC-6, DEC-17, DEC-28)
- Visibility is chosen when the event is created and edited in the event's own editor. (DEC-51)
- New events start in the church's time zone. (DEC-50)
- Times show in the viewer's zone. When the event's own clock differs, the page adds one quiet
  line naming it. Edit forms use the event's zone and name it. (DEC-57)
- Event forms ask title, description, then When, then place, then more options. Seats can be set
  at creation. (DEC-95)
- A series is edited on the event page only. (DEC-161)
- A date dropped by a series edit is cancelled, and comes back with its sign-ups if a later edit
  includes it again; the people signed up are told. A manager can bring back a future date that was
  cancelled by hand. (DEC-91)
- Deleting an event tells the people signed up. Delete and cancel confirmations show how many
  people that is; the number is shown, not enforced. (DEC-175)
- Pictures are uploaded, never linked by URL. (DEC-49)
- Video is a link out. Nothing is embedded. (DEC-16)

### RSVP

- Answers are Going, Maybe and Can't go. Cancelled is its own state. (DEC-54, DEC-63)
- Only the people running an event see who can't go. (DEC-64)
- Attendance shows counts first, then names only to people allowed to see them. (DEC-63)
- Belonging to a group is a standing invitation to its events. A church-wide event invites every
  member. A public event is not an invitation until the person answers. (DEC-72, DEC-73)
- Dates of a church-wide or public event that the person has not answered show only within the
  next 30 days. (DEC-77)
- On a series date, answering asks: just this date, this and following, or all future dates. Dates
  added later are not answered for the person, and the dialog says so. (DEC-71, DEC-92)
- After an answer, a status line that stays on the page says what happened. (DEC-92)
- Seats and a waitlist are optional. When a seat frees up, the next waiting party that fits moves
  up, whatever freed it. (DEC-84)
- Guests register as Going only. A guest's link to manage a date stops working when the date has
  ended. (DEC-55, DEC-56)
- The calendar has Agenda (the default), Week and Month. In the person's own view Going is quietly
  tinted, Maybe and unanswered are outlined, and Can't go is left out. (DEC-67, DEC-71)
- Calendar feeds mark Going as confirmed and Maybe as tentative, and leave out Can't go. (DEC-71)
- Any date a person may see offers "Add to calendar" and "Share". (DEC-93)

### Households and children (slice 5)

- An adult with no birth date on file cannot be registered as a household person; a "child" who is
  18 or older, or a "teen" under 13, cannot be registered until the household fixes the record.
  Ages use the church's local date. (DEC-81)
- The people running an event see a child's care notes only while that child is Going, within 24
  hours of the date. (DEC-81)
- Childcare has its own count and limit. A waiting party that needs more childcare than is left is
  skipped, keeping its place, and the next party that fits moves up. (DEC-80)
- At 13 the guardians get one notice to review the record. At 18 a teen becomes an adult; consent
  history is kept. (DEC-83)

### Notices, announcements and email

- Notices are stored per person, written in the same transaction as the change, with read state on
  the server. (DEC-42)
- The bell shows one plain count. Reminders never go to the bell. No per-group settings. Email goes
  right away; no digests. (DEC-44)
- Read notices are deleted after 90 days and unread ones after 180. (DEC-45)
- A notice never contains an email address or a phone number.
- Leaders write to their own groups, hosts to their own events, admins to anyone.
- The composer shows who a message will reach before it is sent. A scheduled message goes to the
  list as it stands when it is sent. (Grier, 2026-11-24)
- An announcement is From "*author* via *church*". Replies go to an address the church sets;
  personal addresses never appear. (DEC-130)
- Email settings have a switch per kind (group updates, event updates, messages from leaders,
  admin notices) and "turn off all optional email". Account and safety email is always on.
  (DEC-97, DEC-115, DEC-119)
- The inbox's one-click unsubscribe turns off the kind that email came from. (DEC-114)
- A hard bounce stops all mail to that address until an admin clears it. A spam complaint stops
  optional mail only, so nobody is locked out. Admins see both on the person. (DEC-112, DEC-113)
- Optional email carries the church's mailing address in its footer. (DEC-116)
- A send that failed can be tried again for just the people it failed for. (DEC-131)
- A leader picks an event's reminder: off, 2 hours, 1 day (the default), 2 days or 1 week before.
  People on the waitlist get a "still waiting" note at the same time. (DEC-133)

### Rooms (slice 6)

- Rooms are optional for a church.
- A room carries default setup and teardown minutes, copied onto each booking. Conflicts compare
  the padded times. (DEC-101)
- A booking is confirmed if the room is free. A church may require an admin's approval instead,
  off by default. (DEC-102)
- Booking a series books the free dates and marks the busy ones as needing a room. The picker
  warns first. (DEC-103)
- A booking request is one admin task, whether for one date or a series. (DEC-106)
- "Restricted" limits who may book a room, not who may know where an event is. (DEC-104)
- To anyone who may not see an event, its booking shows as "Reserved".
- Rooms have no colours. The by-room week is a grid of rooms by days. (DEC-107, DEC-108)
- Room suggestions arrive with the picker's list of rooms, never from a separate request. (DEC-110)

### Pages and wording

- An error a person cannot act on, or that would reveal something, never reaches them. Rate limits
  and "does this account exist" answer with the form's usual success message and are logged. A
  link that is invalid or expired says so, because the person can ask for a new one. Sign-in by
  link has no lockout message to show, so no public form answers with a "too many requests" error;
  the only one that may is a signed-in leader's daily invitation limit. (DEC-52, restated for P3)
- Every error a person can act on has its own plain wording, written in one place. (DEC-61)
- Colours come from role tokens (primary, surface, danger and so on), chosen by meaning. Status is
  never colour alone. (DEC-60)
- One primary action per view; the rest go under "More". Destruction is solid red only when it is
  the point of the dialog. (DEC-63)
- Every field has a visible label above it, a hint before the control, and its error beneath. A
  failed submission keeps what was valid. (DEC-65)
- No page is a dead end or a blank wait. An empty list says what would appear and offers the next
  step, and is never red. (DEC-66)
- A single on/off setting saves when changed and says "Saved". Text fields keep a Save button, and
  leaving with unsaved edits asks first. (DEC-98)
- Phones get a bottom tab bar; wider screens get the same links in the header. (DEC-62)
- Signed-in Home greets the person and leads with what is coming up for them. Group lists sort by
  next meeting. (DEC-94)

---

## 4. Deferred until a church asks

Push notifications and SMS (R2). Guardian copies of messages to teens. Turning a household person
into their own account. A member limit on groups. Generated video-meeting links. Sign-up sheets
(first after the pilot goes live). A group "pulse". Relaying replies through the app. Images in
announcements. Email digests. A second mail adapter, S3 storage, managed-platform installs, sync
with church management software. A write-once offsite copy of the audit log (R6 builds the log
itself now).

Each returns as its own slice, with Grier's go-ahead, when a named church wants it.

---

## 5. Left behind

| Fold decision | Why it does not come along |
|---|---|
| PostgreSQL only (DEC-1, DEC-24) | R1. |
| React application and JSON API | P2. |
| Passwords and their rules (DEC-90, DEC-53, parts of DEC-87) | P3. An email change is still confirmed from the new address, and the old address still gets a link to cancel it. |
| Contact requests, grants and reveal (DEC-31's workflow) | P5. The protection of teens' details stays. |
| Per-date visibility that narrows, consent tokens, dates "kept" after leaving (DEC-46 in part, DEC-156 to DEC-158) | R3 and P7. |
| Cancelling sign-ups when a group is made more private (DEC-48 in part) | R3. |
| Staged migrations, recovery levels, start fences (DEC-138 in part, DEC-158) | P9. |
| Column-by-column erasure inventory, reason review, restore fence (DEC-144's mechanism, DEC-148) | P10 and P11. The promise to erase stays. |
| Revision tokens, refused-save recovery in the client (DEC-162 to DEC-177's mechanism) | P16. The behaviour stays. |
| Push decisions (DEC-121 to DEC-127, DEC-134 to DEC-136) | R2. Read them again if push returns. |
| New surface permitted before a church is live (DEC-153) | Choice 4 in `AGENTS.md`. |
| Five separate review concerns on every head | R4. |
| The tamper-evident audit log cut from the roadmap (DEC-33, DEC-140) | R6 builds it from the first slice. |
