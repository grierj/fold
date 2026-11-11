# Fold design: 6. Permissions

## 6. Permissions matrix

Roles:

- **Admin**: `User.role=admin`. Church staff. Admins see everything, invite-only groups included (DEC-5).
- **Leader**: has `Membership.role=leader` in a specific group. Leader rights apply only to the groups they lead and to those groups' events. Leaders have broad powers over their groups' events, including visibility (DEC-17). Leading a `pending` or `archived` group grants no power to add or invite people (DEC-88), to create events, or to book rooms (DEC-147).
- **Host**: an adult assigned with `EventHost` to a church-wide event (DEC-7). Host rights apply only to that event. Hosts are often admins but don't have to be. A host row on a group event grants nothing.
- **Member**: a signed-in adult with `status=active`.
- **Teen**: a signed-in user aged 13 to 17 with `account` consent (DEC-8). Teens have member rights, narrowed as the table shows. They are never leaders, hosts or admins (DEC-18).
- **Guest**: not signed in. A `status=guest` row, created when a guest confirms a public registration, never signs in.
- **Guardian** is a relationship, not a role: an adult with a confirmed `GuardianLink` to a teen or a household child. It adds the rights marked "guardian" below.
- A **moderator** role between member and leader (for example, someone who may remove attendees but not edit the group) may come later. It is a note only; do not build it (DEC-17).

"Own group" = a group where the user is an active leader. "Own event" = an event of an own group, or a church-wide event the user hosts.

All checks live in `internal/policy` as pure functions with table tests. Handlers call policy; they never inline role checks, and they never read `Group.LeaderID` for permissions (C12).

SMS, one-to-one messages and the church directory are not built yet. Cells about them are marked **Planned** with their milestone.

**Seeing and joining**

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| See public groups and events | Yes | Yes | Yes | Yes | Yes |
| See private groups and events | Yes | Yes | Yes | Yes | No |
| See invite-only groups and events | Yes, in lists and by id (DEC-5) | Own groups only | Groups they belong to only | Groups they belong to only | No |
| See an invite-only group's event made `private` or `public` (DEC-17) | Yes | Yes | Yes (event only; the group stays hidden) | Yes (event only) | `public` only (event only) |
| See pending or archived groups | Yes | Own only | No | No | No |
| Register for a public event | Yes | Yes | Yes | Yes | Yes, with name, a verified email, and "I am 18 or older" |
| Register for a private event | Yes | Yes | Yes | Yes (DEC-20) | No |
| Register for an invite-only event | Yes | Own groups | Group members only | Group members only | No |
| Register a household person (child, teen, or adult on the account) | Yes, for the account holder | As account holder | As account holder | No | No |
| Join a group | Yes | n/a | Public and private groups: join at once; invite-only: by invitation only (DEC-30) | Same as member | Public groups: "Log in to join"; someone with no account asks for one at `/join` (an account request, DEC-4) |

