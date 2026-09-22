package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
// It does not consume quota.
func (h *Handler) ListScrapeSystems(w http.ResponseWriter, r *http.Request) {
	// Free endpoint, but report the current quota state for convenience.
	if subject, ok := scrapeSubject(r); ok {
		if acct, err := h.scrapeSvc.Account(subject); err == nil {
			writeQuotaHeaders(w, &acct.Quota)
		}
	}
	list, err := h.scrapeSvc.ListSystems()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list systems")
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(subject, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"systems": list})
}

// ScrapeGames resolves a game by id, ROM hash or name and returns its full
// metadata plus media URLs. Each accepted call consumes one unit of the daily
// quota.
func (h *Handler) ScrapeGames(w http.ResponseWriter, r *http.Request) {
	subject, ok := scrapeSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	query, err := parseScrapeGameQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	quota, err := h.scrapeSvc.ConsumeQuota(subject)
	if quota != nil {
		writeQuotaHeaders(w, quota)
	}
	if errors.Is(err, services.ErrQuotaExceeded) {
		h.scrapeSvc.RecordOutcome(subject, "quota_exceeded")
		writeError(w, http.StatusTooManyRequests, "daily scrape quota exceeded")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to consume quota")
		return
	}

	game, matchedBy, err := h.scrapeSvc.FindGames(query)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to resolve game", err)
		return
	}
	if game == nil {
		h.scrapeSvc.RecordOutcome(subject, "not_found")
	} else {
		h.scrapeSvc.RecordOutcome(subject, "ok")
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
// system and game counts. It does not consume quota.
func (h *Handler) ListScrapeFamilies(w http.ResponseWriter, r *http.Request) {
	if subject, ok := scrapeSubject(r); ok {
		if acct, err := h.scrapeSvc.Account(subject); err == nil {
			writeQuotaHeaders(w, &acct.Quota)
		}
	}
	list, err := h.scrapeSvc.ListFamilies()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list families")
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(subject, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"families": list})
}

// ListScrapeGroups returns the system groups (e.g. "mame-fbneo") with their
// system and game counts. It does not consume quota.
func (h *Handler) ListScrapeGroups(w http.ResponseWriter, r *http.Request) {
	if subject, ok := scrapeSubject(r); ok {
		if acct, err := h.scrapeSvc.Account(subject); err == nil {
			writeQuotaHeaders(w, &acct.Quota)
		}
	}
	list, err := h.scrapeSvc.ListGroups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(subject, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"groups": list})
}

// ListScrapePopular returns the most scraped games. It does not consume quota.
func (h *Handler) ListScrapePopular(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	systemID := strings.TrimSpace(r.URL.Query().Get("system_id"))
	list, err := h.scrapeSvc.PopularGames(systemID, limit)
	if err != nil {
		writeServerError(w, http.StatusBadRequest, "failed to load popular games", err)
		return
	}
	if subject, ok := scrapeSubject(r); ok {
		h.scrapeSvc.RecordOutcome(subject, "ok")
	}
	writeJSON(w, http.StatusOK, models.ScrapePopularResponse{Games: list})
}

// ScrapeAccount returns the current scrape subject and its quota state. It does
// not consume quota.
func (h *Handler) ScrapeAccount(w http.ResponseWriter, r *http.Request) {
	subject, ok := scrapeSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	acct, err := h.scrapeSvc.Account(subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load account")
		return
	}
	writeQuotaHeaders(w, &acct.Quota)
	h.scrapeSvc.RecordOutcome(subject, "ok")
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
	if query.CRC == "" && query.MD5 == "" && query.SHA1 == "" && query.SHA256 == "" && query.Name == "" {
		return query, fmt.Errorf("a selector is required: a ROM hash or name")
	}
	if query.Type != "" && !validGameType(query.Type) {
		return query, fmt.Errorf("invalid type %q (base, hack or homebrew)", query.Type)
	}
	return query, nil
}

// validGameType reports whether t is a known game type filter.
func validGameType(t string) bool {
	switch t {
	case "base", "hack", "homebrew":
		return true
	default:
		return false
	}
}

// writeQuotaHeaders exposes the quota state on every scraping response.
func writeQuotaHeaders(w http.ResponseWriter, q *models.ScrapeQuota) {
	w.Header().Set("X-Quota-Limit", strconv.Itoa(q.DailyLimit))
	w.Header().Set("X-Quota-Remaining", strconv.Itoa(q.Remaining))
	w.Header().Set("X-Quota-Reset", q.ResetsAt.UTC().Format(time.RFC3339))
}
