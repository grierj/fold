# Log out

Log out ends the session. The home page drops to what a visitor can see, and Back does not restore the signed-in page.

## Sub-features

- `log-out-header` replaces `Log out` with `Log in` and hides the person's name.
- `log-out-api` makes `GET /api/v1/auth/me` return 401.
- `log-out-back` stays signed out when the browser returns to a page viewed while signed in.

## How to get to it (user POV)

- Choose `Log out` in the header while signed in (640px and wider).
- On a phone (under 640px), open the `Me` tab and choose `Log out` on the profile page; the bottom tab bar disappears.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- `Verify Admin` is signed in. A public group `Room 201 class` exists. A private group `Members-only circle` exists, so the admin can see it before logout (the directory recipe creates both).
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/log-out/`.

- **Note the session.** On `/`, the header shows `Verify Admin` and `Log out`, and `Members-only circle` is listed. Read the `fold-session` cookie value from the browser context. Open `/groups/<id>` of any group too, so Back has a signed-in page to return to.
- **Log out.** Choose `Log out`. Run `page.getByRole("button", { name: "Log out" }).click()`. The page reloads at `/`. The header link `Log in` is visible. The header (`page.getByRole("banner")`) no longer shows `Verify Admin`, and the button `Log out` is absent. Group cards still read `Organized by Verify Admin`, so check the header, not the page. A public heading `Room 201 class` is still visible. `Members-only circle` is not.
- **Confirm the server session.** `GET /api/v1/auth/me` from this browser returns 401. The `fold-session` cookie value is not the value captured before logout.
- **Back.** Use the browser Back control to return to a group URL that was open while signed in, then Back again to `/`. Each page still shows `Log in`, and its header does not show `Verify Admin`.
- **On a phone.** At 390px wide, sign in, open `/`, choose `Me` in `getByRole("navigation", { name: "Tabs" })`, then `Log out`. The browser lands on `/` with `Log in` and the tab bar is gone.
- **Proof.** `log-out/directory.png` shows the Fold header, `Log in`, and the public group, without the admin's name. `log-out/me.txt` records status 401. `log-out/result.txt` names `log-out-header` and the header `Log out` button.

## Gotchas

- Logout leaves a new anonymous `fold-session` cookie for the CSRF token. Compare the value to the pre-logout cookie. An empty cookie jar is not the check.
- `localStorage` and `sessionStorage` are cleared. That is supporting evidence. The 401 from `auth/me` is the session check.
- Back must load the page again. A screenshot of a page that was never revisited does not prove the stale signed-in view is gone.
- Do not prove logout by calling an API logout route from `curl` without using the header button.
- `page.locator("header")` matches the page's own `<header>` as well as the site header; use `getByRole("banner")`.
- Logout ends with a full page load of `/`: wait for the `load` event with the click (`Promise.all([page.waitForEvent("load"), click])`) before the next `goto`, or the reload aborts it.
