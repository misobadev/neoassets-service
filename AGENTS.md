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
internal/models             request/response + DB structs (scrape.go = scraping DTOs)
internal/repository         SQL access (repository.go, user_repository.go,
                            metadata_repository.go, developer_repository.go)
internal/services           business logic (service.go, user_service.go,
                            metadata_service.go, scrape_service.go,
                            developer_service.go)
internal/handlers           HTTP handlers (scrape_handlers.go, developer_handlers.go)
internal/systems            embedded system catalog (+ systems.json)
pkg/auth                    JWT + password helpers, scrape credentials/middleware
pkg/r2                      Cloudflare R2 S3 client
migrations/*                sql-migrate files, run automatically at boot
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

- `GET /api/v1/config`                       public runtime config (threads,
  daily games per thread, thread cost curve and points) so the UI renders the
  current economy without hardcoded values.
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
- `GET /scrape/systems` (free), `GET /scrape/games` (consumes 1 quota unit),
  `GET /scrape/account` (free).
- Quota: the daily limit is **threads * `SCRAPE_DAILY_GAMES_PER_THREAD`** (default
  1000) game lookups per account. A registered user's threads come from their XP
  level rank (levels 1-10 -> 4, 91-100 -> 16; see `internal/progression`); guests
  get `SCRAPE_GUEST_THREADS` (2) and admins `SCRAPE_ADMIN_THREADS` (16). Guest
  quota is keyed by the **developer app owner**, so creating more apps does not
  multiply the limit. Threads also set concurrency. Per-minute rate limit: guest
  `SCRAPE_GUEST_RPM` (10), user `SCRAPE_USER_RPM` (60). In-memory limiters (single
  instance); daily counters live in `scrape_usage`.
- XP & ranks: approved contributions award XP (`POINTS_TEXT_METADATA` 10 /
  `POINTS_IMAGE_METADATA` 50 / `POINTS_VIDEO_METADATA` 100 / `POINTS_SAP_IMAGE` 20).
  Level is derived from total XP with `XPForLevel(L) = round(L^2.5 * 3)` (level 1
  = 0 XP, level 100 = 300,000). Threads are unlocked by rank, never purchased.
  Awards are idempotent per submission (a re-approval does not double-credit).
- Donors (parallel to XP): `users.donor_status` (none | supporter | monthly_supporter)
  adds an **additive** thread bonus on top of the level base
  (`SCRAPE_SUPPORTER_BONUS_THREADS` 2, `SCRAPE_MONTHLY_SUPPORTER_BONUS_THREADS`
  4), hard-capped at `progression.MaxThreads()` (16), plus an XP bonus
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
  claim flow (`POST /api/v1/auth/donor/claim` sends a code to the donation
  email, `.../claim/verify` links it). `users.donor_source` (`none|auto|admin`)
  protects admin overrides from recomputation. Ko-fi has no subscription-end
  event, so the background job downgrades auto monthly supporters whose last
  subscription payment is older than `DONOR_SUBSCRIPTION_GRACE_DAYS` (40);
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
  `group` on `/scrape/games` expand to their systems. A **virtual** system
  (`metadata_systems.virtual`, e.g. `arc`) owns no games and resolves to its
  group (when set) or family, so `system_id=arc` covers every arcade board
  without duplicating games. Seeded by the importer from
  `neoassets_systems.json` (`syncSystemMeta`).
- `game_scrape_stats` (migration `041`) counts scrapes per game; every
  successful `/scrape/games` resolution bumps it and the count is returned as
  `game.scrapes`. `GET /scrape/popular` ranks games by that counter (free).
- Media are public CDN URLs built with `Service.PublicURL`.
- Secrets: developer `client_secret` and user API keys are stored as SHA-256
  (`pkg/auth.HashToken`) with an indexed prefix; plaintext is returned once.
- Self-service management uses the user JWT:
  `/api/v1/auth/developer/apps*` and `/api/v1/auth/api-keys*`.
- Developer dashboard: `developer_apps` holds lifetime counters (`api_calls`,
  `ko_scraps`, `rate_limited`, `quota_exceeded`, `debug_calls`, `last_scrape_at`)
  shown in the web developer page (`GET /api/v1/auth/developer/apps`).
- Per-version stats live in `app_software_stats` (migration `040`). The client
  may send `softname` (query param or `X-Software-Name` header) to distinguish
  versions; absent, the app name is used. Quota/rate limits stay per app/user.
- Debug mode: each app has an owner-only `debug_password`, stored hashed
  (migration `039` column holds the hash). Passing `X-Debug-Password` enables
  `forcerequestok` / `forcethreads` / `forceratelimit`, capped at 100/day in
  `debug_usage`. It never bypasses auth and is only honored when
  `ENABLE_DEBUG_MODE=true`.
- Accounts must verify their email before using the API: registration issues no
  token, login checks verification before the password, authenticated routes and
  scrape user credentials enforce it (unless `SKIP_EMAIL_VERIFICATION=true` for
  local dev). Changing email resets verification.
- Because R2 is public and `/api/v1/metadata/*` stays open (per-IP rate limited),
  quota is an anti-abuse contract, not a hard boundary.

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

## Cloudflare R2

- Signing: `pkg/r2` uses the S3-compatible API against
  `https://{ACCOUNT_ID}.r2.cloudflarestorage.com`.
- Objects are public through the `R2_PUBLIC_BASE_URL` custom domain; no signing
  needed to read them.
- **Bucket CORS** must allow `PUT` + request headers, otherwise the browser
  preflight fails during direct uploads. Configure it on the bucket, not in code.
- Object keys: `packs/{packId}/backgrounds/{system}.webp|gif`,
  `packs/{packId}/preview.webp`, `packs/{packId}/theme.json`.

## Testing / build

```bash
go build ./...
go vet ./...
go test ./...
docker compose --env-file .env up -d --build
```

## Env (.env)

See `README.md` and `.env.example`. `R2_*` tools/certs only need object read +
write; bucket-level ops (e.g. setting CORS via S3) require an admin-permission
token (the current one returns 403 on `PutBucketCors`).
