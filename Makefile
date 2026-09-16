# Regenerate the embedded system catalog from the neostation-frontend repo.
.PHONY: sync
sync:
	go run ./cmd/sync-systems

# Run the service locally.
.PHONY: run
run:
	go run ./cmd

# Database backup/restore. The DB runs in the compose Postgres container.
DB_CONTAINER ?= neoassets-postgres
DB_USER      ?= neoassets_user
DB_NAME      ?= neoassets
BACKUP_DIR   ?= $(CURDIR)/../backups

# Dump the full database (custom format + plain SQL gzip) with a timestamp.
.PHONY: backup
backup:
	@mkdir -p $(BACKUP_DIR)
	@ts=$$(date +%Y%m%d_%H%M%S); \
	dump="$(BACKUP_DIR)/$(DB_NAME)_$$ts.dump"; \
	sql="$(BACKUP_DIR)/$(DB_NAME)_$$ts.sql.gz"; \
	docker exec $(DB_CONTAINER) pg_dump -U $(DB_USER) -d $(DB_NAME) -Fc > $$dump; \
	docker exec $(DB_CONTAINER) pg_dump -U $(DB_USER) -d $(DB_NAME) | gzip > $$sql; \
	ls -lh $$dump $$sql; \
	shasum -a 256 $$dump $$sql

# Restore a custom-format dump into a NEW database (never the live one).
# Usage: make restore FILE=/path/to.dump [TARGET_DB=neoassets_restore]
.PHONY: restore
restore:
	@test -n "$(FILE)" || { echo "usage: make restore FILE=/path/to.dump [TARGET_DB=...]"; exit 1; }
	@target="$${TARGET_DB:-$(DB_NAME)_restore}"; \
	docker exec $(DB_CONTAINER) psql -U $(DB_USER) -d postgres -c "DROP DATABASE IF EXISTS $$target" >/dev/null; \
	docker exec $(DB_CONTAINER) psql -U $(DB_USER) -d postgres -c "CREATE DATABASE $$target" >/dev/null; \
	docker exec -i $(DB_CONTAINER) pg_restore -U $(DB_USER) -d $$target --no-owner < $(FILE); \
	echo "restored $(FILE) -> $$target"
