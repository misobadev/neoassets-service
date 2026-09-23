# NeoAssets Service

> **Public service, to be open-sourced.** This is the backend of the NeoAssets
> scraping system. It will be exposed publicly and released as open source. It
> serves the metadata catalog, media URLs and the public scraping API, and stores
> files in Cloudflare R2.

Go service that manages NeoAssets system art packs. It stores metadata in
PostgreSQL and files in Cloudflare R2 (custom domain `cdn.neoassets.dev`).

The database is the single source of truth. There is **no** `manifest.json`
artifact anymore: the public list of approved packs is served directly from the
DB via `GET /api/v1/packs`. Any client (React web) reads pack metadata from the
API and builds file URLs against the R2 custom domain.

## Architecture

```
neostation-web (assets.neostation.dev)  -->  neoassets-service (Go API)
                                |  PostgreSQL (metadata, files, logs, versions)
                                +  Cloudflare R2 (image files only)
```

## Flow

1. A logged-in user opens the submissions app and creates a pack.
2. The web creates a submission (status `created`), uploads each file to R2 via
   presigned URLs, and can keep saving it as a draft.
3. The user submits it for review (status `pending`) with its audit log.
4. The admin (JWT login) approves it (setting the version) or rejects it.
   - Approve: the pack appears in the public list (`/api/v1/packs`).
   - Reject: the files are deleted from R2.

## Endpoints

### Public
- `GET /health`
- `GET /api/v1/config`                      runtime economy config (threads, daily games/thread, thread costs, points)
- `GET /api/v1/packs`                       approved packs (from the DB), sorted by downloads, paginated
- `GET /api/v1/packs/{packID}`              full pack detail (all files + public URLs), read-only
- `GET /api/v1/packs/{packID}/download`     same as detail but bumps the pack download counter (install/scrape)
- `GET /api/v1/systems`                     system catalog

### Submissions (user Bearer JWT)
- `POST /api/v1/submissions`                create a draft (body: name, author, description, donation_url, ai)
- `PUT /api/v1/submissions/{id}`            update/save a draft
- `POST /api/v1/submissions/{id}/upload`    request presigned URL (body: file_name, kind, system_id, size, mime_type)
- `POST /api/v1/submissions/{id}/submit`    submit for review (draft -> pending)
- `POST /api/v1/submissions/{id}/trash`     move to trash (only while draft/rejected)

### Admin (Bearer JWT)
- `POST /api/v1/admin/login`                login (email, password) -> token
- `GET /api/v1/admin/submissions`           list (filter ?status=pending|approved|rejected|created)
- `GET /api/v1/admin/submissions/{id}`      detail with files + logs
- `POST /api/v1/admin/submissions/{id}/approve`  approve (body: version)
- `POST /api/v1/admin/submissions/{id}/reject`   reject (deletes from R2)

## Public Scraping API

A credential-gated, read-only API for scraper front-ends, mounted under
`/api/v1/scrape`. Every request requires **developer application credentials**;
an optional **user credential** raises the quota. Without a user credential the
request runs in **guest mode** (1 thread, low per-minute rate limit).

Credentials (headers, over TLS):

| layer           | headers                                      | required |
|-----------------|----------------------------------------------|----------|
| developer app   | `X-Client-Id` + `X-Client-Secret`            | always   |
| end user        | `Authorization: Bearer <personal API key>`   | optional |

Endpoints:

- `GET /api/v1/scrape/systems`  system catalog (free)
- `GET /api/v1/scrape/games`    resolve **one** game by `crc`/`md5`/`sha1`/`sha256`
  or `name`, scoped to a required `system_id` (+ optional `type`, `media`). The
  lookup is hash-first and falls back to the name; `name` is cleaned server-side
  (extension and `(region)`/`[tags]` stripped) and the best candidate wins.
  Returns `{ system_id, matched_by, game }` (`game` is null when nothing
  matched) with full metadata, media URLs and the game's `scrapes` counter.
  The ROM list is omitted by default (`rom_count` is always returned; pass
  `roms=true` to include it). Consumes one quota unit.
- `GET /api/v1/scrape/popular`  most scraped games, optional `system_id`/`limit` (free)
- `GET /api/v1/scrape/account`  current subject and quota (free)

Self-service credentials (authenticated with the user JWT from `/api/v1/login`):

- `GET|POST /api/v1/auth/developer/apps`, `DELETE /api/v1/auth/developer/apps/{id}`,
  `POST /api/v1/auth/developer/apps/{id}/rotate`
- `GET|POST /api/v1/auth/api-keys`, `DELETE /api/v1/auth/api-keys/{id}`

XP & levels: approved contributions award XP (text 10 / image 40 / video 100 /
SAP image 5). XP determines a level (1-100) and a rank; each rank unlocks more
base threads, and the daily limit is **active threads x 1000** game lookups
(guest = 2 -> 2000/day; Novato level 1-10 = 4 -> 4000/day; Leyenda level 91-100
= 16 -> 16000/day; admins get 16 threads regardless of their level). Donors add an **additive** thread
bonus (supporter +2, monthly +4) on top of their level base, hard-capped at 16,
plus an XP bonus (supporter +25%, monthly +50%); donations never grant rank.
Threads also set concurrency. Counters
reset at 00:00 UTC and are reported
through `X-Quota-Limit`, `X-Quota-Remaining` and `X-Quota-Reset` response
headers. `docs/postman/neoassets-scraping.postman_collection.json` is a Postman
collection with functional tests plus guest/user rate-limit tests (run them with
the Collection Runner).

### Developer dashboard and debug mode

