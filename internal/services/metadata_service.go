package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"neoassets/internal/apperr"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"neoassets/internal/economy"
	"neoassets/internal/models"
	"neoassets/pkg/r2"
	"neoassets/pkg/video"
)

// mediaExts are the accepted image extensions for metadata media files.
var mediaExts = map[string]bool{
	".webp": true,
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
}

// MaxDescriptionLength caps the English description a user can submit. The
// translate worker translates sentence by sentence, so this bounds latency and
// cost while still covering ~97% of the existing catalog descriptions.
const MaxDescriptionLength = 1500

// videoExts are the accepted video extensions for gameplay video submissions.
// Any ffmpeg-supported container is accepted; the file is converted to MP4
// (HEVC) on approval.
var videoExts = map[string]bool{
	".webm": true,
	".mp4":  true,
	".mov":  true,
	".mkv":  true,
	".avi":  true,
	".m4v":  true,
	".mpg":  true,
	".mpeg": true,
	".ts":   true,
	".ogv":  true,
	".wmv":  true,
	".flv":  true,
}

// ListMetadataSystems returns the metadata system catalog (seeded by the DAT
// importer), independent from the art-pack systems. A virtual parent (the
// arcade aggregate) owns no games, so its stats are the sum of its family so
// the browse card shows the whole family.
func (s *Service) ListMetadataSystems(ctx context.Context) ([]models.MetadataSystem, error) {
	list, err := s.repo.ListMetadataSystems(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if !list[i].Virtual {
			continue
		}
		var games, base, hack, homebrew, bootleg, aftermarket int
		var textWeighted, mediaWeighted float64
		for j := range list {
			if list[j].Virtual || list[j].Family != list[i].Family {
				continue
			}
			games += list[j].TotalGames
			base += list[j].Base
			hack += list[j].Hack
			homebrew += list[j].Homebrew
			bootleg += list[j].Bootleg
			aftermarket += list[j].Aftermarket
			textWeighted += list[j].TextPct * float64(list[j].TotalGames)
			mediaWeighted += list[j].MediaPct * float64(list[j].TotalGames)
		}
		list[i].TotalGames = games
		list[i].Base = base
		list[i].Hack = hack
		list[i].Homebrew = homebrew
		list[i].Bootleg = bootleg
		list[i].Aftermarket = aftermarket
		if games > 0 {
			list[i].TextPct = textWeighted / float64(games)
			list[i].MediaPct = mediaWeighted / float64(games)
			list[i].MetadataPct = (list[i].TextPct + list[i].MediaPct) / 2
		}
	}
	return list, nil
}

// ListSystemIDsByFamily returns the ids of every system in a family (e.g.
// "arcade"), ordered by name.
func (s *Service) ListSystemIDsByFamily(ctx context.Context, family string) ([]string, error) {
	return s.repo.ListSystemIDsByFamily(ctx, family)
}

// ListSystemIDsByGroup returns the ids of every system in a group (e.g.
// "mame-fbneo"), ordered by name.
func (s *Service) ListSystemIDsByGroup(ctx context.Context, group string) ([]string, error) {
	return s.repo.ListSystemIDsByGroup(ctx, group)
}

// ListFamilies returns every non-empty system family with its counts.
func (s *Service) ListFamilies(ctx context.Context) ([]models.MetadataFamily, error) {
	return s.repo.ListFamilies(ctx)
}

// ListGroups returns every non-empty system group with its counts.
func (s *Service) ListGroups(ctx context.Context) ([]models.MetadataGroup, error) {
	return s.repo.ListGroups(ctx)
}

// ListGamesBySystem returns the games for a system with paging and optional
// game-type filter (base/hack/homebrew) and sort order.
func (s *Service) ListGamesBySystem(ctx context.Context, systemID string, limit, offset int, gtype, sort string) ([]models.Game, int64, error) {
	// A virtual parent (the arcade aggregate) lists every game of its family.
	systemIDs := []string{systemID}
	if sys, err := s.repo.GetMetadataSystem(ctx, systemID); err == nil && sys.Virtual {
		if ids, err := s.repo.ListSystemIDsByFamily(ctx, sys.Family); err == nil && len(ids) > 0 {
			systemIDs = ids
		}
	}
	list, total, err := s.repo.ListGamesBySystems(ctx, systemIDs, limit, offset, gtype, sort)
	if err != nil {
		return nil, 0, err
	}
	if err := s.decorateGameStats(ctx, list); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// SearchGameCandidates resolves light name candidates for the scraping
// resolver: no total count and no per-game stats decoration, since it only
// needs the id, name, type and rating to pick the best match. It returns up to
// limit candidates per system, in one query.
func (s *Service) SearchGameCandidates(ctx context.Context, q string, systemIDs []string, gtype string, limit int) ([]models.Game, error) {
	return s.repo.SearchGamesLight(ctx, q, systemIDs, gtype, limit)
}

// SearchGames searches the game catalog. A virtual parent (the arcade
// aggregate) searches its whole family.
func (s *Service) SearchGames(ctx context.Context, q, systemID, gtype, sort string, limit, offset int) ([]models.Game, int64, error) {
	var systemIDs []string
	if systemID != "" {
		systemIDs = []string{systemID}
		if sys, err := s.repo.GetMetadataSystem(ctx, systemID); err == nil && sys.Virtual {
			if ids, err := s.repo.ListSystemIDsByFamily(ctx, sys.Family); err == nil && len(ids) > 0 {
				systemIDs = ids
			}
		}
	}
	list, total, err := s.repo.SearchGames(ctx, q, systemIDs, gtype, sort, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if err := s.decorateGameStats(ctx, list); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// decorateGameStats fills the computed metadata status flags on a game list.
func (s *Service) decorateGameStats(ctx context.Context, list []models.Game) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	stats, err := s.repo.ListGameStats(ctx, ids)
	if err != nil {
		return err
	}
	for i := range list {
		if st, ok := stats[list[i].ID]; ok {
			list[i].TextComplete = st.TextComplete
			list[i].HasTranslations = st.HasTranslations
			list[i].HasScreenshot = st.HasScreenshot
			list[i].HasFanart = st.HasFanart
			list[i].HasVideo = st.HasVideo
			list[i].HasLogo = st.HasLogo
		}
	}
	return nil
}

// GetGame returns a game bundled with its ROM dumps and media. When lang is
// non-empty (and not "en") the description is resolved to that language's
// translation, falling back to English when no translation exists.
func (s *Service) GetGame(ctx context.Context, id uuid.UUID, lang string) (*models.GameDetail, error) {
	g, err := s.repo.GetGame(ctx, id, lang)
	if err != nil {
		return nil, apperr.NotFound("game not found")
	}
	roms, err := s.repo.ListRomsByGame(ctx, id)
	if err != nil {
		return nil, err
	}
	media, err := s.repo.ListMediaByGame(ctx, id)
	if err != nil {
		return nil, err
	}
	translations, err := s.repo.ListGameTranslations(ctx, id)
	if err != nil {
		return nil, err
	}
	regions, err := s.repo.ListGameRegions(ctx, id)
	if err != nil {
		return nil, err
	}
	// The primary region is the first with text (the catalog priority order).
	primary := ""
	for _, gr := range regions {
		if gr.Name != "" || gr.ReleaseYear != nil {
			primary = gr.Region
			break
		}
	}
	// Attach cover/logo media to their region. Region-less media (e.g. imported
	// assets) belong to the primary region, so they show under it.
	byRegion := map[string][]models.Media{}
	for i := range media {
		m := &media[i]
		if m.Kind != models.MediaCover && m.Kind != models.MediaLogo {
			continue
		}
		if m.Region == "" && primary != "" {
			m.Region = primary
		}
		if m.Region != "" {
			byRegion[m.Region] = append(byRegion[m.Region], *m)
		}
	}
	for i := range regions {
		// Within a region the cover is shown before the logo.
		rm := byRegion[regions[i].Region]
		sort.SliceStable(rm, func(a, b int) bool { return mediaKindRank(rm[a].Kind) < mediaKindRank(rm[b].Kind) })
		regions[i].Media = rm
		if primary == "" && (regions[i].Name != "" || regions[i].ReleaseYear != nil || len(regions[i].Media) > 0) {
			primary = regions[i].Region
		}
	}
	// The full media list is returned so the detail page can show every region's
	// asset; each item carries its own region.
	detail := &models.GameDetail{Game: *g, Roms: roms, Media: media, Regions: regions, Region: primary, Translations: translations}
	if lang != "" && lang != "en" {
		detail.Lang = lang
	}
	return detail, nil
}

// mediaKindRank orders the media shown per region: the cover first, then the
// logo, then anything else.
func mediaKindRank(kind string) int {
	switch kind {
	case models.MediaCover:
		return 0
	case models.MediaLogo:
		return 1
	default:
		return 2
	}
}

// ListGameContributors returns the users with approved metadata contributions
// for a game, ordered by contribution count. It is kept out of GetGame so the
// hot scrape path does not pay for the extra aggregate query.
func (s *Service) ListGameContributors(ctx context.Context, gameID uuid.UUID) ([]models.UserCountStat, error) {
	return s.repo.ListGameContributors(ctx, gameID)
}

// ListLanguages returns all enabled description languages.
func (s *Service) ListLanguages(ctx context.Context) ([]models.Language, error) {
	return s.repo.ListLanguages(ctx)
}

// ListGenres returns the canonical genre catalog used by the web forms.
func (s *Service) ListGenres(ctx context.Context) ([]models.Genre, error) {
	return s.repo.ListGenres(ctx)
}

// ListRegions returns the canonical region catalog used by the web forms.
func (s *Service) ListRegions(ctx context.Context) ([]models.Region, error) {
	return s.repo.ListRegions(ctx)
}

// validateRegion checks that a non-empty region is in the canonical catalog.
func (s *Service) validateRegion(ctx context.Context, region string) error {
	region = strings.TrimSpace(region)
	if region == "" {
		return nil
	}
	exists, err := s.repo.RegionExists(ctx, region)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("unknown region %q", region)
	}
	return nil
}

// isHexString reports whether s is a non-empty lowercase hex string.
func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// validateHash checks an optional hash value: empty is allowed, otherwise it must
// be lowercase hex of the expected length.
func validateHash(label, v string, n int) error {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != n || !isHexString(v) {
		return fmt.Errorf("invalid %s", label)
	}
	return nil
}

// validateRoms validates the ROM dump changes carried in a payload. Each entry is
// an add, edit or delete. SHA1 is optional; when present it must be valid and
// unique per game (skipped for a brand-new game, which has no row yet).
func (s *Service) validateRoms(ctx context.Context, gameID *uuid.UUID, raw any) error {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	seenSHA1 := map[string]bool{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return apperr.Invalid("invalid ROM entry")
		}
		str := func(k string) string {
			v, _ := m[k].(string)
			return strings.TrimSpace(v)
		}
		action, _ := m["action"].(string)
		switch action {
		case "add", "edit", "delete":
		default:
			return fmt.Errorf("invalid ROM action %q", action)
		}
		if action == "edit" || action == "delete" {
			if _, err := uuid.Parse(str("id")); err != nil {
				return apperr.Invalid("invalid ROM id")
			}
		}
		if action == "delete" {
			continue
		}
		if str("name") == "" {
			return apperr.Invalid("a ROM dump name is required")
		}
		if err := validateHash("CRC", str("crc"), 8); err != nil {
			return err
		}
		if err := validateHash("MD5", str("md5"), 32); err != nil {
			return err
		}
		if err := validateHash("SHA256", str("sha256"), 64); err != nil {
			return err
		}
		if err := s.validateRegion(ctx, str("region")); err != nil {
			return err
		}
		// SHA1 is optional: arcade ZIP sets are contributed by name and size
		// only. When present it must be valid and unique per game.
		sha1 := strings.ToLower(str("sha1"))
		if sha1 != "" {
			if len(sha1) != 40 || !isHexString(sha1) {
				return apperr.Invalid("invalid SHA1")
			}
			if seenSHA1[sha1] {
				return apperr.Invalid("the same SHA1 appears twice in the submission")
			}
			seenSHA1[sha1] = true
			// Uniqueness is per game; a brand-new game has no row yet.
			if gameID != nil {
				exclude := uuid.Nil
				if action == "edit" {
					exclude, _ = uuid.Parse(str("id"))
				}
				exists, err := s.repo.RomSHA1Exists(ctx, *gameID, sha1, exclude)
				if err != nil {
					return err
				}
				if exists {
					return apperr.Conflict("a ROM dump with that SHA1 already exists for this game")
				}
			}
		}
	}
	return nil
}

