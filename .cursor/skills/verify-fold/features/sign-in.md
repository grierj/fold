# Sign in

Sign in lets the first administrator finish setup, rejects a wrong password, and opens the signed-in home page as that person. A reload keeps the same session.

## Sub-features

- `sign-in-setup` creates the church site and the first admin from the setup wizard.
- `sign-in-reject` stays on the login form and shows an error for a wrong password.
- `sign-in-accept` opens the home page and shows the person's name in the header.
- `sign-in-reload` shows the same signed-in home page after a reload.

## How to get to it (user POV)

- Open the site before any account exists. Fold asks for the setup code before showing the wizard.
- Choose `Log in` in the header.
- Open `/login`. A signed-out visit to a page that needs an account (for example `/groups/new`) lands here too, and signing in returns to that page.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok` for an instance that has not completed setup.
- No user exists in that instance's database.

- **Run the packaged proof.** This performs the steps below. Run `.cursor/skills/verify-fold/verify-fold drive "$RUN_ID" sign-in`. The command exits 0, and `/tmp/fold-verify-proof/$RUN_ID/sign-in/` contains `bad-password.png`, `after-login.png`, `after-reload.png`, `me.json`, `result.txt`, and `users.csv`.
- **Create the site.** On `/setup`, enter the verify instance's setup code (`a` repeated 64 times) and choose `Continue`. Fill site name `Fold verify`, time zone `America/Boise`, name `Verify Admin`, email `verify-admin@example.com`, and password `password1234` in both password fields, and tick `I am 18 or older. The first administrator must be an adult.` Choose `Create site`. The browser lands on `/login`. Setup does not start a session. The setup inputs are `[name=site_name]`, `[name=timezone]`, `[name=name]`, `[name=email]`, `[name=password]`, `[name=password_confirm]` and the checkbox `[name=age_band]`. The button is `getByRole("button", { name: "Create site" })`.
- **Reject a bad password.** The heading reads `Log in to Fold`. Fill `Email` with `verify-admin@example.com` and `Password` with `not-the-password`. Choose `Log in`. The text `That email and password don't match. Check them and try again.` is visible and the URL still contains `/login`. `bad-password.png` shows that form.
- **Sign in.** Replace `Password` with `password1234` and choose `Log in`. The URL is no longer `/login`. The level-1 heading reads `Good evening, Verify` (`Good morning` or `Good afternoon` earlier in the day; the first name only). The header (`getByRole("banner")`) shows `Verify Admin` and a `Log out` button, and the `Groups` section is below the greeting. `after-login.png` shows that page.
- **Confirm the session.** `GET /api/v1/auth/me` from the same browser returns 200. `me.json` has `user.email` `verify-admin@example.com` and `user.name` `Verify Admin`.
- **Reload.** Reload the home page. The same greeting, header name, and `Log out` button are still there. `after-reload.png` shows that page.
- **Confirm the stored user.** `users.csv` from the verify database contains `verify-admin@example.com,Verify Admin,admin`. The dev database named `fold` does not contain that email.

## Gotchas

- A fresh server redirects `/` to `/setup` until the first user exists. The process remembers that setup finished until it exits, even if the users are later removed.
- Password must be at least 10 characters. `password1234` is long enough.
- `Log in` is a link in the header and a button on the form. Choose the button to submit.
- The header's home link shows the church name (`Fold verify`) over `powered by Fold`. It is not proof of which account is signed in. The header name and `Log out` are.
- Only the signed-out home page has the heading `Find your people. Grow in faith together.` A signed-in person sees the greeting instead. `drive-sign-in.mjs` still waits for the old heading after login, so the packaged drive stops with a timeout after `bad-password.png` until that wait is changed to the greeting. The steps above are what it should check.
- `GET /api/v1/auth/me` without the browser's `fold-session` cookie does not prove the form worked.