**Accounts, ages, households** (DEC-4, DEC-8, DEC-23)

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| Self-signup | n/a | n/a | n/a | n/a | Creates an account request (while `self_signup` is `request`) |
| Invite a new person (no approval needed) | Yes, into any active group or none | Leaders of an approved group: yes, into an own active group or no group; leading only a pending or archived group is not enough (DEC-88). Hosts: no | No | No | No |
| "Invite a friend" (creates an account request) | Yes (still a request an admin decides) | Yes (still a request an admin decides) | Yes | No | No |
| Approve or reject account requests | Yes | No | No | No | No |
| Create a household; add household people | Any household | Own household only; children and teens wait for an admin to confirm the link (DEC-32) | Own household only; children and teens wait for an admin to confirm the link (DEC-32) | No | No |
| Turn a household person into their own account | Yes | As an adult in the household (a teen needs a confirmed household guardian link, DEC-32); an admin approves the account | Same as leader | No | No |
| Create a teen account | Yes | Leaders who may invite: by invitation (the invitee answers 13 to 17 and starts `pending_consent`); anyone: from own household (as guardian; an admin confirms the link, DEC-32) | From own household only (as guardian; an admin confirms the link, DEC-32) | n/a | Self-signup as a teen (request, then consent) |
| Confirm any guardian or household link to a teen or child (DEC-32) | Yes, with a note on how it was checked; not their own link while another active admin exists (DEC-79) | No | No | No | No |
| Grant or revoke teen consent (account, sms, photos) | Yes; granting needs evidence text | Only as that teen's confirmed guardian; the leader role never grants consent | Only as that teen's confirmed guardian | No | No |
| Turn guardian copies on or off (DEC-19) | Yes (audited) | No | No | No | No |
| See a household person's name and age | Yes | Account holder; and leaders/hosts of events the person is registered for, once any needed link is confirmed | Account holder only | No | No |
| See a household child's care notes | Yes | Guardian; and leaders/hosts of the event while the child is `going` on a scheduled date, from 24 hours before it starts to 24 hours after it ends (DEC-81) | Guardian only | No | No |
| See a teen's contact details | Yes | No: message through Fold; direct contact only with a grant that an admin and the teen both approved (DEC-31) | Confirmed guardian only | Own only | No |
| Hide my contact info from group leaders (adults; off by default, DEC-31) | Own | Own | Own | n/a (always hidden) | No |
| Request direct contact for a hidden person (every attempt logged) | n/a (admins see contact details) | Leaders: members of own groups and registrants of their groups' events; hosts: registrants of own events | No | No | No |
| Approve or decline a direct-contact request | For teens (the teen must approve too) | As the subject | As the subject (a hidden adult decides alone) | As the subject (with an admin) | No |
| Revoke a direct-contact grant | Any | As the subject | As the subject; as a confirmed guardian, for their teen | As the subject | No |
| See and export contact-access history | All (CSV export for legal requests) | Own requests | Requests about them | Requests about them | No |

