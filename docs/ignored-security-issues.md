# Security findings written down and not fixed

AGENTS.md: each security finding is scored for likelihood and severity, 1 to 10 each. A
product of 4 or less is recorded here instead of fixed. Re-score an entry when the code
around it changes.

| Date | Finding | Likelihood | Severity | Product | Why it stays |
|---|---|---|---|---|---|
| 2026-11-26 | Setup makes the server connect to a mail server host and port that the person setting it up types in. "The server never fetches a URL a person supplied." | 1 | 3 | 3 | Only the holder of the setup code, read from the server's own log, can type it, and only until setup is done. That person already controls the server. The connection speaks SMTP only and the page shows only a cleaned error. A later admin settings page must ask again. |
| 2026-11-26 | One person can cancel another's sign-in link by asking for a new link to their address (asking for a new link cancels the old one, R8). | 2 | 2 | 4 | Each client may ask for a given address 3 times an hour. The address's own cap (6 an hour, 20 a day) can only be used up by someone sending from several networks. Sessions last a week, so the owner is rarely waiting on a link. |
| 2026-11-26 | Rate-limit counters live in memory, so restarting the server resets them. | 1 | 2 | 2 | Only someone who can restart the server can reset them, and that person needs no limit. Decided in `internal/ratelimit` (docs/HAZARDS.md section 9). |