// LookupGame finds games by any of the given ROM hashes, optionally scoped to a
// system (empty systemID searches all systems).
func (s *Service) LookupGame(ctx context.Context, crc, md5, sha1, sha256, systemID string) ([]models.Game, error) {
	return s.repo.LookupGameByHash(ctx, crc, md5, sha1, sha256, systemID)
}

// ---------------------------------------------------------------------------
// Metadata contributions
// ---------------------------------------------------------------------------

// CreateMetadataSubmission creates a contribution already in pending review
// (drafts are kept client-side, so only submissions for review hit the DB).
// Media files listed in req.Files are registered against the submission.
func (s *Service) CreateMetadataSubmission(ctx context.Context, userID uuid.UUID, req models.MetadataSubmissionRequest) (*models.MetadataSubmission, error) {
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = models.MetadataKindEdit
	}
	if kind != models.MetadataKindEdit && kind != models.MetadataKindNewGame {
		return nil, apperr.Invalid("invalid contribution kind")
	}
	if req.GameID == nil && req.SystemID == nil {
		return nil, apperr.Invalid("a game or system must be provided")
	}
	var targetGame *models.Game
	if req.GameID != nil {
		g, err := s.repo.GetGame(ctx, *req.GameID, "")
		if err != nil {
			return nil, apperr.NotFound("game not found")
		}
		targetGame = g
	}
	if req.SystemID != nil {
		if _, err := s.repo.GetMetadataSystem(ctx, *req.SystemID); err != nil {
			return nil, apperr.NotFound("system not found")
		}
	}
	// A rename must not collide with another game's name in the same system (the
	// games table enforces a unique (system_id, name) constraint).
	if targetGame != nil {
		if name, ok := req.Payload["name"].(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				conflictID, err := s.repo.ConflictingGameID(ctx, targetGame.SystemID, name, targetGame.ID)
				if err != nil {
					return nil, err
				}
				if conflictID != uuid.Nil {
					return nil, &GameNameConflictError{GameID: conflictID, SystemID: targetGame.SystemID, Name: name}
				}
			}
		}
	}
	if desc, ok := req.Payload["description"].(string); ok {
		if n := len([]rune(strings.TrimSpace(desc))); n > MaxDescriptionLength {
			return nil, fmt.Errorf("description must be at most %d characters (got %d)", MaxDescriptionLength, n)
		}
	}
	// The genre must be one of the canonical catalog values. The service does
	// not normalize: callers (web form, importer) must send a catalog name.
	if g, ok := req.Payload["genre"].(string); ok {
		g = strings.TrimSpace(g)
		if g != "" {
			exists, err := s.repo.GenreExists(ctx, g)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, fmt.Errorf("unknown genre %q", g)
			}
		}
	}
	// Regions (text and regional media) must also be catalog values.
	if r, ok := req.Payload["region"].(string); ok {
		if err := s.validateRegion(ctx, r); err != nil {
			return nil, err
		}
	}
	if list, ok := req.Payload["regions"].([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"region", "name_from", "release_from"} {
				if r, ok := m[key].(string); ok {
					if err := s.validateRegion(ctx, r); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	for _, f := range req.Files {
		if err := s.validateRegion(ctx, f.Region); err != nil {
			return nil, err
		}
	}
	// ROM dump changes are allowed on an existing game and on a brand-new game
	// (applied right after the game row is created on approval).
	if raw, ok := req.Payload["roms"]; ok {
		if req.GameID == nil && kind != models.MetadataKindNewGame {
			return nil, apperr.Invalid("ROM dumps can only be changed on a game")
		}
		if err := s.validateRoms(ctx, req.GameID, raw); err != nil {
			return nil, err
		}
	}
	// A deletion must explain why (the region is being removed, not corrected).
	deleting := false
	for _, f := range req.Files {
		if f.Delete {
			deleting = true
		}
	}
	if v, ok := req.Payload["delete"].(bool); ok && v {
		deleting = true
	}
	// Deleting a ROM dump also needs a reason.
	if raw, ok := req.Payload["roms"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				if a, _ := m["action"].(string); a == "delete" {
					deleting = true
				}
			}
		}
	}
	// A whole-game deletion request (duplicate report) targets a game, points at
	// the game to keep and must explain why.
	if v, ok := req.Payload["delete_game"].(bool); ok && v {
		if req.GameID == nil {
			return nil, apperr.Invalid("a deletion request must target a game")
		}
		if link, _ := req.Payload["duplicate_of"].(string); strings.TrimSpace(link) == "" {
			return nil, apperr.Invalid("a link to the duplicate game is required")
		}
		deleting = true
	}
	if deleting {
		if note, _ := req.Payload["note"].(string); strings.TrimSpace(note) == "" {
			return nil, apperr.Invalid("a reason is required when deleting")
		}
	}

	if kind == models.MetadataKindNewGame {
		if req.GameID != nil {
			return nil, apperr.Invalid("a new game cannot target an existing game")
		}
		name, _ := req.Payload["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, apperr.Invalid("game name is required")
		}
		if v, ok := req.Payload["type"].(string); ok {
			if !models.ValidGameType(v) {
				return nil, fmt.Errorf("invalid game type %q", v)
			}
		}
		exists, err := s.repo.GameExistsByName(ctx, *req.SystemID, name)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, apperr.Conflict("a game with that name already exists in this system")
		}
		// Block a duplicate pending new game with the same name in this system.
		subs, _, err := s.repo.ListMetadataSubmissionsByUser(ctx, userID, "", 0, 0)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			if sub.Kind != models.MetadataKindNewGame || sub.Status != models.MetadataPending || sub.SystemID == nil || *sub.SystemID != *req.SystemID {
				continue
			}
			var p map[string]any
			if json.Unmarshal(sub.Payload, &p) == nil {
				if n, _ := p["name"].(string); strings.EqualFold(strings.TrimSpace(n), name) {
					return nil, apperr.Conflict("you already have a pending submission for this game")
				}
			}
		}
	} else {
		// Block a new submission for a field/kind that already has a pending
		// submission for this game/system from the same user.
		pending, err := s.pendingSubmissionKeys(ctx, userID, req.GameID, req.SystemID)
		if err != nil {
			return nil, err
		}
		for k := range payloadFieldKeys(req.Payload) {
			if pending[k] {
				return nil, fmt.Errorf("you already have a pending submission for %q on this game", k)
			}
		}
		for _, f := range req.Files {
			if pending[f.Kind] {
				return nil, fmt.Errorf("you already have a pending submission for %q on this game", f.Kind)
			}
		}
	}

	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return nil, apperr.Invalid("invalid payload")
	}
	// Probe uploaded videos before creating the submission so a bad frame rate is
	// rejected with the accurate ffprobe reading and no partial row is left
	// behind. The measured metadata is reused when the file rows are inserted.
	// Probing downloads the video, so the entries are checked first.
	if err := s.checkVideoEntries(ctx, userID, req.Files); err != nil {
		return nil, err
	}
	videoMeta := map[string]*models.VideoMeta{}
	for _, f := range req.Files {
		if f.Delete || f.Move || f.Kind != models.MediaVideo {
			continue
		}
		ctx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
		vm, err := s.probeVideo(ctx, f.ObjectKey)
		cancel()
		if errors.Is(err, errVideoBusy) {
			return nil, err
		}
		if err != nil {
			return nil, apperr.Invalid("could not read the video, please upload it again")
		}
		if vm.FPS > 0 && vm.FPS < minVideoFPS {
			return nil, fmt.Errorf("video must be at least %d fps, 60 recommended (detected %d fps)", minVideoFPS, vm.FPS)
		}
		videoMeta[f.ObjectKey] = vm
	}

	// An upload is stored with the type it was signed with: refuse a type its
	// kind can't be before anything is written.
	for _, f := range req.Files {
		if f.Delete || f.Move {
			continue
		}
		if _, err := uploadContentType(f.Kind, f.MimeType); err != nil {
			return nil, err
		}
	}

	id, err := s.repo.CreateMetadataSubmission(ctx, req.GameID, req.SystemID, userID, kind, payload)
	if err != nil {
		return nil, err
	}
	for _, f := range req.Files {
		if !validMediaKind(f.Kind) {
			return nil, fmt.Errorf("invalid media kind %q", f.Kind)
		}
		if f.Delete {
			// Removing a regional media: no file to upload or move.
			if req.GameID == nil {
				return nil, apperr.Invalid("media can only be deleted on a game")
			}
			if f.Kind != models.MediaCover && f.Kind != models.MediaLogo {
				return nil, apperr.Invalid("only cover and logo can be deleted per region")
			}
			if strings.TrimSpace(f.Region) == "" {
				return nil, apperr.Invalid("region required")
			}
			if err := s.repo.AddMetadataSubmissionFile(ctx, id, f.Kind, f.ObjectKey, f.FileName, "", f.Region, f.Size, true, false, nil); err != nil {
				return nil, err
			}
			continue
		}
		ext := strings.ToLower(getExt(f.FileName))
		if !validMediaExt(f.Kind, ext) {
			return nil, fmt.Errorf("unsupported %s extension %q", mediaExtLabel(f.Kind), ext)
		}
		if f.Move {
			// References an existing cover/logo of this game to move region.
			if req.GameID == nil {
				return nil, apperr.Invalid("media can only be moved on a game")
			}
			if f.Kind != models.MediaCover && f.Kind != models.MediaLogo {
				return nil, apperr.Invalid("only cover and logo can be moved between regions")
			}
			ok, err := s.repo.MediaExists(ctx, *req.GameID, f.Kind, f.ObjectKey)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, apperr.NotFound("media not found")
			}
		} else {
			if f.ObjectKey == "" {
				return nil, apperr.Invalid("object_key required")
			}
			// Only the uploader's own staging objects are accepted. This blocks
			// referencing canonical/published objects (media/games, media/systems,
			// packs, history, rejected), which could otherwise be deleted or
			// overwritten via cancel/reject/approve.
			if !isOwnStagingKey(f.ObjectKey, userID) {
				return nil, apperr.Invalid("invalid upload reference")
			}
			if err := s.ensureUploaded(ctx, f.ObjectKey, f.FileName); err != nil {
				return nil, err
			}
		}
		// A moved media isn't an upload: it keeps the type it is stored with.
		contentType := f.MimeType
		if !f.Move {
			contentType, _ = uploadContentType(f.Kind, f.MimeType) // checked above
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		var vmeta *models.VideoMeta
		if f.Kind == models.MediaVideo {
			vmeta = videoMeta[f.ObjectKey]
		}
		if err := s.repo.AddMetadataSubmissionFile(ctx, id, f.Kind, f.ObjectKey, f.FileName, contentType, f.Region, f.Size, false, f.Move, vmeta); err != nil {
			return nil, err
		}
	}
	return s.repo.GetMetadataSubmissionForUser(ctx, id, userID)
}

