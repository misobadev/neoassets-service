# NeoAssets Service - Agent Context

**Public service, to be open-sourced.** This is the backend of the NeoAssets
scraping system: it will be exposed publicly and released as open source. It
serves the metadata catalog, media URLs and the public scraping API. The web 
UI lives in `neoassets-web`.

Go + PostgreSQL + Cloudflare R2 API for NeoAssets system art packs. The DB is
the single source of truth. The public system art pack list is served from 
the DB via `GET /api/v1/packs`.

## Layout

```
cmd/main.go                 config, DB, migration runner, router
cmd/app.go                  buildApp: wires repository, services, handlers, router
internal/models             request/response + DB structs (scrape.go = scraping DTOs)
internal/repository         SQL access (repository.go, user_repository.go,
                            metadata_repository.go, developer_repository.go)
internal/services           business logic (service.go, user_service.go,
                            metadata_service.go, scrape_service.go,
                            developer_service.go)
internal/handlers           HTTP handlers (scrape_handlers.go, developer_handlers.go)
internal/httpx              shared HTTP helpers: JSON/Error, Decode, DecodeOptional, PathUUID
internal/economy            rewards/scrape economy (threads, rates, XP points) as a value
internal/testdb             per-package migrated test databases
internal/systems            embedded system catalog (+ systems.json)
pkg/auth                    JWT + password helpers, scrape credentials/middleware
pkg/r2                      Cloudflare R2 S3 client (pkg/r2/r2test: in-memory fake)
migrations/*                golang-migrate files, run automatically at boot
```

## Submission state machine

| status     | meaning                                        |
|------------|------------------------------------------------|
| `created`  | draft (default on create), only editable state |
| `pending`  | submitted for review, shows in admin queue     |
| `approved` | published; the only state listed in `/packs`   |
| `rejected` | rejected; files deleted from R2                |
| `trashed`  | user-deleted; hidden from user list, files deleted |

`approved` rows are read-only: to change a published pack, users create a
**contribution** (`submissions.contribution=true`, same `pack_id`) which is a
separate pending row. On approve it becomes the new latest approved revision of
the pack; the contributor's username is added to the pack's `contributors`.

Valid column values are enforced by the `submissions_status_check` CHECK
constraint (migrations `008` + `009`). Array of statuses differs from older docs.

## Endpoints (relevant)

- `GET /api/v1/config`                       public runtime config (ranks and
  threads, `rpm_per_thread`, `guest_rpm_per_thread`, `daily_unlimited`, donor
  tiers, points) so
  the UI (guide, API docs) renders the current economy without hardcoded values.
- `GET /api/v1/metadata/genres`               canonical genre catalog
  (`genres` table, migration `061`) used by the web metadata forms. Submissions
  must send one of these names; the service validates membership but never
  normalizes (normalization lives in the importer).
- `GET /api/v1/metadata/regions`              canonical region catalog
  (`regions` table, migration `062`), in priority order (World, USA, Europe,
  Japan, Spain, France, Germany, Italy, Korea, China). A game has no single
  region: its per-region name/release live in `game_regions` and its cover/logo
  in `media.region`. The detail API resolves the primary name/release/cover by
  region priority and also returns a `regions` array with the text and media per
  region. Region is submitted only for name, release, logo and cover.
- `GET /api/v1/packs?sort=&limit=&offset=`   `services.ListedApprovedPacks()` ->
  `{"themes":[...],"total":N}`, sorted by `downloads` (default), `name` or
  `created`, paginated. Only `approved` submissions are served. Each pack
  includes `downloads`, `contributions` (pending/draft count) and `contributors`.
- `GET /api/v1/packs/{packID}`                public detail: every published
  file with public URLs, pending/draft contributions and contributor names.
  Pure read, does NOT bump the download counter.
- `GET /api/v1/packs/{packID}/download`       same payload but bumps the pack
  download counter (`pack_scrape_stats`); the install/scrape action.
- `POST /api/v1/submissions`                  create (status pending). With
  `contribution=true` + `pack_id`, it creates a contribution to an approved
  pack (name/author/donation/ai copied from the pack; description optional).
  A contribution never changes them: saving a draft keeps them, and approving
  it re-copies them from the pack. The pack's `submitted_by` stays the user who
  submitted the pack itself. So a pack's identity can't change after its first
  approval (there is no owner/admin edit path yet). A new pack's `donation_url`
  must be an https web address (`ko-fi.com/x` and `http://` are upgraded to
  https); pack responses upgrade a stored value the same way and omit one that
  can't be.
