# Running the site

Every command here runs on the server, in the folder with `docker-compose.yml`.

## Email to work and school accounts

Personal Gmail and Outlook.com addresses get the site's email in the inbox. A work or school
address can be different: its organization may hold or discard mail from a sender it does not
know, without telling the person, even after Postmark reports the message as delivered. If
someone with such an address never gets a sign-in link, ask them to try a personal address, or
ask their organization's IT to allow mail from your church's sending domain.

## Backups

A backup is taken every night at 3 am, church time, and the newest 14 are kept in the app's
data volume. Each has a checkpoint file beside it naming the audit log's last row and hash
at that moment.

```sh
docker compose exec app /fold backups     # list them
docker compose exec app /fold backup      # take one now
```

The backups live on the same server as the site. A failed disk or a lost server takes them
too, so copy them somewhere else now and then, or turn on your VPS provider's own backups.
The files are in the `data` volume, under `backups/`:

```sh
docker compose cp app:/data/backups ./backups-copy
```

A copy of the backups does not carry the secret key (it is in `.env`), so the saved mail
settings in it cannot be read without that key.

## Restoring a backup

```sh
docker compose exec app /fold restore 20261125-030000
```

The restore:

1. checks the backup, and its audit log against its checkpoint, and refuses a damaged one;
2. keeps a copy of the current database as a new backup first;
3. replaces the database in one step;
4. signs everyone out and stops every emailed link and every calendar feed, so nothing from
   the backup's day comes back to life. Members who subscribed to their calendar on a phone
   turn the feed on again from their profile and add the new link;
5. writes "restored from the backup of *date*" in the audit log.

Anything changed since the backup was taken is gone.

If the site will not start, stop it and restore without it:

```sh
docker compose stop app
docker compose run --rm app restore 20261125-030000
docker compose up -d
```

## Upgrading, and going back

Change `VERSION` in `.env` and run `docker compose up -d`. Before a new version changes the
database it keeps a copy as an ordinary backup, listed by `fold backups` as "before upgrading
from schema N". Changes to the database only ever go forward. To go back:

```sh
# set VERSION back to the old release in .env, then:
docker compose stop app
docker compose run --rm app restore <the "before upgrading" backup>
docker compose up -d
```

## Checking the audit log

The audit log is checked every morning. If the check finds a problem, every administrator
is emailed. Once a month every administrator is also emailed the current checkpoint, so a
later rewrite of the log would show. To check it by hand:

```sh
docker compose exec app /fold verify
```

## Rotating the secret key

Do this if `.env` may have been seen by someone it should not have been.

1. Put the current value of `SECRET_KEY` in `SECRET_KEY_PREVIOUS`.
2. Set `SECRET_KEY` to a new `openssl rand -hex 32`.
3. Run `docker compose up -d`. On start the site re-encrypts the saved mail settings with
   the new key and says so in its log.
4. Remove `SECRET_KEY_PREVIOUS` and run `docker compose up -d` again.

Keep the old key with the church's records for as long as you keep backups taken before
the change (14 nights on the server, longer for copies kept elsewhere). Restoring one of
those backups needs it: set it as `SECRET_KEY_PREVIOUS` first. A restore refuses a backup
whose mail settings open with neither key, rather than leave the site unable to send
sign-in links.

Anyone in the middle of filling in a form sees "That page was out of date" once. Nobody is
signed out. Each use of the key (form tokens, saved settings, emails waiting to go, the
setup code) has its own key derived from the secret.

## Where the setup code is

Until setup is finished, `docker compose exec app /fold setup-code` prints it. It is also in
the log (`docker compose logs app`). It changes each time the app starts.
