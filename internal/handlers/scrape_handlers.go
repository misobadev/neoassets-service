package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"neoassets/internal/models"
	"neoassets/internal/services"
	"neoassets/pkg/auth"
)

// scrapeSubject returns the authenticated scrape subject from the request
// context (set by auth.ScrapeMiddleware).
func scrapeSubject(r *http.Request) (*auth.ScrapeSubject, bool) {
	return auth.ScrapeSubjectFromContext(r.Context())
}

// ListScrapeSystems returns the metadata system catalog for scraping clients.

func (h *Handler) ListScrapeSystems(w http.ResponseWriter, r *http.Request) {
	list, err := h.scrapeSvc.ListSystems(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list systems")
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(r.Context(), subject, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"systems": list})
}

// ScrapeGames resolves a game by id, ROM hash or name and returns its full
// metadata plus media URLs. Each call counts in today's usage stats.
func (h *Handler) ScrapeGames(w http.ResponseWriter, r *http.Request) {
	subject, ok := scrapeSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	query, err := parseScrapeGameQuery(r)
	if err != nil {
		writeClientError(w, http.StatusBadRequest, err)
		return
	}

	// Count the lookup in today's usage stats. There is no daily limit, so a
	// failed write is logged and never blocks the lookup.
	if _, err := h.scrapeSvc.RecordUsage(r.Context(), subject); err != nil {
		log.Warn().Err(err).Msg("scrape usage not recorded")
	}

	game, matchedBy, err := h.scrapeSvc.FindGames(r.Context(), query)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to resolve game", err)
		return
	}
	if game == nil {
		h.scrapeSvc.RecordOutcome(r.Context(), subject, "not_found")
	} else {
		h.scrapeSvc.RecordOutcome(r.Context(), subject, "ok")
	}
	// With a family selector, report the system the game actually belongs to.
	systemID := query.SystemID
	if game != nil {
		systemID = game.SystemID
	}
	writeJSON(w, http.StatusOK, models.ScrapeGamesResponse{
		SystemID:  systemID,
		MatchedBy: matchedBy,
		Game:      game,
	})
}

// ListScrapeFamilies returns the system families (e.g. "arcade") with their
// system and game counts.
func (h *Handler) ListScrapeFamilies(w http.ResponseWriter, r *http.Request) {
	list, err := h.scrapeSvc.ListFamilies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list families")
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(r.Context(), subject, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"families": list})
}

// ListScrapeGroups returns the system groups (e.g. "mame-fbneo") with their
// system and game counts.
func (h *Handler) ListScrapeGroups(w http.ResponseWriter, r *http.Request) {
	list, err := h.scrapeSvc.ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(r.Context(), subject, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": list})
}

// ListScrapePopular returns the most scraped games.
func (h *Handler) ListScrapePopular(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	systemID := strings.TrimSpace(r.URL.Query().Get("system_id"))
	list, err := h.scrapeSvc.PopularGames(r.Context(), systemID, limit)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to load popular games", err)
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(r.Context(), subject, "ok")
	}
	writeJSON(w, http.StatusOK, models.ScrapePopularResponse{Games: list})
}

// ScrapeAccount returns the current scrape subject, its limits and today's
// usage.
func (h *Handler) ScrapeAccount(w http.ResponseWriter, r *http.Request) {
	subject, ok := scrapeSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	acct, err := h.scrapeSvc.Account(r.Context(), subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}
	h.scrapeSvc.RecordOutcome(r.Context(), subject, "ok")
	writeJSON(w, http.StatusOK, acct)
}

// parseScrapeGameQuery reads and validates the /games selectors.
func parseScrapeGameQuery(r *http.Request) (models.ScrapeGameQuery, error) {
	q := r.URL.Query()

	var media []string
	if raw := q.Get("media"); raw != "" {
		for _, k := range strings.Split(raw, ",") {
			if k = strings.TrimSpace(k); k != "" {
				media = append(media, k)
			}
		}
	}

	query := models.ScrapeGameQuery{
		CRC:         strings.TrimSpace(q.Get("crc")),
		MD5:         strings.TrimSpace(q.Get("md5")),
		SHA1:        strings.TrimSpace(q.Get("sha1")),
		SHA256:      strings.TrimSpace(q.Get("sha256")),
		Name:        strings.TrimSpace(q.Get("name")),
		Package:     strings.TrimSpace(q.Get("package")),
		SystemID:    strings.TrimSpace(q.Get("system_id")),
		Family:      strings.TrimSpace(q.Get("family")),
		Group:       strings.TrimSpace(q.Get("group")),
		Type:        strings.TrimSpace(q.Get("type")),
		Media:       media,
		IncludeRoms: q.Get("roms") == "1" || strings.EqualFold(q.Get("roms"), "true"),
	}

	set := 0
	for _, v := range []string{query.SystemID, query.Family, query.Group} {
		if v != "" {
			set++
		}
	}
	if set == 0 {
		return query, fmt.Errorf("system_id, family or group is required")
	}
	if set > 1 {
		return query, fmt.Errorf("system_id, family and group are mutually exclusive")
	}
	if query.CRC == "" && query.MD5 == "" && query.SHA1 == "" && query.SHA256 == "" && query.Name == "" && query.Package == "" {
		return query, fmt.Errorf("a selector is required: a ROM hash, name or package")
	}
	if query.Package != "" && !models.ValidAndroidPackage(query.Package) {
		return query, fmt.Errorf("invalid package %q", query.Package)
	}
	if query.Type != "" && !models.ValidGameType(query.Type) {
		return query, fmt.Errorf("invalid type %q (%s)", query.Type, strings.Join(models.GameTypes, ", "))
	}
	return query, nil
}

// ScrapeAdmission holds one of the subject's thread slots for the whole
// request: an account's threads are its concurrent requests. When every slot
// stays busy for the admission wait, the request is refused with 429 and a
// Retry-After so the client backs off instead of piling up more requests.
func (h *Handler) ScrapeAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject, ok := scrapeSubject(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-Concurrency-Limit", strconv.Itoa(subject.Threads))
		if subject.RateLimit > 0 {
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(subject.RateLimit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(subject.RateRemaining))
		}
		release, err := h.scrapeSvc.AcquireSubject(r.Context(), subject)
		if errors.Is(err, services.ErrServerBusy) {
			h.scrapeSvc.RecordOutcome(r.Context(), subject, "rate_limited")
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusServiceUnavailable, "server busy, retry shortly")
			return
		}
		if err != nil {
			h.scrapeSvc.RecordOutcome(r.Context(), subject, "rate_limited")
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests,
				fmt.Sprintf("too many concurrent requests: this account allows %d at once", subject.Threads))
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

// ScrapeGlobalAdmission caps the game lookups running at once across every
// account, so the database always has room for the website. A request that
// can't get a slot within the admission wait gets 503 with a Retry-After and
// is not counted in the usage stats.
func (h *Handler) ScrapeGlobalAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject, hasSubject := scrapeSubject(r)
		release, err := h.scrapeSvc.AcquireGlobal(r.Context(), subject)
		if err != nil {
			if hasSubject {
				h.scrapeSvc.RecordOutcome(r.Context(), subject, "rate_limited")
			}
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusServiceUnavailable, "server busy, retry shortly")
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

// ScrapeStatus returns the live, public state of the scraping API for the
// status panel. It needs no credentials and is cached for a few seconds.
func (h *Handler) ScrapeStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=5")
	writeJSON(w, http.StatusOK, h.scrapeSvc.Status())
}
