package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"neoassets/internal/models"
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
// importer), independent from the art-pack systems.
func (s *Service) ListMetadataSystems() ([]models.MetadataSystem, error) {
	return s.repo.ListMetadataSystems()
}

// ListSystemIDsByFamily returns the ids of every system in a family (e.g.
// "arcade"), ordered by name.
func (s *Service) ListSystemIDsByFamily(family string) ([]string, error) {
	return s.repo.ListSystemIDsByFamily(family)
}

// ListSystemIDsByGroup returns the ids of every system in a group (e.g.
// "mame-fbneo"), ordered by name.
func (s *Service) ListSystemIDsByGroup(group string) ([]string, error) {
	return s.repo.ListSystemIDsByGroup(group)
}

// ListFamilies returns every non-empty system family with its counts.
func (s *Service) ListFamilies() ([]models.MetadataFamily, error) {
	return s.repo.ListFamilies()
}

// ListGroups returns every non-empty system group with its counts.
func (s *Service) ListGroups() ([]models.MetadataGroup, error) {
	return s.repo.ListGroups()
}

// ListGamesBySystem returns the games for a system with paging and optional
// game-type filter (base/hack/homebrew) and sort order.
func (s *Service) ListGamesBySystem(systemID string, limit, offset int, gtype, sort string) ([]models.Game, int64, error) {
	list, total, err := s.repo.ListGamesBySystem(systemID, limit, offset, gtype, sort)
	if err != nil {
		return nil, 0, err
	}
	if err := s.decorateGameStats(list); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// SearchGames searches the game catalog.
func (s *Service) SearchGames(q, systemID, gtype, sort string, limit, offset int) ([]models.Game, int64, error) {
	list, total, err := s.repo.SearchGames(q, systemID, gtype, sort, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if err := s.decorateGameStats(list); err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

// decorateGameStats fills the computed metadata status flags on a game list.
func (s *Service) decorateGameStats(list []models.Game) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
	}
	stats, err := s.repo.ListGameStats(ids)
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
func (s *Service) GetGame(id uuid.UUID, lang string) (*models.GameDetail, error) {
	g, err := s.repo.GetGame(id, lang)
	if err != nil {
		return nil, fmt.Errorf("game not found")
	}
	roms, err := s.repo.ListRomsByGame(id)
	if err != nil {
		return nil, err
	}
	media, err := s.repo.ListMediaByGame(id)
	if err != nil {
		return nil, err
	}
	translations, err := s.repo.ListGameTranslations(id)
	if err != nil {
		return nil, err
	}
	regions, err := s.repo.ListGameRegions(id)
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
		regions[i].Media = byRegion[regions[i].Region]
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

// ListGameContributors returns the users with approved metadata contributions
// for a game, ordered by contribution count. It is kept out of GetGame so the
// hot scrape path does not pay for the extra aggregate query.
func (s *Service) ListGameContributors(gameID uuid.UUID) ([]models.UserCountStat, error) {
	return s.repo.ListGameContributors(gameID)
}

// ListLanguages returns all enabled description languages.
func (s *Service) ListLanguages() ([]models.Language, error) {
	return s.repo.ListLanguages()
}

// ListGenres returns the canonical genre catalog used by the web forms.
func (s *Service) ListGenres() ([]models.Genre, error) {
	return s.repo.ListGenres()
}

// ListRegions returns the canonical region catalog used by the web forms.
func (s *Service) ListRegions() ([]models.Region, error) {
	return s.repo.ListRegions()
}

// validateRegion checks that a non-empty region is in the canonical catalog.
func (s *Service) validateRegion(region string) error {
	region = strings.TrimSpace(region)
	if region == "" {
		return nil
	}
	exists, err := s.repo.RegionExists(region)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("unknown region %q", region)
	}
	return nil
}

// LookupGame finds games by any of the given ROM hashes, optionally scoped to a
// system (empty systemID searches all systems).
func (s *Service) LookupGame(crc, md5, sha1, sha256, systemID string) ([]models.Game, error) {
	return s.repo.LookupGameByHash(crc, md5, sha1, sha256, systemID)
}

// ---------------------------------------------------------------------------
// Metadata contributions
// ---------------------------------------------------------------------------

// CreateMetadataSubmission creates a contribution already in pending review
// (drafts are kept client-side, so only submissions for review hit the DB).
// Media files listed in req.Files are registered against the submission.
func (s *Service) CreateMetadataSubmission(userID uuid.UUID, req models.MetadataSubmissionRequest) (*models.MetadataSubmission, error) {
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "edit"
	}
	if kind != "edit" && kind != "new_game" {
		return nil, fmt.Errorf("invalid contribution kind")
	}
	if req.GameID == nil && req.SystemID == nil {
		return nil, fmt.Errorf("a game or system must be provided")
	}
	if req.GameID != nil {
		if _, err := s.repo.GetGame(*req.GameID, ""); err != nil {
			return nil, fmt.Errorf("game not found")
		}
	}
	if req.SystemID != nil {
		if _, err := s.repo.GetMetadataSystem(*req.SystemID); err != nil {
			return nil, fmt.Errorf("system not found")
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
			exists, err := s.repo.GenreExists(g)
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
		if err := s.validateRegion(r); err != nil {
			return nil, err
		}
	}
	for _, f := range req.Files {
		if err := s.validateRegion(f.Region); err != nil {
			return nil, err
		}
	}

	if kind == "new_game" {
		if req.GameID != nil {
			return nil, fmt.Errorf("a new game cannot target an existing game")
		}
		name, _ := req.Payload["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("game name is required")
		}
		if v, ok := req.Payload["type"].(string); ok {
			switch v {
			case "base", "hack", "homebrew":
			default:
				return nil, fmt.Errorf("invalid game type %q", v)
			}
		}
		exists, err := s.repo.GameExistsByName(*req.SystemID, name)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("a game with that name already exists in this system")
		}
		// Block a duplicate pending new game with the same name in this system.
		subs, _, err := s.repo.ListMetadataSubmissionsByUser(userID, "", 0, 0)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			if sub.Kind != "new_game" || sub.Status != models.MetadataPending || sub.SystemID == nil || *sub.SystemID != *req.SystemID {
				continue
			}
			var p map[string]any
			if json.Unmarshal(sub.Payload, &p) == nil {
				if n, _ := p["name"].(string); strings.EqualFold(strings.TrimSpace(n), name) {
					return nil, fmt.Errorf("you already have a pending submission for this game")
				}
			}
		}
	} else {
		// Block a new submission for a field/kind that already has a pending
		// submission for this game/system from the same user.
		pending, err := s.pendingSubmissionKeys(userID, req.GameID, req.SystemID)
		if err != nil {
			return nil, err
		}
		for k := range req.Payload {
			if k == "note" {
				continue
			}
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
		return nil, fmt.Errorf("invalid payload")
	}
	id, err := s.repo.CreateMetadataSubmission(req.GameID, req.SystemID, userID, kind, payload)
	if err != nil {
		return nil, err
	}
	for _, f := range req.Files {
		if !validMediaKind(f.Kind) {
			return nil, fmt.Errorf("invalid media kind %q", f.Kind)
		}
		ext := strings.ToLower(getExt(f.FileName))
		if !validMediaExt(f.Kind, ext) {
			return nil, fmt.Errorf("unsupported %s extension %q", mediaExtLabel(f.Kind), ext)
		}
		if f.ObjectKey == "" {
			return nil, fmt.Errorf("object_key required")
		}
		// Only staging uploads may be registered on a submission; canonical
		// media keys are immutable and reserved for approved content.
		if !strings.HasPrefix(f.ObjectKey, "media/staging/") {
			return nil, fmt.Errorf("object_key must be a staged media upload")
		}
		if err := s.ensureUploaded(context.Background(), f.ObjectKey, f.FileName); err != nil {
			return nil, err
		}
		mime := f.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		var vmeta *models.VideoMeta
		if f.Kind == models.MediaVideo {
			vm, err := s.probeVideo(context.Background(), f.ObjectKey)
			if err != nil {
				log.Warn().Str("object", f.ObjectKey).Err(err).Msg("video probe failed")
			} else {
				vmeta = vm
			}
		}
		if err := s.repo.AddMetadataSubmissionFile(id, f.Kind, f.ObjectKey, f.FileName, mime, f.Region, f.Size, vmeta); err != nil {
			return nil, err
		}
	}
	return s.repo.GetMetadataSubmissionForUser(id, userID)
}

// pendingSubmissionKeys returns the set of payload fields and media kinds that
// already have a pending submission for the given user and game/system.
func (s *Service) pendingSubmissionKeys(userID uuid.UUID, gameID *uuid.UUID, systemID *string) (map[string]bool, error) {
	subs, _, err := s.repo.ListMetadataSubmissionsByUser(userID, "", 0, 0)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, sub := range subs {
		if sub.Status != models.MetadataPending || sub.Kind == "new_game" {
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
			for k := range p {
				if k == "note" {
					continue
				}
				keys[k] = true
			}
		}
		files, err := s.repo.ListMetadataSubmissionFiles(sub.ID)
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
func (s *Service) PendingMetadataKeys(userID, gameID uuid.UUID) ([]string, error) {
	keys, err := s.pendingSubmissionKeys(userID, &gameID, nil)
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
func (s *Service) MetadataUploadURLPre(ctx context.Context, req models.MetadataUploadURLRequest) (*models.UploadResponse, error) {
	if req.GameID == nil && req.SystemID == nil {
		return nil, fmt.Errorf("a game or system must be provided")
	}
	if !validMediaKind(req.Kind) {
		return nil, fmt.Errorf("invalid media kind %q", req.Kind)
	}
	ext := strings.ToLower(getExt(req.FileName))
	if !validMediaExt(req.Kind, ext) {
		return nil, fmt.Errorf("unsupported %s extension %q", mediaExtLabel(req.Kind), ext)
	}
	if req.MimeType == "" {
		req.MimeType = "application/octet-stream"
	}
	if req.GameID != nil {
		if _, err := s.repo.GetGame(*req.GameID, ""); err != nil {
			return nil, fmt.Errorf("game not found")
		}
	} else if req.SystemID != nil {
		if _, err := s.repo.GetMetadataSystem(*req.SystemID); err != nil {
			return nil, fmt.Errorf("system not found")
		}
	}
	objectKey := stagingMediaKey(req.Kind, req.FileName)
	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, objectKey, req.MimeType, s.uploadTTL)
	if err != nil {
		return nil, err
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

// stagingMediaKey builds a temporary upload key under media/staging/.
func stagingMediaKey(kind, fileName string) string {
	name := filepath.Base(fileName)
	if name == "" || name == "." {
		name = kind
	}
	return fmt.Sprintf("media/staging/%s/%s/%s", uuid.NewString(), kind, name)
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
	sub, err := s.repo.GetMetadataSubmissionForUser(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.MetadataCreated && sub.Status != models.MetadataPending {
		return nil, fmt.Errorf("submission is not editable")
	}
	if !validMediaKind(req.Kind) {
		return nil, fmt.Errorf("invalid media kind %q", req.Kind)
	}
	ext := strings.ToLower(getExt(req.FileName))
	if !validMediaExt(req.Kind, ext) {
		return nil, fmt.Errorf("unsupported %s extension %q", mediaExtLabel(req.Kind), ext)
	}
	if req.MimeType == "" {
		req.MimeType = "application/octet-stream"
	}

	objectKey := fmt.Sprintf("media/submissions/%s/%s/%s", submissionID, req.Kind, filepath.Base(req.FileName))
	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, objectKey, req.MimeType, s.uploadTTL)
	if err != nil {
		return nil, err
	}
	if err := s.repo.AddMetadataSubmissionFile(submissionID, req.Kind, objectKey, req.FileName, req.MimeType, req.Region, req.Size, nil); err != nil {
		return nil, err
	}
	return &models.UploadResponse{
		UploadURL: uploadURL,
		ObjectKey: objectKey,
		ExpiresAt: time.Now().Add(s.uploadTTL),
	}, nil
}

// SubmitMetadataSubmission moves a draft contribution into pending review.
func (s *Service) SubmitMetadataSubmission(submissionID, userID uuid.UUID) (*models.MetadataSubmission, error) {
	sub, err := s.repo.GetMetadataSubmissionForUser(submissionID, userID)
	if err != nil {
		return nil, err
	}
	files, err := s.repo.ListMetadataSubmissionFiles(submissionID)
	if err != nil {
		return nil, err
	}
	// A submission must carry either an uploaded media file or a text field.
	if len(files) == 0 && !payloadHasText(sub.Payload) {
		return nil, fmt.Errorf("submission has no content")
	}
	return s.repo.SubmitMetadataSubmission(submissionID, userID)
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
	return false
}

// ListMyMetadataSubmissions lists the user's contributions.
func (s *Service) ListMyMetadataSubmissions(userID uuid.UUID, status string, limit, offset int) ([]models.MetadataSubmission, int64, int, error) {
	list, total, err := s.repo.ListMetadataSubmissionsByUser(userID, status, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := s.decorateMetadataNames(list); err != nil {
		return nil, 0, 0, err
	}
	if err := s.enrichMetadataSubmissions(list); err != nil {
		return nil, 0, 0, err
	}
	metadataXP, _, err := s.repo.UserAwardedXPBySource(userID)
	if err != nil {
		return nil, 0, 0, err
	}
	return list, total, metadataXP, nil
}

// ListMetadataSubmissions lists all contributions, optionally filtered. With no
// filter it returns the review history only (never client-side drafts).
func (s *Service) ListMetadataSubmissions(status string) ([]models.MetadataSubmission, error) {
	if status != "" {
		switch status {
		case models.MetadataCreated, models.MetadataPending, models.MetadataApproved, models.MetadataRejected:
		default:
			return nil, fmt.Errorf("invalid status filter")
		}
	}
	var list []models.MetadataSubmission
	var err error
	if status == "" {
		list, err = s.repo.ListMetadataReviewSubmissions()
	} else {
		list, err = s.repo.ListMetadataSubmissions(status)
	}
	if err != nil {
		return nil, err
	}
	if err := s.decorateMetadataNames(list); err != nil {
		return nil, err
	}
	if err := s.enrichMetadataSubmissions(list); err != nil {
		return nil, err
	}
	return list, nil
}

// enrichMetadataSubmissions fills the target game/system display data and the
// kinds of change for each contribution so the admin review list can render
// them without extra requests. It batches the lookups to avoid N+1 queries.
func (s *Service) enrichMetadataSubmissions(list []models.MetadataSubmission) error {
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
	games, err := s.repo.ListGamesByIDs(gameIDs)
	if err != nil {
		return err
	}
	for _, g := range games {
		gameByID[g.ID] = g
	}
	sysNames, err := s.repo.SystemNamesByIDs(sysIDs)
	if err != nil {
		return err
	}
	kinds, err := s.repo.MetadataSubmissionFileKindsByIDs(subIDs)
	if err != nil {
		return err
	}
	points, err := s.repo.SubmissionPointsByIDs(subIDs)
	if err != nil {
		return err
	}

	for i := range list {
		list[i].PointsEarned = points[list[i].ID]
		list[i].BasePointsEarned = metadataBasePoints(list[i].Payload, list[i].Kind, kinds[list[i].ID])
		if list[i].GameID != nil {
			if g, ok := gameByID[*list[i].GameID]; ok {
				list[i].GameName = g.Name
				list[i].SystemName = g.SystemName
				list[i].Cover = g.Cover
				list[i].CoverUpdated = g.CoverUpdated
			}
		}
		if list[i].SystemID != nil {
			list[i].SystemName = sysNames[*list[i].SystemID]
		}
		if list[i].Kind == "new_game" {
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
			for k := range p {
				if k == "note" || k == "release_month" {
					continue
				}
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
func (s *Service) GetMetadataSubmissionDetail(id uuid.UUID) (*models.MetadataSubmissionDetail, error) {
	sub, err := s.repo.GetMetadataSubmission(id)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	files, err := s.repo.ListMetadataSubmissionFiles(id)
	if err != nil {
		return nil, err
	}
	if err := s.decorateMetadataNames([]models.MetadataSubmission{*sub}); err != nil {
		return nil, err
	}
	detail := &models.MetadataSubmissionDetail{Submission: sub, Files: files, Media: []models.Media{}}
	switch {
	case sub.Kind == "new_game" && sub.SystemID != nil:
		// A new game has no current media; only the target system is shown.
		if sys, err := s.repo.GetMetadataSystem(*sub.SystemID); err == nil {
			detail.System = sys
		}
	case sub.GameID != nil:
		game, err := s.GetGame(*sub.GameID, "")
		if err == nil {
			detail.Game = game
			// All media (every region), so the reviewer can compare the region
			// being submitted instead of only the resolved primary one.
			if media, merr := s.repo.ListMediaByGame(*sub.GameID); merr == nil {
				detail.Media = media
			} else {
				detail.Media = game.Media
			}
		}
	case sub.SystemID != nil:
		if sys, err := s.repo.GetMetadataSystem(*sub.SystemID); err == nil {
			detail.System = sys
		}
		if media, err := s.repo.ListMediaBySystem(*sub.SystemID); err == nil {
			detail.Media = media
		}
	}
	return detail, nil
}

// decorateMetadataNames resolves the submitter and reviewer usernames.
func (s *Service) decorateMetadataNames(list []models.MetadataSubmission) error {
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
	names, err := s.repo.UserNamesByID(ids)
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
// above 60 fps, which are capped to 60 fps.
func (s *Service) validateApprovedMedia(ctx context.Context, files []models.MetadataSubmissionFile) error {
	for _, f := range files {
		data, err := s.r2.DownloadObject(ctx, f.ObjectKey)
		if err != nil {
			return fmt.Errorf("download %s: %w", f.Kind, err)
		}
		if f.Kind == models.MediaVideo {
			meta, err := video.Probe(ctx, data)
			if err != nil {
				return fmt.Errorf("probe video: %w", err)
			}
			if meta.FPS < 23 {
				return fmt.Errorf("video must be at least 23 fps, 60 recommended (detected %d fps)", meta.FPS)
			}
			continue
		}
		if _, err := video.ProbeImage(ctx, data); err != nil {
			return fmt.Errorf("probe %s image: %w", f.Kind, err)
		}
	}
	return nil
}

// ApproveMetadataSubmission approves a pending contribution and applies it.
func (s *Service) ApproveMetadataSubmission(id, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	sub, err := s.repo.GetMetadataSubmission(id)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.MetadataPending {
		return nil, fmt.Errorf("submission is not pending")
	}
	files, err := s.repo.ListMetadataSubmissionFiles(id)
	if err != nil {
		return nil, err
	}
	if err := s.validateApprovedMedia(context.Background(), files); err != nil {
		return nil, err
	}
	// A new game has no row yet: create it and point the submission at it, then
	// the regular media/field apply path below runs against the new game.
	if sub.Kind == "new_game" {
		var p map[string]any
		if err := json.Unmarshal(sub.Payload, &p); err != nil {
			return nil, fmt.Errorf("invalid submission payload")
		}
		if sub.SystemID == nil {
			return nil, fmt.Errorf("new game submission has no system")
		}
		gameID, err := s.repo.CreateGameFromPayload(*sub.SystemID, p)
		if err != nil {
			return nil, err
		}
		if err := s.repo.SetMetadataSubmissionGame(id, gameID); err != nil {
			return nil, err
		}
		sub.GameID = &gameID
		sub.SystemID = nil
	}
	// Snapshot the target's current text and media before the approval overwrites
	// them, so the review detail can still show the "old" side afterwards.
	if sub.Kind != "new_game" {
		s.captureOldState(context.Background(), sub, files)
	}
	if err := s.moveSubmissionMediaToCanonical(context.Background(), id, sub); err != nil {
		return nil, err
	}
	if err := s.repo.ApplyMetadataSubmission(id); err != nil {
		return nil, err
	}
	if err := s.translateApprovedDescription(sub); err != nil {
		// A worker failure must not block approval: the English description is
		// already live, translations can be regenerated later.
		log.Warn().Err(err).Str("submission", id.String()).Msg("auto-translation skipped on approve")
	}
	updated, err := s.repo.SetMetadataStatusForAdmin(id, models.MetadataApproved, adminID, comment)
	if err != nil {
		return nil, err
	}
	if err := s.awardMetadataPoints(id, sub); err != nil {
		return nil, err
	}
	return updated, nil
}

// translateApprovedDescription calls the translate worker with the approved
// English description (when the submission carries one) and stores the returned
// translations for the target game or system. Only rows whose lang is enabled in
// the `lang` table are written, so disabled languages are skipped.
func (s *Service) translateApprovedDescription(sub *models.MetadataSubmission) error {
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
	_, err := s.translateAndStoreDescription(context.Background(), desc, sub.GameID, sub.SystemID)
	return err
}

// translateAndStoreDescription translates an English description and upserts the
// result for the target game or system, returning how many languages were
// stored. Only languages enabled in the `lang` table are written, so disabled
// languages are never re-inserted.
func (s *Service) translateAndStoreDescription(ctx context.Context, desc string, gameID *uuid.UUID, systemID *string) (int, error) {
	if s.translator == nil {
		return 0, fmt.Errorf("translator is not configured")
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
	langs, err := s.repo.ListLanguages()
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
		return len(filtered), s.repo.UpsertGameTranslations(*gameID, filtered)
	}
	if systemID != nil {
		return len(filtered), s.repo.UpsertSystemTranslations(*systemID, filtered)
	}
	return 0, nil
}

// RetranslateGame regenerates every translation of a game from its current
// English description. Used to repair descriptions translated before the worker
// handled long text correctly.
func (s *Service) RetranslateGame(ctx context.Context, gameID uuid.UUID) (int, error) {
	g, err := s.repo.GetGame(gameID, "")
	if err != nil {
		return 0, fmt.Errorf("game not found")
	}
	if strings.TrimSpace(g.Description) == "" {
		return 0, fmt.Errorf("game has no description to translate")
	}
	if n := len([]rune(strings.TrimSpace(g.Description))); n > MaxDescriptionLength {
		return 0, fmt.Errorf("description exceeds %d characters (%d); trim it first", MaxDescriptionLength, n)
	}
	return s.translateAndStoreDescription(ctx, g.Description, &gameID, nil)
}

// RetranslateSystem regenerates every translation of a system description.
func (s *Service) RetranslateSystem(ctx context.Context, systemID string) (int, error) {
	sys, err := s.repo.GetMetadataSystem(systemID)
	if err != nil {
		return 0, fmt.Errorf("system not found")
	}
	if strings.TrimSpace(sys.Description) == "" {
		return 0, fmt.Errorf("system has no description to translate")
	}
	if n := len([]rune(strings.TrimSpace(sys.Description))); n > MaxDescriptionLength {
		return 0, fmt.Errorf("description exceeds %d characters (%d); trim it first", MaxDescriptionLength, n)
	}
	return s.translateAndStoreDescription(ctx, sys.Description, nil, &systemID)
}

// metadataBasePoints is the XP a metadata submission earns before the donor
// boost: one award per filled text field, per media file and, for a new game,
// the new-game bonus. `kinds` may repeat (one entry per file).
func metadataBasePoints(payload json.RawMessage, kind string, kinds []string) int {
	var delta int
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err == nil {
		for _, k := range textMetadataKeys {
			if v, ok := p[k]; ok {
				switch val := v.(type) {
				case string:
					if strings.TrimSpace(val) != "" {
						delta += PointsTextMetadata
					}
				case float64:
					if val > 0 {
						delta += PointsTextMetadata
					}
				}
			}
		}
	}
	for _, k := range kinds {
		if k == models.MediaVideo {
			delta += PointsVideoMetadata
		} else {
			delta += PointsImageMetadata
		}
	}
	if kind == "new_game" {
		delta += PointsNewGame
	}
	return delta
}

// awardMetadataPoints credits the submitter for an approved metadata
// contribution: 10 per text field, 50 per image file, 100 per video file and an
// extra bonus for a new game. The donor XP boost is applied by awardXP.
func (s *Service) awardMetadataPoints(id uuid.UUID, sub *models.MetadataSubmission) error {
	files, err := s.repo.ListMetadataSubmissionFiles(id)
	if err != nil {
		log.Warn().Err(err).Str("submission", id.String()).Msg("failed to list submission files for points")
	}
	kinds := make([]string, 0, len(files))
	for _, f := range files {
		kinds = append(kinds, f.Kind)
	}
	delta := metadataBasePoints(sub.Payload, sub.Kind, kinds)
	if delta == 0 {
		return nil
	}
	return s.awardXP(sub.UserID, delta, "approved_metadata", &id)
}

// captureOldState snapshots the target's published text and media just before
// an approval overwrites them, so the review detail can still show the "old"
// side afterwards. The replaced media objects are moved to the history/ prefix
// in R2 so they are preserved (like rejected/), out of the orphan-cleanup path.
func (s *Service) captureOldState(ctx context.Context, sub *models.MetadataSubmission, files []models.MetadataSubmissionFile) {
	oldPayload := map[string]any{}
	if sub.GameID != nil {
		if g, err := s.repo.GetGame(*sub.GameID, ""); err == nil {
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
		}
	} else if sub.SystemID != nil {
		if sys, err := s.repo.GetMetadataSystem(*sub.SystemID); err == nil {
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
		media, err = s.repo.ListMediaByGame(*sub.GameID)
	} else if sub.SystemID != nil {
		media, err = s.repo.ListMediaBySystem(*sub.SystemID)
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
	if err := s.repo.SetMetadataSubmissionOldState(sub.ID, pb, mb); err != nil {
		log.Warn().Err(err).Str("submission", sub.ID.String()).Msg("failed to store old state snapshot")
	}
}

// moveSubmissionMediaToCanonical moves approved submission media objects from
// media/submissions/{id}/... to their canonical location (media/games/... for a
// game, media/systems/... for a system) so the published asset does not live
// under the submissions prefix. The submission file rows are updated and the
// original submission object is deleted.
func (s *Service) moveSubmissionMediaToCanonical(ctx context.Context, id uuid.UUID, sub *models.MetadataSubmission) error {
	files, err := s.repo.ListMetadataSubmissionFiles(id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	var systemID string
	if sub.GameID != nil {
		g, err := s.repo.GetGame(*sub.GameID, "")
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
		data, err := s.r2.DownloadObject(ctx, f.ObjectKey)
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
		if err := s.repo.UpdateMetadataSubmissionFileObjectKey(f.ID, dest); err != nil {
			return err
		}
		if err := s.repo.UpdateMetadataSubmissionFileMime(f.ID, "image/webp"); err != nil {
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
		old, err = s.repo.ListMediaByGame(*sub.GameID)
	} else if sub.SystemID != nil {
		old, err = s.repo.ListMediaBySystem(*sub.SystemID)
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

// probeVideo downloads a submission video from R2 and inspects it with ffprobe.
func (s *Service) probeVideo(ctx context.Context, objectKey string) (*models.VideoMeta, error) {
	data, err := s.r2.DownloadObject(ctx, objectKey)
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
	data, err := s.r2.DownloadObject(ctx, f.ObjectKey)
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
	if err := s.repo.UpdateMetadataSubmissionFileObjectKey(f.ID, dest); err != nil {
		return err
	}
	if err := s.repo.UpdateMetadataSubmissionFileMime(f.ID, "video/mp4"); err != nil {
		return err
	}
	log.Info().Str("src", f.ObjectKey).Str("dest", dest).Msg("video converted to mp4 (hevc)")
	return nil
}

// RejectMetadataSubmission rejects a pending contribution and deletes its media
// files from R2.
func (s *Service) RejectMetadataSubmission(ctx context.Context, id, adminID uuid.UUID, comment string) (*models.MetadataSubmission, error) {
	sub, err := s.repo.GetMetadataSubmission(id)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.MetadataPending {
		return nil, fmt.Errorf("submission is not pending")
	}
	files, err := s.repo.ListMetadataSubmissionFiles(id)
	if err != nil {
		return nil, err
	}
	// Rejected uploads are preserved under rejected/ (not deleted) so the review
	// history keeps the submitted art; each file row is updated to the new key.
	for _, f := range files {
		newKey, err := preserveRejectedFile(ctx, s.r2, f.ObjectKey)
		if err != nil {
			return nil, fmt.Errorf("preserve rejected file %s: %w", f.ObjectKey, err)
		}
		if err := s.repo.UpdateMetadataSubmissionFileObjectKey(f.ID, newKey); err != nil {
			return nil, err
		}
	}
	return s.repo.SetMetadataStatusForAdmin(id, models.MetadataRejected, adminID, comment)
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