// payloadFieldKeys returns the editable payload field keys, normalizing the
// multi-region "regions" list to the fields it changes (name, release_year) and
// dropping control keys (region, region_from, field, delete, note, ...). This
// keeps the pending-submission block consistent between the legacy single-region
// shape and the multi-region shape.
func payloadFieldKeys(p map[string]any) map[string]bool {
	skip := map[string]bool{
		"note": true, "duplicate_of": true, "region": true, "region_from": true,
		"field": true, "delete": true, "regions": true, "release_month": true,
		"name_from": true, "release_from": true, "delete_name": true, "delete_release": true,
	}
	keys := map[string]bool{}
	for k := range p {
		if skip[k] {
			continue
		}
		keys[k] = true
	}
	if raw, ok := p["regions"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if m["name"] != nil || m["name_from"] != nil || m["delete_name"] == true {
				keys["name"] = true
			}
			if m["release_year"] != nil || m["release_from"] != nil || m["delete_release"] == true {
				keys["release_year"] = true
			}
		}
	}
	return keys
}

// pendingSubmissionKeys returns the set of payload fields and media kinds that
// already have a pending submission for the given user and game/system.
func (s *Service) pendingSubmissionKeys(ctx context.Context, userID uuid.UUID, gameID *uuid.UUID, systemID *string) (map[string]bool, error) {
	subs, _, err := s.repo.ListMetadataSubmissionsByUser(ctx, userID, "", 0, 0)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, sub := range subs {
		if sub.Status != models.MetadataPending || sub.Kind == models.MetadataKindNewGame {
			continue
		}
		if gameID != nil {
			if sub.GameID == nil || *sub.GameID != *gameID {
				continue
			}
		} else if systemID != nil {
			if sub.SystemID == nil || *sub.SystemID != *systemID {
				continue
			}
		}
		var p map[string]any
		if err := json.Unmarshal(sub.Payload, &p); err == nil {
			for k := range payloadFieldKeys(p) {
				keys[k] = true
			}
		}
		files, err := s.repo.ListMetadataSubmissionFiles(ctx, sub.ID)
		if err == nil {
			for _, f := range files {
				keys[f.Kind] = true
			}
		}
	}
	return keys, nil
}

// PendingMetadataKeys returns the sorted list of payload fields and media kinds
// that already have a pending submission for the given user and game, so the
// frontend can block re-submitting them.
func (s *Service) PendingMetadataKeys(ctx context.Context, userID, gameID uuid.UUID) ([]string, error) {
	keys, err := s.pendingSubmissionKeys(ctx, userID, &gameID, nil)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// MetadataUploadURLPre presigns an upload to a temporary staging location
// (media/staging/...) before any submission row exists. On approval the file is
// moved to an immutable canonical key, so unapproved uploads are never public
// and replaced assets always get a brand-new URL (no cache invalidation).
func (s *Service) MetadataUploadURLPre(ctx context.Context, userID uuid.UUID, req models.MetadataUploadURLRequest) (*models.UploadResponse, error) {
	if req.GameID == nil && req.SystemID == nil {
		return nil, apperr.Invalid("a game or system must be provided")
	}
	if !validMediaKind(req.Kind) {
		return nil, fmt.Errorf("invalid media kind %q", req.Kind)
	}
	ext := strings.ToLower(getExt(req.FileName))
	if !validMediaExt(req.Kind, ext) {
		return nil, fmt.Errorf("unsupported %s extension %q", mediaExtLabel(req.Kind), ext)
	}
	contentType, err := uploadContentType(req.Kind, req.MimeType)
	if err != nil {
		return nil, err
	}
	if req.GameID != nil {
		if _, err := s.repo.GetGame(ctx, *req.GameID, ""); err != nil {
			return nil, apperr.NotFound("game not found")
		}
	} else if req.SystemID != nil {
		if _, err := s.repo.GetMetadataSystem(ctx, *req.SystemID); err != nil {
			return nil, apperr.NotFound("system not found")
		}
	}
	objectKey := stagingMediaKey(userID, req.Kind, req.FileName)
	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, objectKey, contentType, s.uploadTTL)
	if err != nil {
		log.Error().Err(err).Msg("failed to sign metadata upload URL")
		return nil, apperr.Unavailable("could not prepare upload")
	}
	return &models.UploadResponse{
		UploadURL: uploadURL,
		ObjectKey: objectKey,
		ExpiresAt: time.Now().Add(s.uploadTTL),
	}, nil
}

