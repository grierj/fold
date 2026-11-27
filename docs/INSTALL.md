# Installing

For a volunteer setting up their church's site. Allow about 15 minutes. You need:

- A small Linux server (a VPS) with Docker installed. One with 1 GB of memory is plenty.
  Many providers offer a "Docker" image that has it ready.
- A domain or subdomain for the site, such as `groups.yourchurch.org`, and access to its DNS.
- A mail account the site can send from: a [Postmark](https://postmarkapp.com) server token
  (the one we suggest), or the name, port, username and password of a mail server that
  offers encryption.

## 1. Point the domain at the server

In your DNS, add an `A` record for the site's name with the server's IP address. It can
take a few minutes to take effect; carry on meanwhile.

## 2. Put the files on the server

Sign in to the server and run:

```sh
mkdir fold && cd fold
curl -fsSLO https://raw.githubusercontent.com/grierj/fold/master/docker-compose.yml
curl -fsSLO https://raw.githubusercontent.com/grierj/fold/master/Caddyfile
curl -fsSL -o .env https://raw.githubusercontent.com/grierj/fold/master/.env.example
```

## 3. Fill in `.env`

Open `.env` in an editor (`nano .env`) and set:

- `DOMAIN`: the site's name, without `https://`.
- `VERSION`: the release to run, from the project's releases page.
- `SECRET_KEY`: run `openssl rand -hex 32` and paste what it prints.

Save a copy of `.env` somewhere safe, such as the church's password manager. Without the
secret key, the saved mail settings cannot be read on a new server.

## 4. Start it

```sh
docker compose up -d
```

Caddy gets the site's HTTPS certificate on its own the first time someone visits.

## 5. Open the site and finish setup

Get the setup code:

```sh
docker compose exec app /fold setup-code
```

Open `https://` and your domain in a browser, enter the code, and answer the questions: your
church's name and time zone, your name and email, and the mail settings. The site sends you
a test message. Open it and press **Finish setup**. You are now signed in as the first
administrator.

If the test message does not arrive, the page says why it could not be sent. Change the
settings and send it again.

## Afterwards

- A backup is taken every night at 3 am, church time. See `docs/OPERATIONS.md` for
  restoring one and for keeping copies off the server.
- To upgrade, change `VERSION` in `.env` and run `docker compose up -d` again. The site
  copies its database before changing it.
