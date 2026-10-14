# Log out

Log out ends the session. The directory drops to what a visitor can see, and Back does not restore the signed-in page.

## Sub-features

- `log-out-header` replaces `Log out` with `Log in` and hides the person's name.
- `log-out-api` makes `GET /api/v1/auth/me` return 401.
- `log-out-back` stays signed out when the browser returns to a page viewed while signed in.

## How to get to it (user POV)

- Choose `Log out` in the header while signed in.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- `Verify Admin` is signed in. A public group `Room 201 class` exists. If a private group `Members-only circle` exists, the admin can see it before logout.
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/log-out/`.

- **Note the session.** On `/`, the header shows `Verify Admin` and `Log out`. Read the `fold-session` cookie value from the browser context.
- **Log out.** Choose `Log out`. Run `page.getByRole("button", { name: "Log out" }).click()`. The URL is `/`. The header link `Log in` is visible. The header (`page.locator("header")`) no longer shows `Verify Admin`, and the button `Log out` is absent. Group cards still read `Organized by Verify Admin`, so check the header, not the page. A public heading `Room 201 class` is still visible. A private group name is not.
- **Confirm the server session.** `GET /api/v1/auth/me` from this browser returns 401. The `fold-session` cookie value is not the value captured before logout.
- **Back.** Use the browser Back control to return to a group URL that was open while signed in, then Back again to `/`. Each page still shows `Log in`, and its header does not show `Verify Admin`.
- **Proof.** `log-out/directory.png` shows the Fold header, `Log in`, and the public group, without the admin's name. `log-out/me.txt` records status 401. `log-out/result.txt` names `log-out-header` and the header `Log out` button.

## Gotchas

- Logout leaves a new anonymous `fold-session` cookie for the CSRF token. Compare the value to the pre-logout cookie. An empty cookie jar is not the check.
- `localStorage` and `sessionStorage` are cleared. That is supporting evidence. The 401 from `auth/me` is the session check.
- Back must load the page again. A screenshot of a page that was never revisited does not prove the stale signed-in view is gone.
- Do not prove logout by calling an API logout route from `curl` without using the header button.