Each app accumulates lifetime usage counters (API calls, KO scraps, rate-limited,
over-quota, debug calls, last scrape) shown in the web developer page. Counters
are split per **version** when the client sends `softname=<name>` (query param
or `X-Software-Name` header); otherwise the developer app name is used. Each app
also gets a **debug password** (owner-only) that unlocks debug query parameters
capped at 100 calls/day per app:

- `devdebugpassword=<password>` (or `X-Debug-Password`) enables debug mode.
- `forcerequestok=N` reports N as the daily used counter (set above the limit to
  simulate over-quota).
- `forcethreads=N` overrides the thread count for the request.
- `forceratelimit=1` forces a `429`.

Note: because R2 objects are public and the catalog is also browsable through
`/api/v1/metadata/*`, the quota is an anti-abuse contract rather than a hard
security boundary.

## Submission states

| status     | meaning                                                     |
|------------|-------------------------------------------------------------|
| `created`  | draft saved by the user, editable                            |
| `pending`  | submitted for review, awaiting admin                         |
| `approved` | published. Only these appear in `GET /api/v1/packs`          |
| `rejected` | rejected by admin; files removed from R2                     |
| `trashed`  | trashed by the user (only while draft/rejected); files removed |

Approved packs are read-only. To change a published pack (new images, updated
description), any user creates a **contribution**: a pending submission with the
same `pack_id` (`contribution=true`). On approval it becomes the new latest
revision of the pack and the contributor is added to the pack's `contributors`;
pending/draft contributions are listed publicly on the pack.

## R2 Layout

```
r2://neoassets/
  packs/{packId}/theme.json
  packs/{packId}/backgrounds/{system}.webp|gif
```

A pack has no dedicated preview image: its public thumbnail is the background of
a popular system (snes, ps1, gba or genesis, falling back to the Atari 2600).

Objects are public via the `cdn.neoassets.dev` custom domain and do not
need signatures. **CORS must be configured on the bucket** so the browser can
`PUT` uploads directly to `*.r2.cloudflarestorage.com` (see R2 bucket ->
Settings -> CORS policy). Without it, presigned uploads fail with a CORS error.

## Configuration (.env)

```
# Service
PORT=8090

# PostgreSQL (own instance, starts alongside the service in the compose)
POSTGRES_DB=neoassets
POSTGRES_USER=neoassets_user
POSTGRES_PASSWORD=...
POSTGRES_PORT=5433   # host-exposed port (5432 is already used by the provider)

# JWT for the admin panel
JWT_SECRET=...

# Cloudflare R2
R2_ACCOUNT_ID=...
R2_ACCESS_KEY=...
R2_SECRET_KEY=...
R2_BUCKET_NAME=neoassets
R2_PUBLIC_BASE_URL=https://cdn.neoassets.dev

# Initial admin (seeded into the DB on startup)
ADMIN_SEED_EMAIL=admin@neostation.dev
ADMIN_SEED_PASSWORD=...

# Public scraping API (0 = built-in default)
SCRAPE_GUEST_THREADS=0            # guest threads (default 2)
SCRAPE_ADMIN_THREADS=0            # admin threads (default 16)
SCRAPE_SUPPORTER_BONUS_THREADS=0        # one-time donor bonus threads (default 2)
SCRAPE_MONTHLY_SUPPORTER_BONUS_THREADS=0 # monthly donor bonus threads (default 4)
XP_SUPPORTER_BONUS_PCT=0                # one-time donor XP bonus % (default 25)
XP_MONTHLY_SUPPORTER_BONUS_PCT=0        # monthly donor XP bonus % (default 50)
SCRAPE_DAILY_GAMES_PER_THREAD=0   # daily games per thread (default 1000; limit = threads * this)
SCRAPE_GUEST_RPM=0                # guest requests/minute (default 10)
SCRAPE_USER_RPM=0                 # user requests/minute (default 60)

# XP economy (registered-user threads come from the level ranks, not config)
POINTS_TEXT_METADATA=0            # XP per approved text field (default 10)
POINTS_IMAGE_METADATA=0           # XP per approved image (default 50)
POINTS_VIDEO_METADATA=0           # XP per approved video (default 100)
POINTS_SAP_IMAGE=0                # XP per approved SAP image (default 20)
POINTS_NEW_GAME=0                 # XP bonus for an approved new game (default 100)
ENABLE_DEBUG_MODE=false           # allow X-Debug-Password force overrides (keep off in prod)
CORS_ORIGINS=http://localhost:5173,http://localhost:8091
```

`DATABASE_URL` is built automatically in the compose pointing at the `postgres`
container (internal DNS), so it does not need to be set manually.

The initial admin is seeded into the DB on startup when `ADMIN_SEED_EMAIL` and
`ADMIN_SEED_PASSWORD` are defined.

## Development

```bash
cp .env.example .env   # fill in values
go mod tidy
go run ./cmd
```

## Docker (all-in-one)

The `docker-compose.yml` starts the **PostgreSQL** (with a persistent volume)
and the **API service** together. The API waits for Postgres to be healthy
before starting.

```bash
docker compose --env-file .env up -d --build
```

### Persistence

Database data is stored in the named `postgres_volume` and survives restarts
and `docker compose up/down`. The schema (tables) is created by the service
itself with its migrations on startup.

> Note: the PostgreSQL image creates the role and database from
> `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` on first init. To
> re-initialize from scratch (deletes data): `docker compose --env-file .env down -v`.

### Access

- Postgres host: `localhost:${POSTGRES_PORT:-5433}`
- API host: `localhost:${PORT:-8090}`
- API healthcheck: `GET /health`
