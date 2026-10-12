# Sign in

Sign in lets the first administrator finish setup, rejects a wrong password, and opens the directory as that person. A reload keeps the same session.

## Sub-features

- `sign-in-setup` creates the church site and the first admin from the setup wizard.
- `sign-in-reject` stays on the login form and shows an error for a wrong password.
- `sign-in-accept` opens the directory and shows the person's name in the header.
- `sign-in-reload` shows the same signed-in directory after a reload.

## How to get to it (user POV)

- Open the site before any account exists. Fold shows the setup wizard.
- Choose `Log in` in the header.
- Open `/login`.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok` for an instance that has not completed setup.
- No user exists in that instance's database.

- **Run the packaged proof.** This performs the steps below. Run `.cursor/skills/verify-fold/verify-fold drive "$RUN_ID" sign-in`. The command exits 0, and `/tmp/fold-verify-proof/$RUN_ID/sign-in/` contains `bad-password.png`, `after-login.png`, `after-reload.png`, `me.json`, `result.txt`, and `users.csv`.
- **Create the site.** On `/setup`, fill site name `Fold verify`, time zone `America/Boise`, name `Verify Admin`, email `verify-admin@example.com`, and password `password1234` in both password fields. Choose `Create site`. The browser lands on `/login`. Setup does not start a session. The setup inputs are `[name=site_name]`, `[name=timezone]`, `[name=name]`, `[name=email]`, `[name=password]`, and `[name=password_confirm]`. The button is `getByRole("button", { name: "Create site" })`.
- **Reject a bad password.** The heading reads `Log in to Fold`. Fill `Email` with `verify-admin@example.com` and `Password` with `not-the-password`. Choose `Log in`. The text `Invalid email or password.` is visible and the URL still contains `/login`. `bad-password.png` shows that form.
- **Sign in.** Replace `Password` with `password1234` and choose `Log in`. The URL is no longer `/login`. The heading reads `Find your people. Grow in faith together.` The header shows `Verify Admin` and a `Log out` button. `after-login.png` shows that page.
- **Confirm the session.** `GET /api/v1/auth/me` from the same browser returns 200. `me.json` has `user.email` `verify-admin@example.com` and `user.name` `Verify Admin`.
- **Reload.** Reload the directory. The same heading, header name, and `Log out` button are still there. `after-reload.png` shows that page.
- **Confirm the stored user.** `users.csv` from the verify database contains `verify-admin@example.com,Verify Admin,admin`. The dev database named `fold` does not contain that email.

## Gotchas

- A fresh server redirects `/` to `/setup` until the first user exists. The process remembers that setup finished until it exits, even if the users are later removed.
- Password must be at least 10 characters. `password1234` is long enough.
- `Log in` is a link in the header and a button on the form. Choose the button to submit.
- The header logo is the link named `Fold`. It is not proof of which account is signed in. The header name and `Log out` are.
- `GET /api/v1/auth/me` without the browser's `fold-session` cookie does not prove the form worked.
