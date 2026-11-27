# Checks only a person can run

These need real phones, real inboxes or a real server. They do not block a merge (Fold DEC-128).
Run them before the pilot church goes live, and note the date and result here.

| Slice | Check | Who | Result |
|---|---|---|---|
| 0 | Install from a blank VPS by following `docs/INSTALL.md`, timed. Target: 15 minutes to the signed-in home page | Grier, on a test server The timed run by a person is deferred until Grier sits with an installer who is not him: "I'm happy with how fast an agent can do it and until I'm sitting with a human that's not me, it won't be meaningful." (2026-11-27) |
| 0 | The setup test message and a sign-in link arrive in a Gmail inbox, not in spam | Grier | 2026-11-27: passed. Both arrived in the Gmail inbox through Postmark, and opening each in the asking browser signed in at once (R26) |
| 0 | The same in an Outlook inbox | Grier | 2026-11-27: passed for Outlook.com (Focused inbox, one click on the same device). A school Microsoft 365 account (mbts.edu) accepted the message ("250 Queued for delivery") but never showed it, not in Junk or quarantine: that school's own mail rules. See docs/OPERATIONS.md, "Email to work and school accounts" |
| 0 | The setup pages, sign-in, home and the component gallery look right on a real iPhone and a real Android phone | Grier | 2026-11-27: passed on an iPhone. Android waived: "I have no access to an android, but I'm not worried about it." |
| 0 | A real backup restores on the test server with `fold restore`, and the restored site works | | 2026-11-27: passed. The copy before restoring was kept, the chain stayed whole, Grier was signed out and signed in again by a new link |
| 0 | The app's log (`docker compose logs app`) shows visitors' real public addresses as `client`, not `172.30.0.1`. If every visitor shows the same address, rate limits would be shared by the whole church; check Docker's userland proxy and IPv6 setup | | 2026-11-27: passed on the walkthrough droplet; the log showed real public addresses |