- `PUT /api/v1/submissions/{id}`              save draft (only when created/rejected)
- `POST /api/v1/submissions/{id}/upload`      presign + `AddFile` + log
- `POST /api/v1/submissions/{id}/submit`      created/rejected -> pending
  (new packs require files; contributions may be description-only)
- `POST /api/v1/submissions/{id}/trash`       moves to trashed, deletes files
  (only while created/rejected; not pending or approved)
- Admin approve/reject under `/api/v1/admin/...`

There is no `/api/v1/manifest` and no boot-time R2 manifest publish anymore.

## Public scraping API

Mounted at `/api/v1/scrape`. Developer app credentials are **always** required;
the user credential is optional and falls back to guest mode.

- Headers: `X-Client-Id` + `X-Client-Secret` (developer app) and
  `Authorization: Bearer <personal API key or user JWT>` (user, optional).
- `GET /scrape/systems`, `GET /scrape/games`, `GET /scrape/account`; the
  public `GET /scrape/status` (no credentials, `ScrapeService.Status`, cached
  5 s) reports requests in the last minute, open threads per kind against their
  caps and scrapers active in the last 5 minutes (`scrape_live.go`, in memory).
- **No daily limit.** Each game lookup counts in `scrape_usage` (uncapped,
  `RecordUsage`) only for stats; `/scrape/account` returns `quota` with
  `unlimited: true`, `daily_limit: null`, `used` and `resets_at`, plus
  `rate_per_minute`. A registered user's threads come from their XP level rank
  (levels 1-10 -> 2, then +2 per rank to 91-100 -> 20; see
  `internal/progression`); guests get `SCRAPE_GUEST_THREADS` (1) and admins
  `SCRAPE_ADMIN_THREADS` (20).
- Threads are **concurrent requests** (`internal/services/scrape_admission.go`):
  `ScrapeAdmission` first takes one of the open-thread slots shared by all
  registered users (`SCRAPE_USER_OPEN_THREADS`, 8192) or all guests
  (`SCRAPE_GUEST_OPEN_THREADS`, 512), 503 + `Retry-After: 2` when full, then
  one of the subject's `threads` slots for every `/scrape/*` request, waiting
  up to 2 s, then 429 + `Retry-After: 1`; responses carry
  `X-Concurrency-Limit`. `/scrape/games` also takes a slot of the global cap
  `SCRAPE_MAX_INFLIGHT` (default 16, -1 off) or gets 503 + `Retry-After: 2`,
  so the website keeps pool connections. Guests first take one of `SCRAPE_GUEST_MAX_INFLIGHT` (default
  4) slots shared by all guests, waiting up to 5 s, so anonymous load never
  takes the server from users. Per-minute rate limit: **threads *
  `SCRAPE_RPM_PER_THREAD`** (default 60, `economy.RPMPerThread`) for
  registered users and threads * `SCRAPE_GUEST_RPM_PER_THREAD` (default 30) for
  guests (`subjectRPM`); a guest over it is held up to 3 s for its token
  (`allowSubjectPaced`) instead of refused. Bursts are about 5 s
  (`rateBurst`), never the full minute. A rate refusal carries the exact
  `Retry-After` (`take1` reserves without waiting and cancels when refused, so
  a refusal costs no token); successful responses carry `X-RateLimit-Limit`
  and `X-RateLimit-Remaining`. The user docs live in the web (`/app/docs`,
  `ApiDocsPage.tsx`): keep them in sync with any limit change.
  Threads, rate and usage are all keyed by the user for registered accounts
  and by the caller's network address for guests, stored hashed (a generous per-app, per-address pre-check
  bounds DB lookups from bogus credentials). In-memory limiters (single
  instance); daily usage counters live in `scrape_usage`.
- XP & ranks: approved contributions award XP (`POINTS_TEXT_METADATA` 10 /
  `POINTS_IMAGE_METADATA` 50 / `POINTS_VIDEO_METADATA` 100 / `POINTS_SAP_IMAGE` 20).
  Level is derived from total XP with `XPForLevel(L) = round(L^2.5 * 3)` (level 1
  = 0 XP, level 100 = 300,000). Threads are unlocked by rank, never purchased.
  Awards are idempotent per submission (a re-approval does not double-credit).