// mediaCacheControl is applied to approved media. Because canonical keys are
// immutable (a new UUID per replacement), the URL never changes content, so it
// can be cached aggressively at the edge and in the browser.
const mediaCacheControl = "public, max-age=31536000, immutable"

// stagingMediaKey builds a temporary upload key under media/staging/, namespaced
// by the uploader so a submission can only ever reference its owner's uploads.
func stagingMediaKey(userID uuid.UUID, kind, fileName string) string {
	name := filepath.Base(fileName)
	if name == "" || name == "." {
		name = kind
	}
	return fmt.Sprintf("media/staging/%s/%s/%s/%s", userID, uuid.NewString(), kind, name)
}

// isOwnStagingKey reports whether objectKey is a staging upload owned by the
// given user. Metadata submissions may only reference their uploader's own
// staging objects; canonical keys (media/games, media/systems, packs, history,
// rejected) must never be accepted from a client.
func isOwnStagingKey(objectKey string, userID uuid.UUID) bool {
	return strings.HasPrefix(objectKey, "media/staging/"+userID.String()+"/")
}

// isSubmissionMediaKey reports whether objectKey belongs to the given metadata
// submission's own upload namespace.
func isSubmissionMediaKey(objectKey string, submissionID uuid.UUID) bool {
	return strings.HasPrefix(objectKey, "media/submissions/"+submissionID.String()+"/")
}

// ownedSubmissionUpload reports whether f is a file the submission itself
// uploaded: not a move/delete entry, and stored in the submitter's staging
// namespace or the submission's own one. Only these may be removed when a
// submission is cancelled or rejected. Move entries reference the game's
// published media and delete entries don't reference an upload, so neither
// names an object the submission owns.
func ownedSubmissionUpload(f models.MetadataSubmissionFile, sub *models.MetadataSubmission) bool {
	if f.IsMove || f.IsDelete {
		return false
	}
	return isOwnStagingKey(f.ObjectKey, sub.UserID) || isSubmissionMediaKey(f.ObjectKey, sub.ID)
}

// immutableMediaKey builds a fresh, immutable canonical key for approved media:
// the UUID is random so each replacement gets a new URL.
func immutableMediaKey(sub *models.MetadataSubmission, systemID, kind, ext string) string {
	if ext == "" {
		ext = ".webp"
	}
	if sub.GameID != nil {
		return fmt.Sprintf("media/games/%s/%s%s", systemID, uuid.NewString(), ext)
	}
	return fmt.Sprintf("media/systems/%s/%s-%s%s", systemID, kind, uuid.NewString(), ext)
}

// metadataImageSpec returns the normalization applied to an approved metadata
// image, so the backend never trusts client-side conversion. Fanart is
// center-cropped to 1920x1080 (16:9), covers and logos are capped at 1024px,
// and the remaining image kinds are capped at 1920px. Metadata images are
// always flattened to a single WebP frame.
func metadataImageSpec(kind string) video.ImageSpec {
	switch kind {
	case models.MediaFanart:
		return video.ImageSpec{TargetW: 1920, TargetH: 1080, Quality: 90, FirstFrameOnly: true}
	case models.MediaCover, models.MediaLogo:
		return video.ImageSpec{MaxSize: 1024, Quality: 90, FirstFrameOnly: true}
	default:
		return video.ImageSpec{MaxSize: 1920, Quality: 90, FirstFrameOnly: true}
	}
}

// MetadataUploadURL returns a presigned URL for a media file, registers it.
func (s *Service) MetadataUploadURL(ctx context.Context, submissionID, userID uuid.UUID, req models.MetadataUploadRequest) (*models.UploadResponse, error) {
	sub, err := s.repo.GetMetadataSubmissionForUser(ctx, submissionID, userID)
	if err != nil {
		return nil, apperr.NotFound("submission not found")
	}
	if sub.Status != models.MetadataCreated && sub.Status != models.MetadataPending {
		return nil, apperr.Conflict("submission is not editable")
	}
	if !validMediaKind(req.Kind) {
		return nil, fmt.Errorf("invalid media kind %q", req.Kind)
	}
	ext := strings.ToLower(getExt(req.FileName))
	if !validMediaExt(req.Kind, ext) {
		return nil, fmt.Errorf("unsupported %s extension %q", mediaExtLabel(req.Kind), ext)
	}
	contentType, err := uploadContentType(req.Kind, req.MimeType)
	if err != nil {
		return nil, err
	}

	objectKey := fmt.Sprintf("media/submissions/%s/%s/%s", submissionID, req.Kind, filepath.Base(req.FileName))
	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, objectKey, contentType, s.uploadTTL)
	if err != nil {
		log.Error().Err(err).Msg("failed to sign metadata upload URL")
		return nil, apperr.Unavailable("could not prepare upload")
	}
	if err := s.repo.AddMetadataSubmissionFile(ctx, submissionID, req.Kind, objectKey, req.FileName, contentType, req.Region, req.Size, false, false, nil); err != nil {
		return nil, err
	}
	return &models.UploadResponse{
		UploadURL: uploadURL,
		ObjectKey: objectKey,
		ExpiresAt: time.Now().Add(s.uploadTTL),
	}, nil
}

// SubmitMetadataSubmission moves a draft contribution into pending review.
func (s *Service) SubmitMetadataSubmission(ctx context.Context, submissionID, userID uuid.UUID) (*models.MetadataSubmission, error) {
	sub, err := s.repo.GetMetadataSubmissionForUser(ctx, submissionID, userID)
	if err != nil {
		return nil, err
	}
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	// A submission must carry either an uploaded media file or a text field.
	if len(files) == 0 && !payloadHasText(sub.Payload) {
		return nil, apperr.Invalid("submission has no content")
	}
	return s.repo.SubmitMetadataSubmission(ctx, submissionID, userID)
}

// CancelMetadataSubmission deletes the user's own submission (while it is still
// editable, pending review or rejected) together with its uploaded media, so a
// mistake can be cleared and submitted again without waiting for a review.
func (s *Service) CancelMetadataSubmission(ctx context.Context, submissionID, userID uuid.UUID) error {
	sub, err := s.repo.GetMetadataSubmissionForUser(ctx, submissionID, userID)
	if err != nil {
		return apperr.NotFound("submission not found")
	}
	if sub.Status == models.MetadataApproved {
		return apperr.Conflict("an approved submission cannot be cancelled")
	}
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, submissionID)
	if err != nil {
		return err
	}
	// Only delete objects this submission actually owns (its own staging or
	// submission namespace). Move/delete entries reference existing canonical
	// media that must never be removed here.
	keys := make([]string, 0, len(files))
	for _, f := range files {
		if ownedSubmissionUpload(f, sub) {
			keys = append(keys, f.ObjectKey)
		}
	}
	if len(keys) > 0 {
		if err := s.r2.DeleteObjects(ctx, keys); err != nil {
			return fmt.Errorf("delete submission media: %w", err)
		}
	}
	return s.repo.DeleteMetadataSubmission(ctx, submissionID)
}

// payloadHasText reports whether a submission payload carries a editable field.
func payloadHasText(payload json.RawMessage) bool {
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		return false
	}
	for _, k := range []string{"name", "description", "region", "publisher", "developer", "genre", "release_year", "rating", "type"} {
		switch v := p[k].(type) {
		case string:
			if v != "" {
				return true
			}
		case float64:
			if v > 0 {
				return true
			}
		}
	}
	// A multi-region text submission carries its changes in the regions list.
	if raw, ok := p["regions"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if s, _ := m["name"].(string); strings.TrimSpace(s) != "" {
				return true
			}
			if n, ok := m["release_year"].(float64); ok && n > 0 {
				return true
			}
			if m["delete_name"] == true || m["delete_release"] == true || m["name_from"] != nil || m["release_from"] != nil {
				return true
			}
		}
	}
	// ROM dump changes count as editable content.
	if raw, ok := p["roms"].([]any); ok && len(raw) > 0 {
		return true
	}
	return false
}

