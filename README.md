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
neoassets-web (neoassets.dev)  -->  neoassets-service (api.neoassets.dev, Go)
scrapers / frontends           -->     |  PostgreSQL (catalog, packs, users, logs)
                                       +  Cloudflare R2 (images and videos, served
                                          by cdn.neoassets.dev)
```

## Flow

1. A logged-in user opens the submissions app and creates a pack.
2. The web creates a submission (status `created`), uploads each file to R2 via
   presigned URLs, and can keep saving it as a draft.
3. The user submits it for review (status `pending`) with its audit log.
4. An admin approves it (setting the version) or rejects it.
   - Approve: the pack appears in the public list (`/api/v1/packs`).
   - Reject: the files are deleted from R2.

## Endpoints

### Public
- `GET /health`
- `GET /api/v1/config`                      runtime economy config (ranks and threads, requests/min per thread, donor tiers, XP points)
- `GET /api/v1/packs`                       approved packs (from the DB), sorted by downloads, paginated
- `GET /api/v1/packs/{packID}`              full pack detail (all files + public URLs), read-only
- `GET /api/v1/packs/{packID}/download`     same as detail but bumps the pack download counter (install/scrape)
- `GET /api/v1/systems`                     system catalog

### Submissions (user session: `ns_user` cookie or `Authorization: Bearer`)
- `POST /api/v1/submissions`                create a draft (body: name, author, description, donation_url, ai)
- `PUT /api/v1/submissions/{id}`            update/save a draft
- `POST /api/v1/submissions/{id}/upload`    request presigned URL (body: file_name, kind, system_id, size, mime_type)
- `POST /api/v1/submissions/{id}/submit`    submit for review (draft -> pending)
- `POST /api/v1/submissions/{id}/trash`     move to trash (only while draft/rejected)

### Admin (admin session: `ns_admin` cookie or `Authorization: Bearer`)
- `POST /api/v1/admin/login`                login (email, password); sets the `ns_admin` cookie (12 h)
- `GET /api/v1/admin/submissions`           list (filter ?status=pending|approved|rejected|created)
- `GET /api/v1/admin/submissions/{id}`      detail with files + logs
- `POST /api/v1/admin/submissions/{id}/approve`  approve (body: version)
- `POST /api/v1/admin/submissions/{id}/reject`   reject (deletes from R2)

## Public Scraping API

A credential-gated, read-only API for scraper front-ends, mounted under
`/api/v1/scrape`. The full user-facing guide (limits table, headers, examples,
best practices) is the web page `/app/docs`.

Every request requires **developer application credentials**. The end user's
personal API key is optional: with it the request uses that user's rank; without
it the request runs as a **guest**. Limits belong to people, not apps: a user is
counted by their account and a guest by their network address (IPv4, or IPv6
/64), so each guest of an app gets their own allowance.

| layer           | headers                                      | required |
|-----------------|----------------------------------------------|----------|
| developer app   | `X-Client-Id` + `X-Client-Secret`            | always   |
| end user        | `Authorization: Bearer <personal API key>`   | optional |

Endpoints:

- `GET /api/v1/scrape/games` resolves **one** game by `crc`/`md5`/`sha1`/`sha256`
  or `name`, within exactly one of `system_id`, `family` or `group` (+ optional
  `type`, `media`, `roms`, `softname`). The lookup is hash-first and falls back to
  the name (cleaned server-side: extension and `(region)`/`[tags]` stripped; the
  best candidate wins). Returns `{ system_id, matched_by, game }` (`game` is null
  when nothing matched; `system_id` is the game's real system). `rom_count` and
  `rom_name` are always returned, the ROM list only with `roms=true`. There is
  no daily limit; each lookup counts in today's usage stats.
- `GET /api/v1/scrape/account` current subject (user or guest), threads,
  `rate_per_minute` and today's usage (`quota.unlimited` is always true and
  `quota.daily_limit` always null)
- `GET /api/v1/scrape/status` live public status, no credentials: requests in
  the last minute, open threads of users and guests against their caps, and
  scrapers active in the last 5 minutes (cached 5 s, per instance)
- `GET /api/v1/scrape/systems`, `/families`, `/groups` catalog levels
- `GET /api/v1/scrape/popular` most scraped games, optional `system_id`/`limit`

Self-service credentials (authenticated with the user session):

- `GET|POST /api/v1/auth/developer/apps`, `DELETE /api/v1/auth/developer/apps/{id}`,
  `POST /api/v1/auth/developer/apps/{id}/rotate`
- `GET|POST /api/v1/auth/api-keys`, `DELETE /api/v1/auth/api-keys/{id}`

### Limits

An account's **threads** set all its limits, and there is **no daily limit**.
Threads come from the XP rank (levels 1-10 Novice = 2, then +2 per rank up to
levels 91-100 Legend = 20; guests 1, admins 20), plus an additive donor bonus
(supporter +8, monthly supporter +12) capped at the top rank (20). Each thread
is worth 60 requests per minute for a registered account and 30 for a guest.
A monthly supporter whose subscription ends stays a supporter.
`GET /api/v1/config` returns the current values.

| limit | value | when exceeded |
|-------|-------|---------------|
| simultaneous requests | threads | waits up to 2 s for a slot, then `429` + `Retry-After: 1` |
| requests per minute | threads x `SCRAPE_RPM_PER_THREAD` (60); a guest threads x `SCRAPE_GUEST_RPM_PER_THREAD` (30); bursts of about 5 s | `429` + exact `Retry-After`; a guest is held up to 3 s for its next token first |
| open requests, all registered users together | `SCRAPE_USER_OPEN_THREADS` (8192) | waits up to 2 s, then `503` + `Retry-After: 2` |
| open requests, all guests together | `SCRAPE_GUEST_OPEN_THREADS` (512) | waits up to 2 s, then `503` + `Retry-After: 2` |
| game lookups in flight, all accounts | `SCRAPE_MAX_INFLIGHT` (16) | `503` + `Retry-After: 2`, not counted in usage |
| game lookups in flight, all guests together | `SCRAPE_GUEST_MAX_INFLIGHT` (4) of those | waits up to 5 s, then `503` + `Retry-After: 2` |

Every scrape response reports the state in `X-RateLimit-Limit`,
`X-RateLimit-Remaining` and `X-Concurrency-Limit`. A client that uses
`X-Concurrency-Limit` workers and honors `Retry-After` resolves 1,000 games in
about 33 min as a guest (1 thread), 8 min as a Novice (2), 1 min 40 s as a
Novice supporter (10) and 50 s as a Legend (20).

XP comes from approved contributions (text field 10, image 50, video 100, SAP
image 20, new game +100; `POINTS_*`). Level = f(total XP) with
`XPForLevel(L) = round(L^2.5 * 3)`.

`docs/postman/neoassets-scraping.postman_collection.json` is a Postman
collection with functional tests plus guest/user rate-limit tests (run them with
the Collection Runner).

### Developer dashboard and debug mode

Each app accumulates lifetime usage counters (API calls, KO scraps, rate-limited,
over-quota from before the daily limit was removed, debug calls, last scrape) shown in the web developer page; they are
written in batches every 10 s. Counters are split per **version** when the
client sends `softname=<name>` (query param or `X-Software-Name` header);
otherwise the developer app name is used. Each app also gets a **debug
password** (owner-only, shown once) that unlocks debug overrides, capped at 100
calls/day per app and only honored when `ENABLE_DEBUG_MODE=true`:

- `X-Debug-Password: <password>` header enables debug mode (never a query
  parameter, so it doesn't end up in logs).
- `forcethreads=N` overrides the thread count for the request.
- `forceratelimit=1` forces a `429`.

Note: because R2 objects are public and the catalog is also browsable through
`/api/v1/metadata/*`, the rate limits are an anti-abuse contract rather than a
hard security boundary.

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

`.env.example` lists every variable with its default and explanation. The main
groups:

- **Database:** `DATABASE_URL` (required; the service does not bundle
  Postgres), `DB_MAX_OPEN_CONNS` (20), `DB_STATEMENT_TIMEOUT_SECONDS` (30).
- **Auth and R2:** `JWT_SECRET`, `R2_ACCOUNT_ID`, `R2_ACCESS_KEY`,
  `R2_SECRET_KEY`, `R2_BUCKET_NAME`, `R2_PUBLIC_BASE_URL`,
  `ADMIN_SEED_EMAIL`/`ADMIN_SEED_PASSWORD` (initial admin, seeded on startup).
- **Scraping API:** `SCRAPE_GUEST_THREADS` (1), `SCRAPE_ADMIN_THREADS` (20),
  `SCRAPE_SUPPORTER_BONUS_THREADS` (8), `SCRAPE_MONTHLY_SUPPORTER_BONUS_THREADS`
  (12), `SCRAPE_RPM_PER_THREAD` (60), `SCRAPE_GUEST_RPM_PER_THREAD` (30),
  `SCRAPE_USER_OPEN_THREADS` (8192, -1 off), `SCRAPE_GUEST_OPEN_THREADS` (512,
  -1 off), `SCRAPE_MAX_INFLIGHT` (16, -1 off), `SCRAPE_GUEST_MAX_INFLIGHT` (4,
  -1 off), `ENABLE_DEBUG_MODE` (false).
- **XP:** `POINTS_TEXT_METADATA` (10), `POINTS_IMAGE_METADATA` (50),
  `POINTS_VIDEO_METADATA` (100), `POINTS_SAP_IMAGE` (20), `POINTS_NEW_GAME`
  (100), `XP_SUPPORTER_BONUS_PCT` (25), `XP_MONTHLY_SUPPORTER_BONUS_PCT` (50).
- **Caching:** `CATALOG_CACHE_SECONDS` (60) for the systems, families, groups,
  languages and dashboard aggregates.
- **Network:** `CORS_ORIGINS`, `TRUST_PROXY_HEADERS`, `CDN_IP_RANGES`,
  `CDN_CLIENT_IP_HEADER` (client IP for the per-IP limiters, see
  `.env.example`).

A `0` (or unset) numeric value keeps the default.

## Development

```bash
cp .env.example .env   # fill in values
go mod tidy
go run ./cmd
```

## Docker

`docker-compose.yml` builds and runs the **API service** only. It connects to
an external PostgreSQL through `DATABASE_URL`; in production that is the
managed Postgres service on Dokploy's `dokploy-network`. Migrations run
automatically on startup.

```bash
docker compose --env-file .env up -d --build
```

The API listens on `127.0.0.1:${PORT:-8090}` (healthcheck `GET /health`). In
production it is reached only through Traefik and the CDN.