- Donors (parallel to XP): `users.donor_status` (none | supporter | monthly_supporter)
  adds an **additive** thread bonus on top of the level base
  (`SCRAPE_SUPPORTER_BONUS_THREADS` 8, `SCRAPE_MONTHLY_SUPPORTER_BONUS_THREADS`
  12), hard-capped at `progression.MaxThreads()` (20), plus an XP bonus
  (`XP_SUPPORTER_BONUS_PCT` 25, `XP_MONTHLY_SUPPORTER_BONUS_PCT` 50). Donations
  never grant rank. Set via admin `PUT /api/v1/admin/users/{id}/donor`.
- Donation integrations (Ko-fi, Patreon): `POST /api/v1/webhooks/kofi` ingests
  Ko-fi payments (form field `data` = JSON, verified with
  `KOFI_VERIFICATION_TOKEN`, constant time). `POST /api/v1/webhooks/patreon`
  ingests Patreon member webhooks (`members:create/update/delete`), verified via
  the `X-Patreon-Signature` HMAC (MD5 or SHA-256) with `PATREON_WEBHOOK_SECRET`.
  `donation_events` is the append-only idempotent log (unique
  `platform`+`external_id`, plus an `active` flag for Patreon's explicit
  membership state); `donor_claims` holds a hashed email-claim code. Matching is
  by email: a webhook for a registered email applies the status immediately;
  unmatched events stay pending and are linked on email verification or via the
  claim flow (`POST /api/v1/auth/donor/claim` emails an 8-digit code to the
  donation email if it has unclaimed donations — the answer is the same either
  way — and `.../claim/verify` links them). A code allows 5 checks, a new one
  can be requested once a minute, and both routes are rate limited per account. `users.donor_source` (`none|auto|admin`)
  protects admin overrides from recomputation. Ko-fi has no subscription-end
  event, so the background job downgrades auto monthly supporters whose last
  subscription payment is older than `DONOR_SUBSCRIPTION_GRACE_DAYS` (40) to
  supporter (`RecomputeDonorStatus`: any payment ever made, one-time or a
  charged subscription, keeps the supporter status);
  Patreon ends arrive as `members:delete` / inactive `patron_status`.
- Debug mode is gated by `ENABLE_DEBUG_MODE` (off by default) and only accepts
  `X-Debug-Password` (header, never a query param so it is not logged). The
  password is stored hashed and returned plaintext only on create/rotate.
- `GET /scrape/games` requires exactly one of `system_id`, `family` or `group`;
  a ROM hash (`crc`/`md5`/`sha1`/`sha256`) or `name` selects **one** game. Hash
  wins; on a hash miss with a `name`, it falls back to the name search and picks
  the best candidate (`bestMatch`). `name` is cleaned server-side (extension and
  `(region)`/`[tags]` stripped) so filenames work. Response is
  `{ system_id, matched_by, game }` (`game` null when nothing matched; `system_id`
  is the system the game actually belongs to, so a family/group query reports the
  resolved board); there is no `game_id` selector (the id is internal, only
  returned). `rom_count` and `rom_name` (the primary ROM file name, e.g.
  `sfa3.zip` for an arcade set) are always returned; the full `roms` list only
  with `roms=true`.
- System grouping: `metadata_systems.family` is the broad category (`arcade`,
  `console`, `computer`, `handheld`, `virtual`) and `metadata_systems.system_group`
  is the emulator sub-set (`mame-fbneo`, `flycast`, `supermodel`, `dolphin`).
  `GET /scrape/families` and `GET /scrape/groups` list them (free); `family`/
  `group` on `/scrape/games` expand to their systems. A **virtual** parent
  (`metadata_systems.virtual`, e.g. `arcade`) owns no games and resolves to its
  group (when set) or family, so `system_id=arcade` covers every arcade board
  without duplicating games. The metadata browse lists it too, and opening it
  lists the whole family (`ListGamesBySystem`/`SearchGames` resolve a virtual
  system to its family; the card stats are the family sum). Seeded by the
  importer from `neoassets_systems.json` (`syncSystemMeta`).
- `game_scrape_stats` (migration `041`) counts scrapes per game; every
  successful `/scrape/games` resolution bumps it and the count is returned as
  `game.scrapes`. `GET /scrape/popular` ranks games by that counter (free).
- Media are public CDN URLs built with `Service.PublicURL`.
- Secrets: developer `client_secret` and user API keys are stored as SHA-256
  (`pkg/auth.HashToken`) with an indexed prefix; plaintext is returned once.