// ListMyMetadataSubmissions lists the user's contributions.
func (s *Service) ListMyMetadataSubmissions(ctx context.Context, userID uuid.UUID, status string, limit, offset int) ([]models.MetadataSubmission, int64, int, error) {
	list, total, err := s.repo.ListMetadataSubmissionsByUser(ctx, userID, status, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := s.decorateMetadataNames(ctx, list); err != nil {
		return nil, 0, 0, err
	}
	if err := s.enrichMetadataSubmissions(ctx, list); err != nil {
		return nil, 0, 0, err
	}
	metadataXP, _, err := s.repo.UserAwardedXPBySource(ctx, userID)
	if err != nil {
		return nil, 0, 0, err
	}
	return list, total, metadataXP, nil
}

// ReviewSummary returns the newest review items (lightweight, no payloads) and
// the user's lifetime awarded XP, so the notification badge never downloads the
// full review feed.
func (s *Service) ReviewSummary(ctx context.Context, userID uuid.UUID, limit int) (*models.ReviewSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	items, err := s.repo.ListUserReviewItems(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	metadataXP, sapXP, err := s.repo.UserAwardedXPBySource(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &models.ReviewSummary{Items: items, TotalXP: metadataXP + sapXP}, nil
}

// ListMetadataSubmissions lists all contributions, optionally filtered. With no
// filter it returns the review history only (never client-side drafts).
func (s *Service) ListMetadataSubmissions(ctx context.Context, status string) ([]models.MetadataSubmission, error) {
	if status != "" {
		switch status {
		case models.MetadataCreated, models.MetadataPending, models.MetadataApproved, models.MetadataRejected:
		default:
			return nil, apperr.Invalid("invalid status filter")
		}
	}
	var list []models.MetadataSubmission
	var err error
	if status == "" {
		list, err = s.repo.ListMetadataReviewSubmissions(ctx)
	} else {
		list, err = s.repo.ListMetadataSubmissions(ctx, status)
	}
	if err != nil {
		return nil, err
	}
	if err := s.decorateMetadataNames(ctx, list); err != nil {
		return nil, err
	}
	if err := s.enrichMetadataSubmissions(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

// enrichMetadataSubmissions fills the target game/system display data and the
// kinds of change for each contribution so the admin review list can render
// them without extra requests. It batches the lookups to avoid N+1 queries.
func (s *Service) enrichMetadataSubmissions(ctx context.Context, list []models.MetadataSubmission) error {
	if len(list) == 0 {
		return nil
	}
	gameIDs := make([]uuid.UUID, 0, len(list))
	sysIDs := make([]string, 0, len(list))
	subIDs := make([]uuid.UUID, 0, len(list))
	for i := range list {
		if list[i].GameID != nil {
			gameIDs = append(gameIDs, *list[i].GameID)
		}
		if list[i].SystemID != nil {
			sysIDs = append(sysIDs, *list[i].SystemID)
		}
		subIDs = append(subIDs, list[i].ID)
	}

	gameByID := map[uuid.UUID]models.Game{}
	games, err := s.repo.ListGamesByIDs(ctx, gameIDs)
	if err != nil {
		return err
	}
	for _, g := range games {
		gameByID[g.ID] = g
	}
	sysNames, err := s.repo.SystemNamesByIDs(ctx, sysIDs)
	if err != nil {
		return err
	}
	files, err := s.repo.MetadataSubmissionFilesByIDs(ctx, subIDs)
	if err != nil {
		return err
	}
	kinds, err := s.repo.MetadataSubmissionFileKindsByIDs(ctx, subIDs)
	if err != nil {
		return err
	}
	points, err := s.repo.SubmissionPointsByIDs(ctx, subIDs)
	if err != nil {
		return err
	}

	for i := range list {
		list[i].PointsEarned = points[list[i].ID]
		list[i].BasePointsEarned = metadataBasePoints(s.repo.Economy().Points, list[i].Payload, list[i].Kind, kinds[list[i].ID])
		list[i].Files = files[list[i].ID]
		if list[i].GameID != nil {
			if g, ok := gameByID[*list[i].GameID]; ok {
				list[i].GameName = g.Name
				list[i].SystemName = g.SystemName
				list[i].Cover = g.Cover
				list[i].CoverUpdated = g.CoverUpdated
				// A game edit carries game_id, not system_id, so resolve it from
				// the game for the review list's system filter.
				if list[i].SystemID == nil && g.SystemID != "" {
					sid := g.SystemID
					list[i].SystemID = &sid
				}
			}
		}
		if list[i].SystemID != nil {
			list[i].SystemName = sysNames[*list[i].SystemID]
		}
		if list[i].Kind == models.MetadataKindNewGame {
			// A new game has no row yet, so use the proposed name from the payload.
			var p map[string]any
			if json.Unmarshal(list[i].Payload, &p) == nil {
				if n, _ := p["name"].(string); strings.TrimSpace(n) != "" {
					list[i].GameName = strings.TrimSpace(n)
				}
			}
		}
		// Change kinds are the union of the payload text fields and the uploaded
		// media kinds; "note" is the free-text reason and is not a change itself.
		set := map[string]bool{}
		var p map[string]any
		if err := json.Unmarshal(list[i].Payload, &p); err == nil {
			for k := range payloadFieldKeys(p) {
				set[k] = true
			}
		}
		for _, k := range kinds[list[i].ID] {
			set[k] = true
		}
		out := make([]string, 0, len(set))
		for k := range set {
			out = append(out, k)
		}
		sort.Strings(out)
		list[i].ChangeKinds = out
	}
	return nil
}

// GetMetadataSubmissionDetail returns a contribution with its uploaded files
// and a snapshot of the current target (game or system) values and media, so
// admins can compare the proposed change against what is already published.
func (s *Service) GetMetadataSubmissionDetail(ctx context.Context, id uuid.UUID) (*models.MetadataSubmissionDetail, error) {
	sub, err := s.repo.GetMetadataSubmission(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("submission not found")
	}
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	// decorateMetadataNames mutates the slice elements, so pass a slice holding a
	// copy and write the decorated value back to sub.
	decorated := []models.MetadataSubmission{*sub}
	if err := s.decorateMetadataNames(ctx, decorated); err != nil {
		return nil, err
	}
	*sub = decorated[0]
	detail := &models.MetadataSubmissionDetail{Submission: sub, Files: files, Media: []models.Media{}}
	switch {
	case sub.Kind == models.MetadataKindNewGame && sub.SystemID != nil:
		// A new game has no current media; only the target system is shown.
		if sys, err := s.repo.GetMetadataSystem(ctx, *sub.SystemID); err == nil {
			detail.System = sys
		}
	case sub.GameID != nil:
		game, err := s.GetGame(ctx, *sub.GameID, "")
		if err == nil {
			detail.Game = game
			// All media (every region), so the reviewer can compare the region
			// being submitted instead of only the resolved primary one.
			if media, merr := s.repo.ListMediaByGame(ctx, *sub.GameID); merr == nil {
				detail.Media = media
			} else {
				detail.Media = game.Media
			}
		}
	case sub.SystemID != nil:
		if sys, err := s.repo.GetMetadataSystem(ctx, *sub.SystemID); err == nil {
			detail.System = sys
		}
		if media, err := s.repo.ListMediaBySystem(ctx, *sub.SystemID); err == nil {
			detail.Media = media
		}
	}
	return detail, nil
}

// decorateMetadataNames resolves the submitter and reviewer usernames.
func (s *Service) decorateMetadataNames(ctx context.Context, list []models.MetadataSubmission) error {
	if len(list) == 0 {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for i := range list {
		if !seen[list[i].UserID] {
			seen[list[i].UserID] = true
			ids = append(ids, list[i].UserID)
		}
		if list[i].ReviewedBy != nil && !seen[*list[i].ReviewedBy] {
			seen[*list[i].ReviewedBy] = true
			ids = append(ids, *list[i].ReviewedBy)
		}
	}
	names, err := s.repo.UserNamesByID(ctx, ids)
	if err != nil {
		return err
	}
	for i := range list {
		list[i].SubmittedByName = names[list[i].UserID]
		if list[i].ReviewedBy != nil {
			list[i].ReviewedByName = names[*list[i].ReviewedBy]
		}
	}
	return nil
}

// validateApprovedMedia checks the media files of a submission before it is
// applied. Images are normalized by the backend on approval, so they only need
// to be decodable; videos must have a sane frame rate (at least 23 fps, 60 fps
// recommended). The source frame rate is preserved on re-encode, except videos
// above 60 fps, which are capped to 60 fps. Deletes and moves carry no new
// content: their object key points at the existing media (which may already be
// gone from R2), so they are skipped.
// minVideoFPS is the minimum accepted source frame rate. It is enforced when a
// submission is created (with the accurate ffprobe reading) and again on
// approval as a backstop.
const minVideoFPS = 23

func (s *Service) validateApprovedMedia(ctx context.Context, files []models.MetadataSubmissionFile) error {
	for _, f := range files {
		if f.IsDelete || f.IsMove {
			continue
		}
		if err := s.validateApprovedFile(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// validateApprovedFile downloads and probes one submitted file for
// validateApprovedMedia; a video holds a review slot while it is in memory.
func (s *Service) validateApprovedFile(ctx context.Context, f models.MetadataSubmissionFile) error {
	if f.Kind == models.MediaVideo {
		release, err := acquireVideoSlot(ctx, reviewSlots)
		if err != nil {
			return err
		}
		defer release()
	}
	data, err := s.r2.DownloadObject(ctx, f.ObjectKey, maxUploadSize(f.Kind, f.FileName))
	if err != nil {
		return fmt.Errorf("download %s: %w", f.Kind, err)
	}
	if f.Kind == models.MediaVideo {
		meta, err := video.Probe(ctx, data)
		if err != nil {
			return fmt.Errorf("probe video: %w", err)
		}
		if meta.FPS > 0 && meta.FPS < minVideoFPS {
			return fmt.Errorf("video must be at least %d fps, 60 recommended (detected %d fps)", minVideoFPS, meta.FPS)
		}
		return nil
	}
	if _, err := video.ProbeImage(ctx, data); err != nil {
		return fmt.Errorf("probe %s image: %w", f.Kind, err)
	}
	return nil
}

// resolveSubmissionMediaRegions assigns the game's primary region to cover and
// logo files submitted without one, so a new asset replaces the primary region's
// asset (there is one cover and one logo per region) instead of adding a second.
func (s *Service) resolveSubmissionMediaRegions(ctx context.Context, sub *models.MetadataSubmission) error {
	if sub.GameID == nil {
		return nil
	}
	primary, err := s.repo.PrimaryGameRegion(ctx, *sub.GameID)
	if err != nil {
		return err
	}
	if primary == "" {
		return nil
	}
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, sub.ID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Region != "" || f.IsDelete || f.IsMove {
			continue
		}
		if f.Kind != models.MediaCover && f.Kind != models.MediaLogo {
			continue
		}
		if err := s.repo.UpdateMetadataSubmissionFileRegion(ctx, f.ID, primary); err != nil {
			return err
		}
	}
	return nil
}

// GameNameConflictError reports a rename that collides with an existing game in
// the same system. It carries the conflicting game so the review UI can link to
// it.
type GameNameConflictError struct {
	GameID   uuid.UUID
	SystemID string
	Name     string
}

func (e *GameNameConflictError) Error() string {
	return "another game in this system already has that name"
}

// ensureGameNameAvailable rejects a rename whose target name already belongs to
// another game in the same system, so approval fails with a clear message
// instead of a unique-constraint error.
func (s *Service) ensureGameNameAvailable(ctx context.Context, gameID uuid.UUID, payload []byte) error {
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil
	}
	names := []string{}
	if name, _ := p["name"].(string); strings.TrimSpace(name) != "" {
		names = append(names, strings.TrimSpace(name))
	}
	// A multi-region submission carries the names in the regions list; any of
	// them may become the canonical name, so all must be free.
	if raw, ok := p["regions"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				if n, _ := m["name"].(string); strings.TrimSpace(n) != "" {
					names = append(names, strings.TrimSpace(n))
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	g, err := s.repo.GetGame(ctx, gameID, "")
	if err != nil {
		return nil
	}
	for _, name := range names {
		conflictID, err := s.repo.ConflictingGameID(ctx, g.SystemID, name, gameID)
		if err != nil {
			return err
		}
		if conflictID != uuid.Nil {
			return &GameNameConflictError{GameID: conflictID, SystemID: g.SystemID, Name: name}
		}
	}
	return nil
}

// ApproveMetadataSubmission approves a pending contribution and applies it.
// ApproveMetadataSubmission applies a pending metadata submission. Approve and
// reject of the same submission are serialized (see repository.WithReviewLock),
// so a double click can't apply it twice.
func (s *Service) ApproveMetadataSubmission(ctx context.Context, id, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	var out *models.MetadataSubmission
	err := s.repo.WithReviewLock(ctx, id, func() error {
		var err error
		out, err = s.approveMetadataSubmission(ctx, id, adminID, comment)
		return err
	})
	return out, err
}

func (s *Service) approveMetadataSubmission(ctx context.Context, id, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	sub, err := s.repo.GetMetadataSubmission(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("submission not found")
	}
	if sub.Status != models.MetadataPending {
		return nil, apperr.Conflict("submission is not pending")
	}
	if err := s.blockSelfReview(ctx, &sub.UserID, adminID); err != nil {
		return nil, err
	}
	// A duplicate report asks for the whole game to be removed. On approval the
	// game and its media are deleted instead of applying field/media changes.
	if isDeleteGamePayload(sub.Payload) {
		if err := s.deleteDuplicateGame(ctx, sub); err != nil {
			return nil, err
		}
		return s.repo.SetMetadataStatusForAdmin(ctx, id, models.MetadataApproved, adminID, comment)
	}
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.validateApprovedMedia(ctx, files); err != nil {
		return nil, err
	}
	// A rename must not collide with another game's name in the same system (the
	// games table enforces a unique (system_id, name) constraint).
	if sub.GameID != nil {
		if err := s.ensureGameNameAvailable(ctx, *sub.GameID, sub.Payload); err != nil {
			return nil, err
		}
	}
	// A new game has no row yet: create it and point the submission at it, then
	// the regular media/field apply path below runs against the new game.
	if sub.Kind == models.MetadataKindNewGame {
		var p map[string]any
		if err := json.Unmarshal(sub.Payload, &p); err != nil {
			return nil, apperr.Invalid("invalid submission payload")
		}
		if sub.SystemID == nil {
			return nil, apperr.Invalid("new game submission has no system")
		}
		gameID, err := s.repo.CreateGameFromPayload(ctx, *sub.SystemID, p)
		if err != nil {
			return nil, err
		}
		if err := s.repo.SetMetadataSubmissionGame(ctx, id, gameID); err != nil {
			return nil, err
		}
		sub.GameID = &gameID
		sub.SystemID = nil
	}
	// A cover/logo submitted without a region belongs to the game's primary
	// region, so it replaces that region's asset instead of stacking a second one.
	// The files are re-read so the resolved region is used by the steps below.
	if err := s.resolveSubmissionMediaRegions(ctx, sub); err != nil {
		return nil, err
	}
	if files, err = s.repo.ListMetadataSubmissionFiles(ctx, id); err != nil {
		return nil, err
	}
	// Snapshot the target's current text and media before the approval overwrites
	// them, so the review detail can still show the "old" side afterwards.
	if sub.Kind != models.MetadataKindNewGame {
		s.captureOldState(ctx, sub, files)
	}
	if err := s.moveSubmissionMediaToCanonical(ctx, id, sub); err != nil {
		return nil, err
	}
	if err := s.repo.ApplyMetadataSubmission(ctx, id); err != nil {
		return nil, err
	}
	if err := s.translateApprovedDescription(ctx, sub); err != nil {
		// A worker failure must not block approval: the English description is
		// already live, translations can be regenerated later.
		log.Warn().Err(err).Str("submission", id.String()).Msg("auto-translation skipped on approve")
	}
	updated, err := s.repo.SetMetadataStatusForAdmin(ctx, id, models.MetadataApproved, adminID, comment)
	if err != nil {
		return nil, err
	}
	if err := s.awardMetadataPoints(ctx, id, sub); err != nil {
		return nil, err
	}
	return updated, nil
}

// isDeleteGamePayload reports whether the submission asks for the whole game to
// be removed (a duplicate report) rather than a field or media change.
func isDeleteGamePayload(payload json.RawMessage) bool {
	var p map[string]any
	if json.Unmarshal(payload, &p) != nil {
		return false
	}
	v, _ := p["delete_game"].(bool)
	return v
}

// deleteDuplicateGame removes a game flagged as a duplicate: its media objects
// in R2 and the game row (regions, roms, media and translations cascade). The
// game's submissions are detached first so the review history survives.
func (s *Service) deleteDuplicateGame(ctx context.Context, sub *models.MetadataSubmission) error {
	if sub.GameID == nil {
		return apperr.Invalid("deletion request has no game")
	}
	media, err := s.repo.ListMediaByGame(ctx, *sub.GameID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(media))
	for _, m := range media {
		keys = append(keys, m.ObjectKey)
	}
	if len(keys) > 0 {
		if err := s.r2.DeleteObjects(ctx, keys); err != nil {
			return fmt.Errorf("delete game media: %w", err)
		}
	}
	return s.repo.DeleteGame(ctx, *sub.GameID)
}

// translateApprovedDescription calls the translate worker with the approved
// English description (when the submission carries one) and stores the returned
// translations for the target game or system. Only rows whose lang is enabled in
// the `lang` table are written, so disabled languages are skipped.
func (s *Service) translateApprovedDescription(ctx context.Context, sub *models.MetadataSubmission) error {
	if s.translator == nil {
		return nil
	}
	var p map[string]any
	if err := json.Unmarshal(sub.Payload, &p); err != nil {
		return nil // not a text payload
	}
	desc, ok := p["description"].(string)
	if !ok || strings.TrimSpace(desc) == "" {
		return nil
	}
	_, err := s.translateAndStoreDescription(ctx, desc, sub.GameID, sub.SystemID)
	return err
}

// translateAndStoreDescription translates an English description and upserts the
// result for the target game or system, returning how many languages were
// stored. Only languages enabled in the `lang` table are written, so disabled
// languages are never re-inserted.
func (s *Service) translateAndStoreDescription(ctx context.Context, desc string, gameID *uuid.UUID, systemID *string) (int, error) {
	if s.translator == nil {
		return 0, apperr.Unavailable("translator is not configured")
	}
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return 0, nil
	}
	translations, err := s.translator.Translate(ctx, desc)
	if err != nil {
		return 0, err
	}
	if len(translations) == 0 {
		return 0, nil
	}

	enabled := map[string]bool{}
	langs, err := s.repo.ListLanguages(ctx)
	if err != nil {
		return 0, err
	}
	for _, l := range langs {
		enabled[l.Code] = true
	}
	filtered := make(map[string]string, len(translations))
	for code, text := range translations {
		if enabled[code] {
			filtered[code] = text
		}
	}
	if len(filtered) == 0 {
		return 0, nil
	}

	if gameID != nil {
		return len(filtered), s.repo.UpsertGameTranslations(ctx, *gameID, filtered)
	}
	if systemID != nil {
		return len(filtered), s.repo.UpsertSystemTranslations(ctx, *systemID, filtered)
	}
	return 0, nil
}

// RetranslateGame regenerates every translation of a game from its current
// English description. Used to repair descriptions translated before the worker
// handled long text correctly.
func (s *Service) RetranslateGame(ctx context.Context, gameID uuid.UUID) (int, error) {
	g, err := s.repo.GetGame(ctx, gameID, "")
	if err != nil {
		return 0, apperr.NotFound("game not found")
	}
	if strings.TrimSpace(g.Description) == "" {
		return 0, apperr.Invalid("game has no description to translate")
	}
	if n := len([]rune(strings.TrimSpace(g.Description))); n > MaxDescriptionLength {
		return 0, fmt.Errorf("description exceeds %d characters (%d); trim it first", MaxDescriptionLength, n)
	}
	return s.translateAndStoreDescription(ctx, g.Description, &gameID, nil)
}

// RetranslateSystem regenerates every translation of a system description.
func (s *Service) RetranslateSystem(ctx context.Context, systemID string) (int, error) {
	sys, err := s.repo.GetMetadataSystem(ctx, systemID)
	if err != nil {
		return 0, apperr.NotFound("system not found")
	}
	if strings.TrimSpace(sys.Description) == "" {
		return 0, apperr.Invalid("system has no description to translate")
	}
	if n := len([]rune(strings.TrimSpace(sys.Description))); n > MaxDescriptionLength {
		return 0, fmt.Errorf("description exceeds %d characters (%d); trim it first", MaxDescriptionLength, n)
	}
	return s.translateAndStoreDescription(ctx, sys.Description, nil, &systemID)
}

// metadataBasePoints is the XP a metadata submission earns before the donor
// boost: one award per filled text field, per media file and, for a new game,
// the new-game bonus. `kinds` may repeat (one entry per file).
func metadataBasePoints(points economy.Points, payload json.RawMessage, kind string, kinds []string) int {
	var delta int
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err == nil {
		for _, k := range textMetadataKeys {
			if v, ok := p[k]; ok {
				switch val := v.(type) {
				case string:
					if strings.TrimSpace(val) != "" {
						delta += points.TextMetadata
					}
				case float64:
					if val > 0 {
						delta += points.TextMetadata
					}
				}
			}
		}
	}
	for _, k := range kinds {
		if k == models.MediaVideo {
			delta += points.VideoMetadata
		} else {
			delta += points.ImageMetadata
		}
	}
	if kind == models.MetadataKindNewGame {
		delta += points.NewGame
	}
	return delta
}

// awardMetadataPoints credits the submitter for an approved metadata
// contribution: 10 per text field, 50 per image file, 100 per video file and an
// extra bonus for a new game. The donor XP boost is applied by awardXP.
func (s *Service) awardMetadataPoints(ctx context.Context, id uuid.UUID, sub *models.MetadataSubmission) error {
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, id)
	if err != nil {
		log.Warn().Err(err).Str("submission", id.String()).Msg("failed to list submission files for points")
	}
	kinds := make([]string, 0, len(files))
	for _, f := range files {
		if f.IsDelete {
			continue // removing content does not earn points
		}
		kinds = append(kinds, f.Kind)
	}
	delta := metadataBasePoints(s.repo.Economy().Points, sub.Payload, sub.Kind, kinds)
	if delta == 0 {
		return nil
	}
	return s.awardXP(ctx, sub.UserID, delta, "approved_metadata", &id)
}

// captureOldState snapshots the target's published text and media just before
// an approval overwrites them, so the review detail can still show the "old"
// side afterwards. The replaced media objects are moved to the history/ prefix
// in R2 so they are preserved (like rejected/), out of the orphan-cleanup path.
func (s *Service) captureOldState(ctx context.Context, sub *models.MetadataSubmission, files []models.MetadataSubmissionFile) {
	oldPayload := map[string]any{}
	if sub.GameID != nil {
		if g, err := s.repo.GetGame(ctx, *sub.GameID, ""); err == nil {
			oldPayload = map[string]any{
				"name":          g.Name,
				"description":   g.Description,
				"genre":         g.Genre,
				"developer":     g.Developer,
				"publisher":     g.Publisher,
				"release_year":  g.ReleaseYear,
				"release_month": g.ReleaseMonth,
				"rating":        g.Rating,
				"type":          g.Type,
			}
			if roms, rerr := s.repo.ListRomsByGame(ctx, *sub.GameID); rerr == nil {
				oldPayload["roms"] = roms
			}
		}
	} else if sub.SystemID != nil {
		if sys, err := s.repo.GetMetadataSystem(ctx, *sub.SystemID); err == nil {
			oldPayload = map[string]any{"description": sys.Description, "region": sys.Region}
		}
	}

	replaced := map[string]bool{}
	for _, f := range files {
		replaced[f.Kind+"\x00"+f.Region] = true
	}
	var media []models.Media
	var err error
	if sub.GameID != nil {
		media, err = s.repo.ListMediaByGame(ctx, *sub.GameID)
	} else if sub.SystemID != nil {
		media, err = s.repo.ListMediaBySystem(ctx, *sub.SystemID)
	}
	oldMedia := []map[string]any{}
	if err == nil {
		for _, m := range media {
			if !replaced[m.Kind+"\x00"+m.Region] {
				continue
			}
			key := m.ObjectKey
			hist := "history/" + key
			if cerr := s.r2.CopyObject(ctx, key, hist); cerr == nil {
				if derr := s.r2.DeleteObject(ctx, key); derr != nil {
					log.Warn().Err(derr).Str("object", key).Msg("failed to delete replaced media after history copy")
				}
				key = hist
			} else {
				log.Warn().Err(cerr).Str("object", m.ObjectKey).Msg("failed to preserve replaced media in history")
			}
			oldMedia = append(oldMedia, map[string]any{
				"kind":       m.Kind,
				"region":     m.Region,
				"object_key": key,
				"mime":       m.Mime,
				"size":       m.Size,
				"created_at": m.CreatedAt,
			})
		}
	}

	pb, err := json.Marshal(oldPayload)
	if err != nil {
		return
	}
	mb, err := json.Marshal(oldMedia)
	if err != nil {
		return
	}
	if err := s.repo.SetMetadataSubmissionOldState(ctx, sub.ID, pb, mb); err != nil {
		log.Warn().Err(err).Str("submission", sub.ID.String()).Msg("failed to store old state snapshot")
	}
}

// moveSubmissionMediaToCanonical moves approved submission media objects from
// media/submissions/{id}/... to their canonical location (media/games/... for a
// game, media/systems/... for a system) so the published asset does not live
// under the submissions prefix. The submission file rows are updated and the
// original submission object is deleted.
func (s *Service) moveSubmissionMediaToCanonical(ctx context.Context, id uuid.UUID, sub *models.MetadataSubmission) error {
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	var systemID string
	if sub.GameID != nil {
		g, err := s.repo.GetGame(ctx, *sub.GameID, "")
		if err != nil {
			return fmt.Errorf("lookup submission game: %w", err)
		}
		systemID = g.SystemID
	} else if sub.SystemID != nil {
		systemID = *sub.SystemID
	}

	// Remove the previous canonical objects for the kinds being replaced so
	// approved media does not accumulate old versions.
	s.deleteReplacedMediaObjects(ctx, sub, files)

	for _, f := range files {
		// A moved existing media has nothing to copy or convert (only its region
		// changes) and a delete has no object at all.
		if f.IsMove || f.IsDelete {
			continue
		}
		// Videos are re-encoded to MP4 (HEVC + AAC) at the original resolution
		// with nearest-neighbour scaling; the canonical object is always .mp4.
		if f.Kind == models.MediaVideo {
			if err := s.convertVideoToCanonical(ctx, f, sub, systemID); err != nil {
				return err
			}
			continue
		}
		// Images are normalized server-side (format, crop and size) so a bad
		// client conversion can never publish a non-WebP asset. The canonical
		// object is always .webp with a random immutable key, so no cache
		// invalidation is needed (the old object is deleted above).
		data, err := s.r2.DownloadObject(ctx, f.ObjectKey, maxUploadSize(f.Kind, f.FileName))
		if err != nil {
			return fmt.Errorf("download %s %s: %w", f.Kind, f.ObjectKey, err)
		}
		normalized, err := video.NormalizeImage(ctx, data, metadataImageSpec(f.Kind))
		if err != nil {
			return fmt.Errorf("normalize %s: %w", f.Kind, err)
		}
		dest := immutableMediaKey(sub, systemID, f.Kind, ".webp")
		if err := s.r2.UploadFileCached(ctx, dest, "image/webp", mediaCacheControl, bytes.NewReader(normalized)); err != nil {
			return fmt.Errorf("upload normalized %s %s: %w", f.Kind, dest, err)
		}
		if err := s.r2.DeleteObject(ctx, f.ObjectKey); err != nil {
			log.Warn().Err(err).Str("object", f.ObjectKey).Msg("failed to delete staging media")
		}
		if err := s.repo.UpdateMetadataSubmissionFileObjectKey(ctx, f.ID, dest); err != nil {
			return err
		}
		if err := s.repo.UpdateMetadataSubmissionFileMime(ctx, f.ID, "image/webp"); err != nil {
			return err
		}
	}
	return nil
}

// deleteReplacedMediaObjects removes the R2 objects of the current media for the
// kinds being replaced by an approved submission.
func (s *Service) deleteReplacedMediaObjects(ctx context.Context, sub *models.MetadataSubmission, files []models.MetadataSubmissionFile) {
	// A file replaces the existing media for the same (kind, region).
	replaced := map[string]bool{}
	for _, f := range files {
		replaced[f.Kind+"\x00"+f.Region] = true
	}
	var old []models.Media
	var err error
	if sub.GameID != nil {
		old, err = s.repo.ListMediaByGame(ctx, *sub.GameID)
	} else if sub.SystemID != nil {
		old, err = s.repo.ListMediaBySystem(ctx, *sub.SystemID)
	}
	if err != nil {
		log.Warn().Err(err).Msg("failed to list replaced media")
		return
	}
	for _, m := range old {
		if !replaced[m.Kind+"\x00"+m.Region] {
			continue
		}
		if err := s.r2.DeleteObject(ctx, m.ObjectKey); err != nil {
			log.Warn().Err(err).Str("object", m.ObjectKey).Msg("failed to delete replaced media object")
		}
	}
}

// videoProbeTimeout bounds the create-time download and probe of a video.
const videoProbeTimeout = 2 * time.Minute

// Videos are held in memory while they are probed or converted, so only a few
// are handled at once. Contributors' create-time probes and reviewers'
// approvals use separate slots, so neither can hold up the other.
var (
	probeSlots  = make(chan struct{}, 2)
	reviewSlots = make(chan struct{}, 2)
)

// errVideoBusy is returned when no video slot frees up before the deadline.
var errVideoBusy = errors.New("the server is busy processing other videos, please try again in a few minutes")

// acquireVideoSlot waits for a free slot in slots; the returned func releases it.
func acquireVideoSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, errVideoBusy
	}
}

// checkVideoEntries checks a submission's video entries before anything is
// downloaded: at most one video, with a video extension, uploaded by this user
// to their own staging area, and within maxVideoSize (by HEAD).
func (s *Service) checkVideoEntries(ctx context.Context, userID uuid.UUID, files []models.MetadataUploadRequest) error {
	videos := 0
	for _, f := range files {
		if f.Delete || f.Move || f.Kind != models.MediaVideo {
			continue
		}
		if videos++; videos > 1 {
			return apperr.Invalid("only one video can be submitted at a time")
		}
		if ext := strings.ToLower(getExt(f.FileName)); !validMediaExt(f.Kind, ext) {
			return fmt.Errorf("unsupported %s extension %q", mediaExtLabel(f.Kind), ext)
		}
		if !isOwnStagingKey(f.ObjectKey, userID) {
			return apperr.Invalid("invalid upload reference")
		}
		if err := s.ensureUploaded(ctx, f.ObjectKey, f.FileName); err != nil {
			return err
		}
	}
	return nil
}

// probeVideo downloads a submission video from R2 and inspects it with ffprobe.
func (s *Service) probeVideo(ctx context.Context, objectKey string) (*models.VideoMeta, error) {
	release, err := acquireVideoSlot(ctx, probeSlots)
	if err != nil {
		return nil, err
	}
	defer release()
	data, err := s.r2.DownloadObject(ctx, objectKey, maxVideoSize)
	if err != nil {
		return nil, err
	}
	return video.Probe(ctx, data)
}

// convertVideoToCanonical downloads the user's uploaded video, re-encodes it to
// MP4 (HEVC + AAC, nearest-neighbour scaling, original resolution, 45s cap)
// and uploads it to the canonical media/games or media/systems key as .mp4.
// The submission's original object is removed and the file row is updated.
func (s *Service) convertVideoToCanonical(ctx context.Context, f models.MetadataSubmissionFile, sub *models.MetadataSubmission, systemID string) error {
	release, err := acquireVideoSlot(ctx, reviewSlots)
	if err != nil {
		return err
	}
	defer release()
	data, err := s.r2.DownloadObject(ctx, f.ObjectKey, maxVideoSize)
	if err != nil {
		return fmt.Errorf("download video %s: %w", f.ObjectKey, err)
	}
	converted, err := video.ConvertToMP4(ctx, data)
	if err != nil {
		return err
	}

	dest := immutableMediaKey(sub, systemID, f.Kind, ".mp4")
	if err := s.r2.UploadFileCached(ctx, dest, "video/mp4", mediaCacheControl, bytes.NewReader(converted)); err != nil {
		return fmt.Errorf("upload converted video %s: %w", dest, err)
	}
	if err := s.r2.DeleteObject(ctx, f.ObjectKey); err != nil {
		log.Warn().Err(err).Str("object", f.ObjectKey).Msg("failed to delete original video")
	}
	if err := s.repo.UpdateMetadataSubmissionFileObjectKey(ctx, f.ID, dest); err != nil {
		return err
	}
	if err := s.repo.UpdateMetadataSubmissionFileMime(ctx, f.ID, "video/mp4"); err != nil {
		return err
	}
	log.Info().Str("src", f.ObjectKey).Str("dest", dest).Msg("video converted to mp4 (hevc)")
	return nil
}

// RejectMetadataSubmission rejects a pending contribution, preserving the files
// it uploaded under rejected/.
// RejectMetadataSubmission rejects a pending metadata submission. Approve and
// reject of the same submission are serialized (see repository.WithReviewLock).
func (s *Service) RejectMetadataSubmission(ctx context.Context, id, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	var out *models.MetadataSubmission
	err := s.repo.WithReviewLock(ctx, id, func() error {
		var err error
		out, err = s.rejectMetadataSubmission(ctx, id, adminID, comment)
		return err
	})
	return out, err
}

func (s *Service) rejectMetadataSubmission(ctx context.Context, id, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	sub, err := s.repo.GetMetadataSubmission(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("submission not found")
	}
	if sub.Status != models.MetadataPending {
		return nil, apperr.Conflict("submission is not pending")
	}
	if err := s.blockSelfReview(ctx, &sub.UserID, adminID); err != nil {
		return nil, err
	}
	files, err := s.repo.ListMetadataSubmissionFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := preserveRejectedSubmissionFiles(ctx, s.r2, sub, files, func(fileID uuid.UUID, objectKey string) error {
		return s.repo.UpdateMetadataSubmissionFileObjectKey(ctx, fileID, objectKey)
	}); err != nil {
		return nil, err
	}
	return s.repo.SetMetadataStatusForAdmin(ctx, id, models.MetadataRejected, adminID, comment)
}

// preserveRejectedSubmissionFiles moves the files a rejected submission
// uploaded to rejected/ (not deleted, so the review history keeps the
// submitted art) and records each new key with setKey. Everything else is left
// where it is: move and delete entries don't name an upload of this
// submission, and rejecting must never touch published objects.
func preserveRejectedSubmissionFiles(ctx context.Context, r2c r2.Client, sub *models.MetadataSubmission, files []models.MetadataSubmissionFile, setKey func(fileID uuid.UUID, objectKey string) error) error {
	for _, f := range files {
		if !ownedSubmissionUpload(f, sub) {
			continue
		}
		newKey, err := preserveRejectedFile(ctx, r2c, f.ObjectKey)
		if err != nil {
			return fmt.Errorf("preserve rejected file %s: %w", f.ObjectKey, err)
		}
		if err := setKey(f.ID, newKey); err != nil {
			return err
		}
	}
	return nil
}

func validMediaKind(kind string) bool {
	switch kind {
	case models.MediaCover, models.MediaBoxFront, models.MediaBoxBack, models.MediaScreenshot,
		models.MediaLogo, models.MediaFanart, models.MediaVideo:
		return true
	default:
		return false
	}
}

// validMediaExt reports whether an extension is accepted for a media kind:
// images for the image kinds, video containers for video.
func validMediaExt(kind, ext string) bool {
	if kind == models.MediaVideo {
		return videoExts[ext]
	}
	return mediaExts[ext]
}

// mediaExtLabel returns the human label for a media kind's file type, used in
// validation error messages.
func mediaExtLabel(kind string) string {
	if kind == models.MediaVideo {
		return "video"
	}
	return "image"
}
