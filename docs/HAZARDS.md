# Hazards

Mistakes the predecessor project (Fold) made, or caught in review, in seven weeks and about 600
pull requests and issues. Read the section for the area you are touching before you write code.

Each line is a rule and, in brackets, where Fold learned it (an issue number, an audit finding
such as R5, or a decision). Sections 1 to 8 still apply here. Section 9 is new with this design.
Section 10 lists problems this design removes, so nobody rebuilds them. Section 11 is how the work
itself went wrong. Section 12 is open questions nobody has decided.

---

## 1. Time

- **Never expand a repeating event in UTC.** A weekly 7:00 pm meeting drifts by an hour when
  daylight saving changes, and can land on the wrong day. Expand from the local start time in the
  event's zone. (Fold C2)
- **Store three kinds of time, deliberately.** SQLite has no date type. An instant is integer
  seconds in UTC. A wall-clock time is text, `YYYY-MM-DDTHH:MM`, beside an IANA zone. A calendar
  date is text, `YYYY-MM-DD`. Never mix them in one column. (audit R9: Fold's text dates had to be
  squashed out)
- **A series never ends on its own.** Generate dates to a horizon and extend daily. Refuse rules
  that repeat more than daily or would make thousands of dates. (DEC-89)
- **Ages count from the church's local date**, and a transaction reads the clock once.
- **Refuse the zone name "Local".** Offer every zone in setup, not a short list. (audit, setup page)
- **Format dates in one place**, in the viewer's zone, and name the zone when it differs from the
  event's.