- Self-service management uses the user JWT:
  `/api/v1/auth/developer/apps*` and `/api/v1/auth/api-keys*`.
- Developer dashboard: `developer_apps` holds lifetime counters (`api_calls`,
  `ko_scraps`, `rate_limited`, `quota_exceeded` (historical: no new ones since
  the daily limit was removed), `debug_calls`, `last_scrape_at`)
  shown in the web developer page (`GET /api/v1/auth/developer/apps`).
- Per-version stats live in `app_software_stats` (migration `040`). The client
  may send `softname` (query param or `X-Software-Name` header) to distinguish
  versions; absent, the app name is used. Rate limits stay per user/guest.
  An app keeps at most 50 software names (later ones count under the app name).
  Every outcome (ok, not_found, rate_limited) is counted in
  memory and written every 10 s (`FlushOutcomes`, also on shutdown and before
  the developer pages that show the counters), so a busy app's threads don't
  each write the same `developer_apps`/`app_software_stats` rows.
- A family/group selector searches all its systems in one query (up to 25
  candidates per system), and a search matches on at most 8 words. A hash
  lookup is also one query for all the selector's systems.
- Name search matches each token with `LIKE '%token%'` on `games.search_norm`
  (migration `076`): the normalized display name, short name and every
  regional name joined by `' | '`, kept current by triggers on `games` and
  `game_regions` (so importer writes are covered too), with a trigram index.
  Never search with `regexp_replace`/`f_unaccent` per row again.
- Browse/search pages are deferred joins (`pagedGamesQuery`): the page's ids
  are chosen first and only those rows get the cover/scrape subqueries.
- Debug mode: each app has an owner-only `debug_password`, stored hashed
  (migration `039` column holds the hash). Passing `X-Debug-Password` enables
  `forcethreads` / `forceratelimit`, capped at 100/day in
  `debug_usage`. It never bypasses auth and is only honored when
  `ENABLE_DEBUG_MODE=true`.
- Accounts must verify their email before using the API: registration issues no
  token, login checks verification before the password, authenticated routes and
  scrape user credentials enforce it (unless `SKIP_EMAIL_VERIFICATION=true` for
  local dev). Changing email resets verification.
- Browser sessions are **httpOnly cookies** (`ns_user`, `ns_admin`;
  `SameSite=Lax`, `Secure` over HTTPS) so the JWT is never exposed to JavaScript.
  `Authorization: Bearer` is still accepted for non-browser API clients. Login
  and `/admin/login` set the cookies and no longer return the raw JWT;
  `POST /api/v1/logout` clears them. JWTs carry a `ver` (`token_version`) claim:
  changing or resetting the password bumps `users.token_version` and revokes every
  outstanding token immediately, admin tokens included. Admin tokens (and the
  `ns_admin` cookie) last 12 h (`auth.AdminTokenTTL`), whichever login issued them. Email-verification and password-reset tokens are
  stored as SHA-256 hashes, never plaintext.
- **Client IP** (`handlers.ClientIP`), used by every per-IP limiter: starting at
  the peer, X-Forwarded-For is followed **right to left** past private/loopback
  hops (Traefik, a tunnel) to the first outside address (entries further left
  are client-supplied). The CDN's client-address header is used when that
  address is in the CDN's ranges or when the request only passed through
  private hops; any other public address is the client. This assumes the
  origin is reachable only through the CDN (a NAT that shows outside clients
  as a private address would let them pick their key) and that Traefik appends
  to X-Forwarded-For (keep `forwardedHeaders.notAppendXForwardedFor` off).
  Hops in 100.64.0.0/10 (CGNAT, e.g. an overlay network) don't count as
  private. `TRUST_PROXY_HEADERS=always` treats the direct peer as our proxy.
- **CDN:** Cloudflare by default (`CF-Connecting-IP`, ranges embedded in
  `security.go`; update them if Cloudflare announces a change). To move to
  another CDN set **both** `CDN_IP_RANGES` (its published edge CIDRs) and
  `CDN_CLIENT_IP_HEADER` (a header the CDN sets on every request, overwriting
  any value from the client) — startup refuses one without the other, and
  refuses `X-Forwarded-For`/`Forwarded`. Without them, every user of one edge
  node of the new CDN would share a bucket. With nothing in front of Traefik
  (DNS only) set `CDN_IP_RANGES=none`; don't use `none` behind a tunnel or
  ingress, where every hop is private and all clients would share one bucket.