**Groups and events**

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| Create a group | Yes (active at once) | Leaders: yes (pending) | Yes (pending) | No | No |
| Approve or reject groups | Yes | No | No | No | No |
| Edit a group, manage its roster (add members) | Any | Own groups (leaders); adding people only while the group is approved (DEC-88) | No | No | No |
| Remove a member from a group (audited; DEC-35, M0.3b) | Anyone except the group's last leader | Own groups (leaders): anyone except an admin or the last leader; co-leaders included | No (leave instead) | No (leave instead) | No |
| Set or clear a group's member limit (DEC-39) | Any | Own groups (leaders) | No | No | No |
| Add someone to a full group (explicit over-limit choice, audited; DEC-39) | Any | Own groups (leaders) | No (self-join refused: "This group is full") | No | No |
| Make someone a group leader, or a leader a member again | Any adult member; the last leader is never demoted | Own groups, adult members only; audited, and admins get an in-app notice (no admin gate, DEC-31, DEC-34); can't demote an admin (DEC-35) | No | No | No |
| Create or edit events in a group | Any active group (create); any (edit) | Own groups; creating only while the group is active (DEC-147) | No | No | No |
| Create church-wide events | Yes | No | No | No | No |
| Assign or remove hosts of a church-wide event | Yes (active adults only; the event always keeps a host) | No | No | No | No |
| Edit a church-wide event | Any | Hosts: own events | No | No | No |
| Set a group event's visibility to any value (`public`, `private`, `invite_only`); more-open-than-group changes are audited and notified (DEC-6, DEC-17) | Any | Own group events (leaders) | No | No | No |
| Switch a church-wide event between `public` and `private` | Any | Hosts: own events | No | No | No |
| Remove (prune) a registration, with an optional note | Any | Own events | Own registrations only (cancel) | Own only (cancel) | Own, from the manage link |
| See an event's attendee list, with Teen and Child badges (DEC-20) | Any | Own events | Adult attendees' names; teens and children show only as a count. For an invite-only group's event made `private` or `public`: a count only, unless they're in the group | Same as member | No |
| See contact details (email, phone) of adults | Yes | Members of own groups and registrants of own events, unless the adult hides them (then through Fold, or with the adult's grant, DEC-31) | No. **Planned (M6.1):** per the person's directory settings | No | No |

**Rooms, messaging, calendar, admin**

| Action | Admin | Leader / Host | Member | Teen | Guest |
|---|---|---|---|---|---|
| Manage rooms | Yes | No | No | No | No |
| Book a room | Any room, for events of active groups or church-wide events (DEC-147) | Own events of an active group or hosted church-wide events, non-restricted rooms | No | No | No |
| See the room calendar | Full details, restricted rooms and bookings waiting for approval included (DEC-104, DEC-109) | Details of bookings they can see; others show "Reserved" | Same as leader | No | No |
| Email all members | Yes | No | No | No | No |
| Email a group's members | Any group | Own approved groups (leaders, DEC-88) | No | No | No |
| Email registrants of an occurrence, a series, or a chosen set of events | Any events | Own events only (every chosen event must be an own event) | No | No | No |
| SMS the same audiences. **Planned (M3.7)** | As for email | As for email | No | No | No |
| See recipient lists with Teen badges and counts | Yes | For their own messages, and reminders of events they run (DEC-133) | No | No | No |
| See message history | All | Messages they wrote, and reminders of events they run (DEC-133). **Planned (M3.9):** SMS requests they approved | Announcements sent to them, read at `/messages/:id/read` (DEC-135) | Same as member | No |
| Send a one-to-one message through Fold (in-app, email, SMS; 7.8). **Planned (M3.11)** | Anyone | Members of own groups and registrants of own events | Reply in a conversation; "Message the organizers" of events they can see | Same as member | No |
| Request a group SMS. **Planned (M3.9)** | n/a | n/a (they send directly) | Yes, for groups they belong to | No | No |
| Approve a group SMS request. **Planned (M3.9)** | Any group | Own groups (leaders) | No | No | No |
| See the SMS opt-out log (read-only). **Planned (M3.7)** | Yes | No | No | No | No |
| Override the SMS send window (warning, reason, audited; DEC-9). **Planned (M3.8)** | Yes | No | No | No | No |
| Change the church-wide SMS send window. **Planned (M3.8)** | Yes (warning if outside 8 AM to 9 PM) | No | No | No | No |
| Personal ICS feed | Yes | Yes | Yes | Yes | No (public feeds only) |
| Room ICS feed | Yes | No | No | No | No |
| Configure providers, backups, settings | Yes | No | No | No | No |
| Manage users and roles | Yes | No | No | No | No |
| See the audit log | Yes | No | No | No | No |
| Background-check flags. **Planned (M6.8)** | Yes (and an optional safety-coordinator role) | No | Own status only | No | No |

**Guard rails.**

- The last admin cannot be demoted or deleted, and nobody deletes or demotes themselves.
- Invite-only groups answer 404 (not 403) to people who cannot see them.
- A teen cannot be made a leader, host, or admin (DEC-18). No path creates an account for someone under 13.
- A group cannot lose its last leader. A leader cannot remove or demote an admin (DEC-35).
- No guardian or household link to a teen or child is confirmed without an admin (DEC-32).
- No API response to a leader or host includes a teen's email or phone (DEC-31).
- An account that is a group's only leader cannot be deleted or disabled until the admin picks a successor for each such group: an active adult member of the group, or any admin (DEC-36).
- A disabled account never keeps a leader role. Its memberships and registrations stay but are hidden and never counted while it is disabled. Re-enabling never restores leadership, and leaves the person out of any group or event that is now full, with a notice to ask a leader (DEC-38, DEC-47).
- Rosters show leaders which members are admins, and leaders never get a Remove button for them (DEC-38).
- An account that is the only non-deleted host of a church-wide event cannot be deleted until the admin picks an eligible replacement host (active, adult, never chosen automatically) for each such event. Disabling keeps host rows, and a deleted account never counts as a host (DEC-144).