- **Tests never depend on the wall clock or a short deadline.** Fold filed more than a dozen flaky
  tests that raced a timer (#477, #495, #536, #538 to #540, #603). Inject the clock.

## 2. Showing what should be hidden

The rule is simple and was broken many times: **every surface asks policy, and a hidden thing
answers exactly like a missing thing.**

- Filter a list of dates by what the viewer may see **before** limiting or paging it.
- An event opened to the public from an invite-only group must not show the group's name, place,
  id, leaders or members anywhere: page, calendar, feed, notice or email.
- The group directory's "next meeting" revealed the times of private events to signed-out
  visitors. Derived fields leak too. (#359)
- A public calendar feed copied the series description and video link onto an opened date without
  checking the series. (#167)
- A room's feed showed titles of events the viewer could not see. Show "Reserved". (#357, R5)
- A guest's link kept showing an event's details after the event was made private. (#166)
- A message filled `{{event_title}}` from the live event with no visibility check. (#262)
- A delayed or scheduled send must check visibility again when it is delivered. (#350)
- "Try again" on a failed message re-sent to people who had since left. (#261)
- People search revealed which groups a person belongs to. (R5)
- Only the people running an event see who declined; everyone else gets no declined data at all,
  not a hidden element. (DEC-64)
- A page left open keeps showing an event after the viewer lost access, until its next read. Any
  refused action makes the page read again. (#460)

## 3. Revealing whether an account exists

- Public forms (sign-up, guest RSVP, sign-in link request) give the same answer, at about the same
  cost, for every address. The email carries the hint. (DEC-52)
- **Timing is an answer.** Do the same work whether or not the address exists. (#93, #156, #168)
- **An error is an answer.** A NUL byte in a name produced a 500 for one case and a 200 for the
  other. Validate input before any lookup. (#259)
- **Signed-in members are also askers.** Email change, invitations and "add by email" each let a
  member test addresses. Give one acknowledgement and rate-limit them. (#157, #474, DEC-172)

## 4. Limits and exhaustion

- **Check the budget before the expensive work**, not after. Fold's reset limit did not stop the
  password hashing it was meant to protect. (#169)
- **Limit mail per address, not only per sender or IP.** One person could flood another's inbox
  with account or verification mail. Add a daily cap as well as an hourly one. (#91, #160, #263)
- **Count an IPv6 client by its /64**, not its full address. (#159)
- **Check a limit and spend it in one atomic step.** Parallel requests slipped past a check-then-
  act lockout and a daily message limit. (#90, #260)
- **Bound image work.** One small crafted PNG cost about 380 MB to decode. Cap pixel dimensions
  before decoding, run a fixed number at once, and limit uploads per person. (#356, #414)
- **Every request has a body size limit, and the server has read, write and idle timeouts.**
  (pull request #249)
- **Every text field has a maximum length**, checked on the server.

## 5. Sign-in, sessions and authority

- **First-run setup can be stolen.** A fresh public install let any visitor create the first
  admin. Print a one-time setup code to the log, and create the first admin in one transaction
  that checks there is still none. (#240)
- **Load the actor inside the write.** Fold checked who you were, then read the request, then
  wrote, and an account disabled in between kept working. This was found five separate times. The
  write executor loads the actor fresh, so no handler can forget. (#164, #360, #412, #413, #476)
- **Authority given earlier can outlive its giver.** An invitation still granted membership after
  the inviter stopped being a leader. Check the inviter's rights when the invitation is used. (#155)
- **An edit endpoint must not be a back door around a state rule.** An admin's generic "edit user"
  could activate a teen waiting for consent or make a teen an admin; a group edit could un-archive
  a group. State changes go through their own commands. (#94, #165, #428)
- **Logging out ends every session of the account**, so a copied cookie dies. The cookie lifetime
  in the code must match the one in the docs. (R5, #158)
- **Secrets in URLs must not reach the access log.** Redact token paths. (#89)
- **Changing an email address needs proof from the current one.** Fold once let it change with no
  proof, and its link survived a credential change. Here the proof is a fresh link to the current
  address, and any change of address kills every outstanding link for the account. (#154)
- **Restoring a backup brings back old sessions and old links.** After a restore, end every session
  and expire every outstanding link. (#475, #511)
- **Only trust `X-Forwarded-For` from Caddy.** The app sits behind it (R9), on a private network
  with a fixed address range. Trust the header from that range only, and refuse to start if the
  trusted range is "everything". Publish only Caddy's ports; the app has none on the host.
- **One secret must not key everything.** Derive a separate key per purpose so one can be rotated
  without breaking the others, and write the rotation steps down. (R12)

## 6. Text that strangers type

- User text is escaped by the template, always. Never build HTML by joining strings.
- Rich text passes one allow-list sanitizer before it is stored and is the only user content marked
  safe. The list is a few formatting tags, links and images. It must never admit `script`, `style`,
  event attributes, `data-` attributes or the attributes the fragment-update script reads. Keep a
  test that proves it.
- **A spreadsheet export can run formulas.** A cell beginning with `=`, `+`, `-` or `@` must be
  neutralised. (Fold handled this in its contact-history export)
- **Names and subjects go into email headers.** Strip line breaks so nobody can add headers.
- **A notice or an admin's message to a person must not carry an email address or phone number.**
  (DEC-96)
- **Uploaded photos carry GPS coordinates.** Re-encode every upload so metadata is gone. (R5)
- **A file's name and type are claims.** Sniff the bytes, accept images only, and store under a
  name the server generated.
- **An emptied field means "clear it".** Fold read a cleared seat limit as "not sent" and kept the
  old value. Test clearing every optional field. (#445)

## 7. Seats, lists and notices

- **Anything that frees a seat promotes the waitlist**: a cancel, a decline, a removal by a leader,
  a smaller party, a disabled account, a raised limit. Fold missed two of these. Put promotion in
  one place that every seat change passes through. (#147, #179)
- **Leaving must not rewrite history.** Leaving a group recorded every past date as "Can't go", and
  blocked rejoining as if the person had been removed. (#144, #145)
- **Tell people when something they signed up for goes away.** Deleting an event or a group told
  nobody. (DEC-175, DEC-177)
- **A confirmation that states a number says exactly what the number counts**, and its wording when
  the count cannot be read must still be true.
- **Do not chain two writes behind one confirmation.** Ask about each stored change.
- **Send in batches.** One long write blocks every other write. Insert notices in bulk and deliver
  mail in small jobs. (#257)
- **Email sending must require TLS.** Optional STARTTLS let mail go out in plain text. (#161)

## 8. Installing and running

- **An install that cannot send mail is not installed.** Fold's compose file once dropped the mail
  settings, so a "working" site could invite nobody. Setup does not finish until a test message
  arrives. (#146, audit R1)
- **Never build on the church's server.** Compiling ran a 1 GB machine out of memory. Ship an
  image. (audit R1)
- **Documents that record status go stale within days.** Fold's status page said no release existed
  after two had shipped. Keep one status table and update it in the pull request that changes it.
- **Erased people hide in many places**: audit details, stored notice text, mail logs, request
  reasons, uploaded files and backups. This is why personal data lives in one table here. (R4, #493)

## 9. New with this design

**One writer**

- A write that waits on the network stops the whole church. No mail, DNS or HTTP inside a write.
- Open one connection for writes and a pool for reads. Use write-ahead logging and a busy timeout.
- Turn foreign keys on for every connection; SQLite leaves them off by default.
- Declare tables `STRICT`, or SQLite will store text in a number column without complaint.
- Never copy the live database file to back it up. Use the backup interface or `VACUUM INTO`.
- Changing a table is clumsier than in Postgres. Plan a migration as "new table, copy, swap" when
  a column must change type.
- Case-insensitive matching with `NOCASE` covers ASCII only. Normalise emails to lower case on the
  way in.
- Do not put the database on a network file system.

**Rate limits with one writer.** Every anonymous request that counted itself in the database would
queue behind the writer. Keep counters in memory in the one process, and write to the database only
when a limit is reached. Decide and document this in slice 0.

**Signing in by link**

- Mail scanners open links before the person does. A link opens a page with a button, and only the
  button's POST signs anyone in. This is the same rule as every other emailed link. The one
  exception is the browser that asked for the link (R26): it holds a cookie bound to the address,
  which no scanner has.
- A link is single use and lasts 24 hours (R8). Asking for a new one cancels the old. A day is a
  long life for a secret sitting in an inbox, so the single use, the cancel-on-new and the button
  are what protect it; do not weaken any of them.
- A session lasts a week and is renewed on each visit (R8). Renewing means a session in steady use
  never expires by time, so signing out, a change of email, being disabled and a restore must each
  end it for certain.
- Limit link requests per address and per client, silently.
- If mail is slow, nobody can sign in. Keep sessions long, and make the setup test message real.
- Admins need a second factor, and the code entry needs its own attempt limit.

**Pages rendered by the server**

- Signed-in pages are sent with `no-store`, so nothing is left on screen or in the cache after
  signing out.
- A fragment request is a full request: it needs the CSRF token on a POST and the same policy check
  as the page.
- The fragment-update script must run with script evaluation turned off, under the strict policy.
  Check this before choosing a library.

**The erased list.** It lives beside the database and is never part of a backup or a restore. It
holds each erased person's random identifier, never the row number: after a restore a row number
can be given to a new person, who would then be erased by mistake (Fold #475 saw row numbers
reissued this way). A whole-disk snapshot restore rolls the list back; say so where a person
confirms a deletion.

**The sealed audit log**

- It cannot be added later. A chain that starts in slice 4 proves nothing about slices 0 to 3, so
  the executor writes it from the first command.
- A hash chain kept only inside the database is theatre: whoever can edit a row can recompute every
  hash after it. The checkpoints that leave the server (in backups, in the admins' inboxes) are
  what make tampering show.
- Fix the exact bytes that are hashed, and version that format. A change to field order or to how a
  time is written breaks every later check.
- Order rows by sequence number, never by the clock.
- No names, addresses or free text in a row. Erasure cannot edit the log, so anything personal
  written there is permanent.
- A migration must never rebuild this table by copying rows through new code. Copy the stored
  hashes byte for byte, and check the chain before and after.
- A restore rewinds the chain honestly. Write a "restored from the backup of *date*" row first
  thing afterwards, so checkpoints mailed between that backup and the restore are explained and
  not mistaken for tampering.
- The request to open the log is the most abusable admin action there is. It is never a link, never
  a GET, always behind the second factor, and it always tells the other admins.
- Do not let the log become a viewer by the back door: no "recent activity" page, no admin search,
  no debug route reads it.

## 10. Gone with this design

If you find yourself building one of these, stop and ask.

- Lock ordering, row locks, advisory locks and "check again after waiting".
- Protecting seat counts from simultaneous writers. Count and write in the one write.
- A model of the job queue. One goroutine reads due rows; there is no claim to race for.
- Types, error wording or rules written twice, once for a server and once for a client.
- Receipts and replay for a save whose answer was lost. A form post redirects to a page; reloading
  the page shows what is stored.
- Fences against a restored database. See the erased list.
- Staged migrations across releases.
- Tests that scan source to keep two copies of something in step.

## 11. How the work went wrong

- **Reviews found races faster than churches could ever cause them.** Each became code, a test and
  a rule. Grier: the proof machinery "was inadvertently added by 'correctness' reviews that were
  supposed to review that the code was correct, not that all interactions were provable."
- **Depth arrived before use.** Erasure took as much effort as events, RSVP, calendar and groups
  together, while the setup wizard and the import were never started.
- **Simplifying inside the old shape did not hold.** Three efforts each left the code larger.
- **Copying a sibling for consistency spread a design that was itself the problem.** When a fix
  keeps fighting the design for two review rounds, stop and propose the alternative.
- **A hidden token was proposed twice and removed twice.** Before adding state to guard something,
  look for what is already stored.

## 12. Open questions

Nobody has decided these. Raise them with Grier when the slice needs them; do not guess.

- **A local time that does not exist or happens twice.** 2:30 am on the night clocks go forward,
  1:30 am on the night they go back. Refuse it, or move it? (Fold audit, never settled)
- **Changing the church's time zone after events exist.** Do existing events move? (Fold audit)
- **Finding public events of groups you are not in.** (Fold DEC-76)
- **May a person hide completely from name search?** And is "private" the right word for a group
  every signed-in member can see? (Fold DEC-78)