- **Limiter keys** are per IPv4 address or IPv6 /64 (`ClientKey`). Limiters
  keep two 10-minute generations of buckets with a key cap, so memory is
  bounded and nothing scans the map per request.
- Because R2 is public and `/api/v1/metadata/*` stays open (per-IP rate limited),
  the rate limits are an anti-abuse contract, not a hard boundary.

## Rules derived from history

- `CreateSubmission` inserts `created` (not pending).
- `ListUserSubmissions` MUST include `files` (the web renders thumbnails from
  `object_key`). Do not strip it.
- Uploads are allowed while a submission is `created` or `pending`.
- `submit` requires at least one registered file; otherwise it errors.
- Editing a submission is only possible while `created` (`UpdateSubmission` and
  `SubmitSubmission` both guard on it). A rejected pack is read-only and cannot
  be re-edited/resubmitted (by design, for now).
- `ListSubmissionsByUser` filters out `trashed`; a user never sees their own
  trashed packs in the list. Trashing deletes the pack's files from R2 and only
  works while the submission is a draft (`created`) or rejected (`rejected`) -
  not while it is pending review or once approved.
- Metadata contributions carry a `kind`: `edit` (change an existing game or
  system) or `new_game` (create a game from scratch; requires system + name +
  type). Approving a `new_game` creates the `games` row and then applies its
  payload and media, and awards an extra `POINTS_NEW_GAME` (100) bonus.
- Metadata `description` submissions are capped at `MaxDescriptionLength` (1500
  chars), enforced in `CreateMetadataSubmission` and in the web textarea. The
  translate worker (`@cf/meta/m2m100-1.2b`) caps its output and drops the last
  sentence of multi-sentence input, so it translates **sentence by sentence**
  (its native unit) and concatenates; `POST /api/v1/admin/metadata/games/{id}/
  retranslate` (and `/systems/{id}/retranslate`) regenerate translations from the
  stored English description. The importer's bulk path uses `translate_nllb.py`
  instead.

## Code conventions

- **Handlers** decode bodies with `httpx.Decode` (or `httpx.DecodeOptional`
  when the body may be omitted) and parse path ids with `httpx.PathUUID`; they
  report service errors with `writeClientError`, which keeps business messages
  but never returns storage errors verbatim (a missing row becomes
  `not found`, database failures a logged 500).
- **Errors**: services return `apperr` errors for anything the client should
  see (`apperr.Invalid`, `NotFound`, `Conflict`, `Forbidden`,
  `Unauthorized`, `Unavailable`); the message is shown as-is. Use
  `fmt.Errorf("...: %w", err)` only for internal failures, which
  `writeClientError` turns into a logged 500 when they come from storage.
  Handlers still pick the same HTTP status per endpoint as before.
- **Transactions**: multi-write operations run in `repo.WithTx(ctx, fn)`; the
  `*Repository` passed to `fn` shares one transaction (nested calls join it).
  `ApplyMetadataSubmission` and the pack approval writes use it. R2 moves stay
  outside the transaction.
- **Economy**: `economy.Config` is built once in `cmd` from the environment
  (`economyFromConfig`) and given to the repository (`repository.WithEconomy`);
  services read it with `repo.Economy()`. There are no economy globals.
- **Context**: every repository method that touches the database takes
  `ctx context.Context` first and uses the `*Context` database/sql calls;
  services pass their `ctx` through and handlers pass `r.Context()`. Reads are
  cancelled when the client disconnects; `handlers.DetachWrites` keeps write
  requests (anything but GET/HEAD/OPTIONS) running to the end, within the
  request deadline, so a review or upload is never left half applied.
  Background jobs use the jobs context from `main`.
- **Translator**: the description translator is built from
  `TRANSLATE_WORKER_URL`/`TRANSLATE_WORKER_TOKEN` in `cmd` and injected with
  `services.WithTranslator`.
- **Sitemaps**: `internal/sitemap` serves `/sitemap.xml` (an index),
  `/sitemaps/pages.xml` (static pages, systems, approved packs) and
  `/sitemaps/games-{n}.xml` (40,000 games each). URLs point at
  `FRONTEND_URL`; the web's robots.txt references this sitemap.
- **Constants**: metadata kinds (`models.MetadataKindEdit`,
  `models.MetadataKindNewGame`) and submission log actions
  (`models.LogAction*`) are constants; don't use the string literals.

## Reviews and XP (concurrency)

- Approve and reject (packs and metadata) run under
  `repository.WithReviewLock`: a PostgreSQL advisory lock per submission id,
  taken with `pg_try_advisory_lock` on a dedicated connection. A second
  concurrent review of the same submission gets
  `submission is already being reviewed` (400) instead of applying twice;
  after the first finishes, the usual `submission is not pending` applies.
- `AwardXP` grants at most one ledger entry per submission: inside its
  transaction it takes `pg_advisory_xact_lock` on the submission id and
  re-checks the ledger before inserting, so overlapping approvals never
  double-credit. (No unique index is added, so existing data can never make
  a migration fail.)

## Cloudflare R2

- Signing: `pkg/r2` uses the S3-compatible API against
  `https://{ACCOUNT_ID}.r2.cloudflarestorage.com`.
- Objects are public through the `R2_PUBLIC_BASE_URL` custom domain; no signing
  needed to read them.
- **Bucket CORS** must allow `PUT` + request headers, otherwise the browser
  preflight fails during direct uploads. Configure it on the bucket, not in code.
- **Upload Content-Type:** a presigned upload is signed with the client's
  `mime_type` only if it fits the kind (`uploadContentType`: images
  `image/png|jpeg|webp|gif`, videos `video/*`, themes `application/json`, or
  `application/octet-stream`); anything else (HTML, SVG, text) is refused,
  because uploads are served from the public bucket as stored. Approval writes
  every published pack object itself with a server-chosen type (WebP, GIF, or a
  theme checked with `json.Valid`, max 1 MiB) — never `CopyObject`, which keeps
  the uploader's metadata.
- Object keys: `packs/{packId}/backgrounds/{system}.webp|gif`,
  `packs/{packId}/theme.json`. A pack has no dedicated preview image; its public
  thumbnail is the background of a popular system (snes, ps1, gba, genesis,
  falling back to the Atari 2600).
- Images are normalized **on approval** by the backend (`video.NormalizeImage`):
  metadata fanart is center-cropped to 1920x1080, covers/logos are capped at
  1024px and other image kinds at 1920px; SAP backgrounds are center-cropped to
  1024x1024 and previews/logos capped at 1024px. Animated GIF backgrounds are
  kept as-is to preserve animation; metadata images are flattened to WebP. The
  client uploads the original file, so a bad client conversion can never publish
  a non-WebP asset. Staging and replaced canonical objects are deleted on
  approval.

## Testing / build

```bash
go build ./...
go vet ./...
go test ./...
docker compose --env-file .env up -d --build
```

Database-backed tests use `internal/testdb.Open(t, "<name>")`: with
`NEOASSETS_TEST_DATABASE_URL` set (its database name must contain `test`) each
caller gets its own sibling database `<base>_<name>`, reset and migrated once
per test process; without the variable those tests are skipped. Point it at a
throwaway server, e.g.
`docker run -d -e POSTGRES_PASSWORD=test -e POSTGRES_DB=neoassets_test -p 127.0.0.1:55432:5432 postgres:16-alpine`.

`cmd/http_golden_test.go` pins the HTTP API: it runs the production wiring
(`buildApp`) over a test database and an in-memory bucket (`pkg/r2/r2test`)
and compares every response with `cmd/testdata/http/*.golden`. A refactor must
leave them unchanged; after an intended API change run
`go test ./cmd -run HTTP -update` and review the diff. The approve path needs
ffmpeg with libwebp (CI installs it) and is skipped without it.

## Env (.env)

`DB_MAX_OPEN_CONNS` (default 20) bounds the connection pool and
`DB_STATEMENT_TIMEOUT_SECONDS` (default 30) is sent to Postgres as
`statement_timeout`, so a slow query stops even after its request has gone;
migrations run on a separate connection without it (behind PgBouncer, set it on
the role and use 0). Search and scrape queries take the request context.
`CATALOG_CACHE_SECONDS` (default 60, 0 disables) caches the catalog aggregates
in memory (systems, families, groups, languages, the public dashboard);
approvals and pack deletions clear them, catalog imports show up within the
TTL.

See `README.md` and `.env.example`. `R2_*` tools/certs only need object read +
write; bucket-level ops (e.g. setting CORS via S3) require an admin-permission
token (the current one returns 403 on `PutBucketCors`).
