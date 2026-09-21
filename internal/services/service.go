package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"neoassets/internal/models"
	"neoassets/internal/repository"
	"neoassets/internal/systems"
	"neoassets/pkg/auth"
	"neoassets/pkg/r2"
	"neoassets/pkg/video"
)

// packIDRegex validates that a pack id is a safe path segment.
var packIDRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// systemIDRegex validates system ids used for background filenames.
var systemIDRegex = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// Service contains the business logic for system art pack submissions.
type Service struct {
	repo       *repository.Repository
	r2         r2.Client
	publicBase string
	uploadTTL  time.Duration
	imageExts  map[string]bool
	jwtSecret  string
	tokenTTL   time.Duration
	systems    *systems.Catalog
	translator *Translator

	storageMu      sync.RWMutex
	storageUsage   *StorageUsage
	storageUpdated time.Time
}

// NewService creates a Service.
func NewService(repo *repository.Repository, r2Client r2.Client, publicBase, jwtSecret string, systems *systems.Catalog) *Service {
	return &Service{
		repo:       repo,
		r2:         r2Client,
		publicBase: strings.TrimRight(publicBase, "/"),
		uploadTTL:  15 * time.Minute,
		// Images (backgrounds, previews, logos) may be uploaded in any common
		// raster format: the backend normalizes them to WebP on approval. GIFs
		// are kept as-is to preserve animation. Themes are JSON files and are
		// validated separately.
		imageExts: map[string]bool{
			".webp": true,
			".gif":  true,
			".png":  true,
			".jpg":  true,
			".jpeg": true,
		},
		jwtSecret: jwtSecret,
		tokenTTL:  12 * time.Hour,
		systems:   systems,
		translator: NewTranslator(
			os.Getenv("TRANSLATE_WORKER_URL"),
			os.Getenv("TRANSLATE_WORKER_TOKEN"),
		),
	}
}

// ListSystems returns the official system catalog for the web frontend.
func (s *Service) ListSystems() []systems.System {
	if s.systems == nil {
		return nil
	}
	return s.systems.List()
}

// Login validates admin credentials and returns a signed JWT.
func (s *Service) Login(email, password string) (*models.LoginResult, error) {
	admin, err := s.repo.GetAdminByEmail(email)
	if err != nil {
		log.Warn().Str("email", email).Msg("failed admin login: unknown account")
		return nil, fmt.Errorf("invalid credentials")
	}
	if !auth.CheckPassword(admin.PasswordHash, password) {
		log.Warn().Str("email", email).Msg("failed admin login: wrong password")
		return nil, fmt.Errorf("invalid credentials")
	}

	expiresAt := time.Now().Add(s.tokenTTL)
	token, err := auth.GenerateToken(s.jwtSecret, admin.ID, admin.Email, s.tokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	log.Info().Str("email", admin.Email).Msg("admin login")
	return &models.LoginResult{
		Token:     token,
		Email:     admin.Email,
		ExpiresAt: expiresAt,
	}, nil
}

// ListSubmissions returns submissions filtered by status.
func (s *Service) ListSubmissions(status string) ([]models.Submission, error) {
	if status != "" {
		switch status {
		case models.StatusCreated, models.StatusPending, models.StatusApproved, models.StatusRejected, models.StatusTrashed:
		default:
			return nil, fmt.Errorf("invalid status filter")
		}
	}
	return s.repo.ListSubmissions(status)
}

// GetSubmissionDetail returns a submission with its files and logs.
func (s *Service) GetSubmissionDetail(ctx context.Context, id uuid.UUID) (*models.SubmissionDetail, error) {
	sub, err := s.repo.GetSubmission(id)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}

	files, err := s.repo.ListFiles(id)
	if err != nil {
		return nil, err
	}
	logs, err := s.repo.ListLogs(id)
	if err != nil {
		return nil, err
	}

	detail := &models.SubmissionDetail{
		Submission: *sub,
		Files:      files,
		Logs:       logs,
	}
	if sub.UserID != nil || sub.ReviewedBy != nil {
		var ids []uuid.UUID
		if sub.UserID != nil {
			ids = append(ids, *sub.UserID)
		}
		if sub.ReviewedBy != nil {
			ids = append(ids, *sub.ReviewedBy)
		}
		if names, err := s.repo.UserNamesByID(ids); err == nil {
			if sub.UserID != nil {
				detail.SubmittedBy = names[*sub.UserID]
			}
			if sub.ReviewedBy != nil {
				detail.ReviewedByName = names[*sub.ReviewedBy]
			}
		}
	}
	if err := s.decorateReplaces([]models.SubmissionDetail{*detail}); err != nil {
		return nil, err
	}
	if err := s.decorateLogUsers(ctx, []models.SubmissionDetail{*detail}); err != nil {
		return nil, err
	}
	return detail, nil
}

// ValidateCreate checks the create-submission payload.
func (s *Service) ValidateCreate(req models.CreateSubmissionRequest) error {
	if strings.TrimSpace(req.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(req.Author) == "" {
		return fmt.Errorf("author is required")
	}
	if len(req.Name) > 255 {
		return fmt.Errorf("name too long")
	}
	if len(req.Author) > 255 {
		return fmt.Errorf("author too long")
	}
	if len(req.DonationURL) > 512 {
		return fmt.Errorf("donation_url too long")
	}
	return nil
}

// PackIDFromName generates a URL-safe pack id from a display name.
func (s *Service) PackIDFromName(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug = re.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) == 0 {
		slug = "theme"
	}
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return slug
}

// CreateSubmission registers a new submission for the logged-in user. When the
// request is a contribution, it targets an existing approved pack and this
// submission only carries images/description changes for it.
func (s *Service) CreateSubmission(ctx context.Context, req models.CreateSubmissionRequest, userID uuid.UUID) (*models.Submission, error) {
	if err := s.ValidateCreate(req); err != nil {
		return nil, err
	}

	packID := s.PackIDFromName(req.Name)
	if req.Contribution {
		if strings.TrimSpace(req.PackID) == "" {
			return nil, fmt.Errorf("pack_id is required for a contribution")
		}
		target, err := s.repo.GetApprovedPackMeta(strings.TrimSpace(req.PackID))
		if err != nil {
			return nil, fmt.Errorf("pack not found or not approved")
		}
		packID = target.PackID
		req.Name = target.Name
		req.Author = target.Author
		req.DonationURL = target.DonationURL
		req.AI = target.AI
		if strings.TrimSpace(req.Description) == "" {
			req.Description = target.Description
		}
	}
	if !packIDRegex.MatchString(packID) {
		packID = fmt.Sprintf("theme-%s", uuid.NewString()[:8])
	}

	anonID := userID.String()
	id, err := s.repo.CreateSubmission(
		packID, strings.TrimSpace(req.Name), strings.TrimSpace(req.Author),
		strings.TrimSpace(req.Description), strings.TrimSpace(req.DonationURL),
		anonID, req.AI, &userID, req.Contribution,
	)
	if err != nil {
		return nil, err
	}

	for _, f := range req.Files {
		if err := s.validateSubmissionFile(f); err != nil {
			return nil, err
		}
		if err := s.validatePackObjectKey(packID, f); err != nil {
			return nil, err
		}
		if err := s.ensureUploaded(ctx, f.ObjectKey, f.FileName); err != nil {
			return nil, err
		}
		mime := f.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		if err := s.repo.AddFileReason(id, f.ObjectKey, f.FileName, f.SystemID, f.Kind, mime, f.Size, f.Reason); err != nil {
			return nil, err
		}
	}

	if err := s.repo.AddLog(id, "submitted", anonID, "", "Submission submitted for review"); err != nil {
		return nil, err
	}

	return s.repo.GetSubmission(id)
}

// validateSubmissionFile checks a file already uploaded against the allowed
// kinds/extensions/system ids.
func (s *Service) validateSubmissionFile(f models.SubmissionFileInput) error {
	if f.ObjectKey == "" {
		return fmt.Errorf("object_key required")
	}
	switch f.Kind {
	case models.KindBackground, models.KindPreview, models.KindTheme, models.KindLogo:
	default:
		return fmt.Errorf("invalid kind %q", f.Kind)
	}
	ext := strings.ToLower(getExt(f.FileName))
	if f.Kind == models.KindTheme {
		if ext != ".json" {
			return fmt.Errorf("theme must be a .json file")
		}
	} else if !s.imageExts[ext] {
		return fmt.Errorf("unsupported image extension %q", ext)
	}
	if (f.Kind == models.KindBackground || f.Kind == models.KindLogo) && f.SystemID != "" && s.systems != nil && !s.systems.Has(f.SystemID) {
		return fmt.Errorf("unknown system id %q", f.SystemID)
	}
	return nil
}

// validatePackObjectKey ensures a client-supplied object key belongs to this
// pack's own staging area or to its exact canonical location, so a submission
// can never reference (and later delete on approval) objects owned by another
// pack or by the media catalog.
func (s *Service) validatePackObjectKey(packID string, f models.SubmissionFileInput) error {
	if !strings.HasPrefix(f.ObjectKey, "packs/"+packID+"/") {
		return fmt.Errorf("object_key does not belong to this submission")
	}
	if !isReviewObjectKey(f.ObjectKey) {
		// A canonical key is only acceptable when it is exactly the one the
		// service would generate for this (kind, file name).
		if canonical := s.ObjectKey(packID, f.Kind, f.FileName); f.ObjectKey != canonical {
			return fmt.Errorf("object_key does not belong to this submission")
		}
	}
	return nil
}

// SubmissionUploadURLPre presigns an upload to a temp key under the pack.
// The temp key keeps the currently-published image at the canonical key intact
// until the submission is approved, so the reviewer can compare old vs new.
func (s *Service) SubmissionUploadURLPre(ctx context.Context, req models.SubmissionUploadURLRequest) (*models.UploadResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("pack name is required")
	}
	if err := s.ValidateUploadRequest(models.UploadRequest{Kind: req.Kind, FileName: req.FileName, SystemID: req.SystemID, MimeType: req.MimeType, Size: req.Size}); err != nil {
		return nil, err
	}
	packID := s.PackIDFromName(req.Name)
	if !packIDRegex.MatchString(packID) {
		packID = fmt.Sprintf("theme-%s", uuid.NewString()[:8])
	}
	objectKey := reviewObjectKey(packID, req.FileName)
	mime := req.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, objectKey, mime, s.uploadTTL)
	if err != nil {
		return nil, err
	}
	return &models.UploadResponse{UploadURL: uploadURL, ObjectKey: objectKey, ExpiresAt: time.Now().Add(s.uploadTTL)}, nil
}

// maxGIFSize caps GIF uploads (avatars and SAP art) at 5 MiB, since animated
// GIFs are stored as-is (not re-encoded) and can be large.
const maxGIFSize = 5 << 20

// maxImageSize caps a non-GIF image upload. The backend re-encodes it to WebP
// on approval, so the raw upload only needs to be reasonable in size.
const maxImageSize = 25 << 20

// ValidateUploadRequest checks a single file upload request against the
// allowed kinds, extensions, and (for backgrounds) system ids.
func (s *Service) ValidateUploadRequest(req models.UploadRequest) error {
	switch req.Kind {
	case models.KindBackground, models.KindPreview, models.KindTheme, models.KindLogo:
	default:
		return fmt.Errorf("invalid kind %q", req.Kind)
	}

	ext := strings.ToLower(getExt(req.FileName))

	// Images (background, preview, logo) must be WebP/GIF; themes are JSON.
	if req.Kind == models.KindTheme {
		if ext != ".json" {
			return fmt.Errorf("theme must be a .json file")
		}
	} else if !s.imageExts[ext] {
		return fmt.Errorf("unsupported image extension %q", ext)
	}

	// GIFs are stored as-is (animation preserved), so cap their size. Other
	// images are re-encoded on approval, so they only need a sane size cap.
	if ext == ".gif" {
		if req.Size > maxGIFSize {
			return fmt.Errorf("GIF must be at most %d MB", maxGIFSize>>20)
		}
	} else if req.Size > maxImageSize {
		return fmt.Errorf("image must be at most %d MB", maxImageSize>>20)
	}

	if req.Kind == models.KindBackground || req.Kind == models.KindLogo {
		systemID := strings.TrimSuffix(req.FileName, ext)
		if !systemIDRegex.MatchString(systemID) {
			return fmt.Errorf("invalid system id %q", systemID)
		}
		if s.systems != nil && !s.systems.Has(systemID) {
			return fmt.Errorf("unknown system id %q", systemID)
		}
	}
	return nil
}

// isReviewObjectKey reports whether an object key is a staged submission upload
// (review/uploads folder) rather than a canonical published file (backgrounds/,
// preview.webp, theme.json, logos/). Review uploads are deleted on reject and
// promoted to canonical on approve; canonical files are the live pack.
func isReviewObjectKey(objectKey string) bool {
	return strings.Contains(objectKey, "/review/") || strings.Contains(objectKey, "/uploads/")
}

// rejectedObjectKey is where a rejected submission's file is preserved, so the
// rejected upload stays available for the review history instead of being
// deleted.
func rejectedObjectKey(objectKey string) string {
	return "rejected/" + objectKey
}

// preserveRejectedFile moves an object to its rejected location (copy + delete)
// and returns the new key. The caller stores the new key so the file row keeps
// pointing at the preserved object.
func preserveRejectedFile(ctx context.Context, r2c r2.Client, objectKey string) (string, error) {
	dst := rejectedObjectKey(objectKey)
	if err := r2c.CopyObject(ctx, objectKey, dst); err != nil {
		return "", err
	}
	if err := r2c.DeleteObject(ctx, objectKey); err != nil {
		return "", err
	}
	return dst, nil
}

// reviewObjectKey builds the staged review object key for a submission upload,
// kept separate from the canonical published keys so the live pack is never
// touched until an admin approves the submission.
func reviewObjectKey(packID, fileName string) string {
	return fmt.Sprintf("packs/%s/review/%s/%s", packID, uuid.NewString()[:8], fileName)
}

// ObjectKey builds the R2 object key for a file in a submission.
func (s *Service) ObjectKey(packID, kind, fileName string) string {
	switch kind {
	case models.KindPreview:
		return fmt.Sprintf("packs/%s/preview.webp", packID)
	case models.KindTheme:
		return fmt.Sprintf("packs/%s/theme.json", packID)
	case models.KindBackground, models.KindLogo:
		// Images are normalized to WebP on approval; only animated GIFs keep
		// their original extension.
		rawExt := getExt(fileName)
		ext := strings.ToLower(rawExt)
		if ext != ".gif" {
			ext = ".webp"
		}
		systemID := strings.TrimSuffix(fileName, rawExt)
		dir := "backgrounds"
		if kind == models.KindLogo {
			dir = "logos"
		}
		return fmt.Sprintf("packs/%s/%s/%s%s", packID, dir, systemID, ext)
	default:
		return fmt.Sprintf("packs/%s/%s", packID, fileName)
	}
}

// PublicURL returns the public URL for an object key served via the R2
// custom domain.
func (s *Service) PublicURL(objectKey string) string {
	return fmt.Sprintf("%s/%s", s.publicBase, objectKey)
}

// GetUploadURL returns a presigned URL for uploading a single file to R2,
// registering the file metadata once the URL is generated.
func (s *Service) GetUploadURL(ctx context.Context, submissionID uuid.UUID, userID uuid.UUID, req models.UploadRequest) (*models.UploadResponse, error) {
	sub, err := s.repo.GetSubmissionByIDForUser(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.StatusPending && sub.Status != models.StatusCreated {
		return nil, fmt.Errorf("submission is not editable")
	}
	if err := s.ValidateUploadRequest(req); err != nil {
		return nil, err
	}

	objectKey := reviewObjectKey(sub.PackID, req.FileName)

	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, objectKey, mimeType, s.uploadTTL)
	if err != nil {
		return nil, err
	}

	systemID := ""
	if req.Kind == models.KindBackground || req.Kind == models.KindLogo {
		systemID = strings.TrimSuffix(req.FileName, getExt(req.FileName))
	}

	if err := s.repo.AddFile(submissionID, objectKey, req.FileName, systemID, req.Kind, mimeType, req.Size); err != nil {
		return nil, err
	}

	if err := s.repo.AddLog(submissionID, "upload_initiated", sub.AnonUserID, "", objectKey); err != nil {
		return nil, err
	}

	return &models.UploadResponse{
		UploadURL: uploadURL,
		ObjectKey: objectKey,
		ExpiresAt: time.Now().Add(s.uploadTTL),
	}, nil
}

// UpdateSubmission edits the metadata of a draft owned by the user. Drafts are
// stored while the submission stays in the 'created' state.
func (s *Service) UpdateSubmission(ctx context.Context, submissionID uuid.UUID, userID uuid.UUID, req models.CreateSubmissionRequest) (*models.Submission, error) {
	if err := s.ValidateCreate(req); err != nil {
		return nil, err
	}

	sub, err := s.repo.UpdateSubmission(
		submissionID, userID,
		strings.TrimSpace(req.Name), strings.TrimSpace(req.Author),
		strings.TrimSpace(req.Description), strings.TrimSpace(req.DonationURL), req.AI,
	)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}

	if err := s.repo.AddLog(submissionID, "updated", sub.AnonUserID, "", "Submission saved"); err != nil {
		return nil, err
	}

	return sub, nil
}

// AddSubmissionFiles registers newly-uploaded files on an existing draft or
// rejected submission (used when a submitter iterates after a rejection).
// Approved submissions are read-only: changes must be a new contribution.
func (s *Service) AddSubmissionFiles(ctx context.Context, submissionID uuid.UUID, userID uuid.UUID, files []models.SubmissionFileInput) (*models.Submission, error) {
	sub, err := s.repo.GetSubmissionByIDForUser(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.StatusCreated && sub.Status != models.StatusRejected {
		return nil, fmt.Errorf("submission cannot be edited")
	}
	for _, f := range files {
		if err := s.validateSubmissionFile(f); err != nil {
			return nil, err
		}
		if err := s.validatePackObjectKey(sub.PackID, f); err != nil {
			return nil, err
		}
		if err := s.ensureUploaded(ctx, f.ObjectKey, f.FileName); err != nil {
			return nil, err
		}
		mime := f.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		if err := s.repo.AddFileReason(submissionID, f.ObjectKey, f.FileName, f.SystemID, f.Kind, mime, f.Size, f.Reason); err != nil {
			return nil, err
		}
	}
	return s.repo.GetSubmission(submissionID)
}

// ensureUploaded verifies a submitted object actually exists in R2 (and that a
// GIF does not exceed the size cap) so a failed or oversized browser upload is
// never registered.
func (s *Service) ensureUploaded(ctx context.Context, objectKey, fileName string) error {
	size, ok, err := s.r2.ObjectSize(ctx, objectKey)
	if err != nil {
		return fmt.Errorf("could not verify upload %q: %w", fileName, err)
	}
	if !ok {
		return fmt.Errorf("upload for %q did not complete — try uploading again", fileName)
	}
	ext := strings.ToLower(getExt(fileName))
	switch {
	case ext == ".gif":
		if size > maxGIFSize {
			return fmt.Errorf("GIF %q is too large (max %d MB)", fileName, maxGIFSize>>20)
		}
	case s.imageExts[ext]:
		if size > maxImageSize {
			return fmt.Errorf("image %q is too large (max %d MB)", fileName, maxImageSize>>20)
		}
	}
	return nil
}

// avatarKey builds a fresh, immutable avatar object key for a user, so a new
// upload never serves a stale cached image.
func avatarKey(userID uuid.UUID, ext string) string {
	return fmt.Sprintf("profile/%s/%s%s", userID, uuid.NewString(), ext)
}

// AvatarUploadURL presigns an upload for a new avatar (WebP or GIF; GIFs are
// capped at maxGIFSize and stored as-is to preserve animation).
func (s *Service) AvatarUploadURL(ctx context.Context, userID uuid.UUID, mimeType string, size int64) (*models.UploadResponse, error) {
	ext := ".webp"
	switch {
	case strings.EqualFold(mimeType, "image/gif"):
		ext = ".gif"
	case strings.EqualFold(mimeType, "image/webp"):
		ext = ".webp"
	default:
		return nil, fmt.Errorf("avatar must be a WebP or GIF image")
	}
	if ext == ".gif" && size > maxGIFSize {
		return nil, fmt.Errorf("GIF must be at most %d MB", maxGIFSize>>20)
	}
	key := avatarKey(userID, ext)
	uploadURL, err := s.r2.GenerateSignedUploadURL(ctx, key, mimeType, s.uploadTTL)
	if err != nil {
		return nil, err
	}
	return &models.UploadResponse{UploadURL: uploadURL, ObjectKey: key, ExpiresAt: time.Now().Add(s.uploadTTL)}, nil
}

// SetAvatar validates and stores a user's uploaded avatar, deleting the
// previous object so only one avatar exists per user.
func (s *Service) SetAvatar(ctx context.Context, userID uuid.UUID, objectKey string) (*models.User, error) {
	if !strings.HasPrefix(objectKey, fmt.Sprintf("profile/%s/", userID)) {
		return nil, fmt.Errorf("object_key does not belong to this user")
	}
	if err := s.ensureUploaded(ctx, objectKey, objectKey); err != nil {
		return nil, err
	}
	prev, err := s.repo.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	updated, err := s.repo.SetUserAvatar(userID, objectKey)
	if err != nil {
		return nil, err
	}
	if prev.AvatarKey != "" && prev.AvatarKey != objectKey {
		if err := s.r2.DeleteObject(ctx, prev.AvatarKey); err != nil {
			log.Warn().Str("object", prev.AvatarKey).Err(err).Msg("failed to delete previous avatar")
		}
	}
	return updated, nil
}

// RemoveAvatar clears a user's avatar and deletes the object.
func (s *Service) RemoveAvatar(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	prev, err := s.repo.GetUserByID(userID)
	if err != nil {
		return nil, err
	}
	updated, err := s.repo.SetUserAvatar(userID, "")
	if err != nil {
		return nil, err
	}
	if prev.AvatarKey != "" {
		if err := s.r2.DeleteObject(ctx, prev.AvatarKey); err != nil {
			log.Warn().Str("object", prev.AvatarKey).Err(err).Msg("failed to delete avatar")
		}
	}
	return updated, nil
}

// SubmitForReview finalizes a draft owned by the user, moving it from the
// 'created' state to 'pending' so it shows up in the admin review queue.
func (s *Service) SubmitForReview(ctx context.Context, submissionID uuid.UUID, userID uuid.UUID) (*models.Submission, error) {
	sub, err := s.repo.SubmitSubmission(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}

	files, err := s.repo.ListFiles(submissionID)
	if err != nil {
		return nil, err
	}
	// A contribution may carry only a description change (no new images), so
	// require files only for brand-new packs.
	if len(files) == 0 && !sub.Contribution {
		return nil, fmt.Errorf("submission has no files")
	}

	if err := s.repo.AddLog(submissionID, "submitted", sub.AnonUserID, "", "Submitted for review"); err != nil {
		return nil, err
	}
	return sub, nil
}

// ListUserSubmissions returns the submissions of a user, each with its files
// and logs. Files are included so the frontend can render uploaded thumbnails.
// status/limit/offset page the result; total is the unpaged count and totalXP
// the lifetime XP earned from approved system art packs.
func (s *Service) ListUserSubmissions(ctx context.Context, userID uuid.UUID, status string, limit, offset int) ([]models.SubmissionDetail, int64, int, error) {
	list, total, err := s.repo.ListSubmissionsByUser(userID, status, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	details, err := s.loadDetails(ctx, list)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := s.decorateWithUsername(ctx, details); err != nil {
		return nil, 0, 0, err
	}
	if err := s.decorateLogUsers(ctx, details); err != nil {
		return nil, 0, 0, err
	}
	ids := make([]uuid.UUID, 0, len(details))
	for i := range details {
		ids = append(ids, details[i].ID)
	}
	if points, err := s.repo.SubmissionPointsByIDs(ids); err == nil {
		for i := range details {
			details[i].PointsEarned = points[details[i].ID]
			details[i].BasePointsEarned = len(details[i].Files) * PointsSAPImage
		}
	}
	_, totalXP, err := s.repo.UserAwardedXPBySource(userID)
	if err != nil {
		return nil, 0, 0, err
	}
	return details, total, totalXP, nil
}

// ListAdminSubmissions returns submissions (with files and logs) filtered by
// status, for the admin review grid. With no filter it returns the whole review
// history (pending, approved, rejected), never private drafts or trashed rows.
func (s *Service) ListAdminSubmissions(ctx context.Context, status string) ([]models.SubmissionDetail, error) {
	var list []models.Submission
	var err error
	if status == "" {
		list, err = s.repo.ListReviewSubmissions()
	} else {
		list, err = s.repo.ListSubmissions(status)
	}
	if err != nil {
		return nil, err
	}
	details, err := s.loadDetails(ctx, list)
	if err != nil {
		return nil, err
	}
	if err := s.decorateWithUsername(ctx, details); err != nil {
		return nil, err
	}
	if err := s.decorateReplaces(details); err != nil {
		return nil, err
	}
	if err := s.decorateLogUsers(ctx, details); err != nil {
		return nil, err
	}
	return details, nil
}

// loadDetails attaches every submission's files and logs in a single batched
// query each, avoiding the N+1 pattern of one query per submission.
func (s *Service) loadDetails(ctx context.Context, list []models.Submission) ([]models.SubmissionDetail, error) {
	ids := make([]uuid.UUID, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	filesByID, err := s.repo.ListFilesBySubmissionIDs(ids)
	if err != nil {
		return nil, err
	}
	logsByID, err := s.repo.ListLogsBySubmissionIDs(ids)
	if err != nil {
		return nil, err
	}
	details := make([]models.SubmissionDetail, 0, len(list))
	for i := range list {
		details = append(details, models.SubmissionDetail{
			Submission: list[i],
			Files:      filesByID[list[i].ID],
			Logs:       logsByID[list[i].ID],
		})
	}
	return details, nil
}

// decorateWithUsername sets SubmittedBy and ReviewedByName on each detail from
// the users table.
func (s *Service) decorateWithUsername(ctx context.Context, details []models.SubmissionDetail) error {
	if len(details) == 0 {
		return nil
	}
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for i := range details {
		if details[i].UserID != nil && !seen[*details[i].UserID] {
			seen[*details[i].UserID] = true
			ids = append(ids, *details[i].UserID)
		}
		if details[i].ReviewedBy != nil && !seen[*details[i].ReviewedBy] {
			seen[*details[i].ReviewedBy] = true
			ids = append(ids, *details[i].ReviewedBy)
		}
	}
	names, err := s.repo.UserNamesByID(ids)
	if err != nil {
		return err
	}
	for i := range details {
		if details[i].UserID != nil {
			details[i].SubmittedBy = names[*details[i].UserID]
		}
		if details[i].ReviewedBy != nil {
			details[i].ReviewedByName = names[*details[i].ReviewedBy]
		}
	}
	return nil
}

// decorateLogUsers resolves the usernames of the actors stored in each log's
// user_id, so the status history can label who did what.
func (s *Service) decorateLogUsers(ctx context.Context, details []models.SubmissionDetail) error {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for i := range details {
		for _, l := range details[i].Logs {
			if id, err := uuid.Parse(l.UserID); err == nil && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names, err := s.repo.UserNamesByID(ids)
	if err != nil {
		return err
	}
	for i := range details {
		for j := range details[i].Logs {
			if id, err := uuid.Parse(details[i].Logs[j].UserID); err == nil {
				details[i].Logs[j].UserName = names[id]
			}
		}
	}
	return nil
}

// decorateReplaces flags each submitted file as changed when it was uploaded
// after the submission's last review (i.e. this is what the submitter actually
// updated), while files carried over from a previous rejected revision are not
// marked. Changed files that replace an already-published image get their
// replaces_object_key resolved so the reviewer can compare old vs new.
func (s *Service) decorateReplaces(details []models.SubmissionDetail) error {
	packIDs := make([]string, 0, len(details))
	seen := map[string]bool{}
	for i := range details {
		if details[i].PackID == "" || seen[details[i].PackID] {
			continue
		}
		seen[details[i].PackID] = true
		packIDs = append(packIDs, details[i].PackID)
	}
	approvedKeys, err := s.repo.ListApprovedPackKeysByPacks(packIDs)
	if err != nil {
		return err
	}
	for i := range details {
		sub := &details[i]
		if sub.PackID == "" {
			continue
		}
		old := map[string]bool{}
		for _, k := range approvedKeys[sub.PackID] {
			old[k] = true
		}
		for j := range sub.Files {
			f := &sub.Files[j]
			changed := sub.ReviewedAt == nil || f.CreatedAt.After(*sub.ReviewedAt)
			if !changed {
				f.Changed = false
				continue
			}
			f.Changed = true
			canonical := s.ObjectKey(sub.PackID, f.Kind, f.FileName)
			if canonical != "" && canonical != f.ObjectKey && old[canonical] {
				f.ReplacesObjectKey = canonical
				continue
			}
			prevKey, ok, err := s.repo.PackPreviousObjectKey(sub.PackID, sub.ID, f.Kind, f.SystemID)
			if err != nil {
				return err
			}
			if ok && !old[prevKey] {
				f.ReplacesObjectKey = prevKey
			}
		}
	}
	return nil
}

// GetUserSubmissionDetail returns a single submission owned by the user,
// with its files and logs.
func (s *Service) GetUserSubmissionDetail(ctx context.Context, submissionID uuid.UUID, userID uuid.UUID) (*models.SubmissionDetail, error) {
	sub, err := s.repo.GetSubmissionByIDForUser(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}

	files, err := s.repo.ListFiles(submissionID)
	if err != nil {
		return nil, err
	}
	logs, err := s.repo.ListLogs(submissionID)
	if err != nil {
		return nil, err
	}

	detail := &models.SubmissionDetail{
		Submission: *sub,
		Files:      files,
		Logs:       logs,
	}
	if err := s.decorateLogUsers(ctx, []models.SubmissionDetail{*detail}); err != nil {
		return nil, err
	}
	return detail, nil
}

// Approve marks a submission as approved, auto-assigning its pack version
// (1.0 for the first approved revision, then 1.1, 1.2, ...).
func (s *Service) Approve(ctx context.Context, submissionID uuid.UUID, adminID uuid.UUID, _, comment string) (*models.Submission, error) {
	sub, err := s.repo.GetSubmission(submissionID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.StatusPending {
		return nil, fmt.Errorf("submission is not pending")
	}

	n, err := s.repo.CountApprovedByPack(sub.PackID, sub.ID)
	if err != nil {
		return nil, err
	}
	version := fmt.Sprintf("1.%d", n)

	if err := s.moveSubmissionFilesToCanonical(ctx, sub.PackID, submissionID); err != nil {
		return nil, err
	}

	updated, err := s.repo.SetSubmissionStatus(submissionID, models.StatusApproved, adminID, version)
	if err != nil {
		return nil, err
	}

	detail := "Approved by admin"
	if strings.TrimSpace(comment) != "" {
		detail = fmt.Sprintf("Approved by admin: %s", strings.TrimSpace(comment))
	}
	if err := s.repo.AddLog(submissionID, "approved", adminID.String(), "", detail); err != nil {
		return nil, err
	}

	if sub.UserID != nil {
		files, err := s.repo.ListFiles(submissionID)
		if err == nil && len(files) > 0 {
			if err := s.awardXP(*sub.UserID, len(files)*PointsSAPImage, "approved_sap_images", &submissionID); err != nil {
				return nil, err
			}
		}
	}

	return updated, nil
}

// moveSubmissionFilesToCanonical publishes an approved submission's files to
// their canonical pack keys. Theme JSON and animated GIFs are copied unchanged;
// every other image is normalized to WebP by the backend (crop, scale, quality)
// so the published art never depends on client-side conversion. The staging
// objects and the replaced canonical objects are deleted so R2 does not
// accumulate garbage.
func (s *Service) moveSubmissionFilesToCanonical(ctx context.Context, packID string, submissionID uuid.UUID) error {
	if packID == "" {
		return nil
	}
	files, err := s.repo.ListFiles(submissionID)
	if err != nil {
		return err
	}
	for i := range files {
		if err := s.publishSubmissionFile(ctx, packID, submissionID, &files[i]); err != nil {
			return err
		}
	}
	return nil
}

// publishSubmissionFile moves a single approved file to its canonical key. JSON
// themes and animated GIFs are copied as-is; all other images are re-encoded to
// WebP.
func (s *Service) publishSubmissionFile(ctx context.Context, packID string, submissionID uuid.UUID, f *models.SubmissionFile) error {
	canonical := s.ObjectKey(packID, f.Kind, f.FileName)
	if canonical == "" || canonical == f.ObjectKey {
		return nil
	}

	// JSON themes are not images and are copied verbatim.
	if f.Kind == models.KindTheme {
		return s.copySubmissionFileToCanonical(ctx, packID, submissionID, f, canonical)
	}

	data, err := s.r2.DownloadObject(ctx, f.ObjectKey)
	if err != nil {
		// A missing source upload (404 NoSuchKey) means the object never made it
		// to R2; drop that stale file row and keep going rather than failing the
		// whole approval.
		if isMissingSource(err) {
			log.Warn().Str("object", f.ObjectKey).Msg("skipping missing submission object on approve")
			return s.repo.DeleteSubmissionFile(f.ID)
		}
		return fmt.Errorf("download %s: %w", f.ObjectKey, err)
	}

	// Animated GIFs keep their animation and are published unchanged.
	if info, perr := video.ProbeImage(ctx, data); perr == nil && info.Codec == "gif" {
		return s.copySubmissionFileToCanonical(ctx, packID, submissionID, f, canonical)
	}

	normalized, err := video.NormalizeImage(ctx, data, sapImageSpec(f.Kind))
	if err != nil {
		return fmt.Errorf("normalize %s: %w", f.FileName, err)
	}
	if err := s.r2.UploadFile(ctx, canonical, "image/webp", bytes.NewReader(normalized)); err != nil {
		return fmt.Errorf("upload normalized %s: %w", canonical, err)
	}
	if err := s.r2.DeleteObject(ctx, f.ObjectKey); err != nil {
		return fmt.Errorf("delete temp %s: %w", f.ObjectKey, err)
	}
	// The previous canonical may have used a different extension (a GIF replaced
	// by a WebP); remove it so it does not linger in R2.
	s.deleteSiblingCanonical(ctx, packID, f.Kind, f.FileName, canonical)
	// A submission may carry a duplicate row for the same system (one already
	// canonical, one left in review); drop the other row so the unique
	// (submission_id, object_key) constraint does not block the update.
	if err := s.repo.DeleteSubmissionFileByObjectKey(submissionID, canonical, f.ID); err != nil {
		return err
	}
	if err := s.repo.UpdateSubmissionFileObjectKey(f.ID, canonical); err != nil {
		return err
	}
	return s.repo.UpdateSubmissionFileMime(f.ID, "image/webp")
}

// copySubmissionFileToCanonical copies a staged file to its canonical key
// without re-encoding (themes and animated GIFs), deletes the staging object and
// updates the file row.
func (s *Service) copySubmissionFileToCanonical(ctx context.Context, packID string, submissionID uuid.UUID, f *models.SubmissionFile, canonical string) error {
	if err := s.r2.CopyObject(ctx, f.ObjectKey, canonical); err != nil {
		if isMissingSource(err) {
			log.Warn().Str("object", f.ObjectKey).Msg("skipping missing submission object on approve")
			return s.repo.DeleteSubmissionFile(f.ID)
		}
		return fmt.Errorf("copy %s -> %s: %w", f.ObjectKey, canonical, err)
	}
	if err := s.r2.DeleteObject(ctx, f.ObjectKey); err != nil {
		return fmt.Errorf("delete temp %s: %w", f.ObjectKey, err)
	}
	s.deleteSiblingCanonical(ctx, packID, f.Kind, f.FileName, canonical)
	if err := s.repo.DeleteSubmissionFileByObjectKey(submissionID, canonical, f.ID); err != nil {
		return err
	}
	return s.repo.UpdateSubmissionFileObjectKey(f.ID, canonical)
}

// deleteSiblingCanonical removes the alternate-extension canonical object for a
// background/logo so replacing a GIF with a WebP (or vice versa) does not leave
// an orphan in R2. It is a no-op for other kinds.
func (s *Service) deleteSiblingCanonical(ctx context.Context, packID, kind, fileName, canonical string) {
	if packID == "" || (kind != models.KindBackground && kind != models.KindLogo) {
		return
	}
	rawExt := getExt(fileName)
	base := strings.TrimSuffix(fileName, rawExt)
	dir := "backgrounds"
	if kind == models.KindLogo {
		dir = "logos"
	}
	for _, ext := range []string{".webp", ".gif"} {
		key := fmt.Sprintf("packs/%s/%s/%s%s", packID, dir, base, ext)
		if key == canonical {
			continue
		}
		if err := s.r2.DeleteObject(ctx, key); err != nil {
			log.Warn().Err(err).Str("object", key).Msg("failed to delete replaced canonical object")
		}
	}
}

// sapImageSpec returns the normalization applied to an approved SAP image:
// backgrounds are center-cropped to a 1024x1024 square and previews/logos are
// capped at 1024px. GIFs never reach this path (they are copied unchanged).
func sapImageSpec(kind string) video.ImageSpec {
	if kind == models.KindBackground {
		return video.ImageSpec{TargetW: 1024, TargetH: 1024, Quality: 90}
	}
	return video.ImageSpec{MaxSize: 1024, Quality: 90}
}

// isMissingSource reports whether an S3 error means the source key does not
// exist (so the object was never successfully uploaded).
func isMissingSource(err error) bool {
	var smithyErr interface{ ErrorCode() string }
	if errors.As(err, &smithyErr) {
		switch smithyErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "404")
}

// Reject marks a submission as rejected and deletes its staged (review) files
// from R2. The pack's canonical published files are untouched, so rejecting an
// update simply drops the changes and keeps whatever is currently live.
func (s *Service) Reject(ctx context.Context, submissionID uuid.UUID, adminID uuid.UUID, comment string) (*models.Submission, error) {
	sub, err := s.repo.GetSubmission(submissionID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.StatusPending {
		return nil, fmt.Errorf("submission is not pending")
	}

	files, err := s.repo.ListFiles(submissionID)
	if err != nil {
		return nil, err
	}
	// Rejected uploads are preserved under rejected/ (not deleted) so the review
	// history keeps the submitted art; each file row is updated to the new key.
	for _, f := range files {
		if !isReviewObjectKey(f.ObjectKey) {
			continue
		}
		newKey, err := preserveRejectedFile(ctx, s.r2, f.ObjectKey)
		if err != nil {
			return nil, fmt.Errorf("preserve rejected file: %w", err)
		}
		if err := s.repo.UpdateSubmissionFileObjectKey(f.ID, newKey); err != nil {
			return nil, err
		}
	}

	updated, err := s.repo.SetSubmissionStatus(submissionID, models.StatusRejected, adminID, "")
	if err != nil {
		return nil, err
	}

	detail := "Rejected by admin"
	if strings.TrimSpace(comment) != "" {
		detail = fmt.Sprintf("Rejected by admin: %s", strings.TrimSpace(comment))
	}
	if err := s.repo.AddLog(submissionID, "rejected", adminID.String(), "", detail); err != nil {
		return nil, err
	}

	return updated, nil
}

// DeleteSubmission removes a system art pack submission completely: it deletes
// every R2 object referenced by the submission (both staged review files and any
// already-promoted canonical files) and then removes the submission row (cascade
// to its file rows and logs). Admin only.
func (s *Service) DeleteSubmission(ctx context.Context, submissionID uuid.UUID) error {
	if _, err := s.repo.GetSubmission(submissionID); err != nil {
		return fmt.Errorf("submission not found")
	}
	files, err := s.repo.ListFiles(submissionID)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		keys := make([]string, 0, len(files))
		for _, f := range files {
			keys = append(keys, f.ObjectKey)
		}
		if err := s.r2.DeleteObjects(ctx, keys); err != nil {
			return fmt.Errorf("delete submission media: %w", err)
		}
	}
	if err := s.repo.DeleteSubmission(submissionID); err != nil {
		return err
	}
	return nil
}

// StorageUsage is the storage usage breakdown between object storage and the
// metadata database.
type StorageUsage struct {
	TotalBytes     int64 `json:"total_bytes"`
	StorageBytes   int64 `json:"storage_bytes"`
	StorageObjects int   `json:"storage_objects"`
	DBBytes        int64 `json:"db_bytes"`
}

// GetStorageUsage computes the current storage usage.
func (s *Service) GetStorageUsage(ctx context.Context) (*StorageUsage, error) {
	usage := &StorageUsage{}

	// Try Cloudflare GraphQL API first (single HTTP request, instant)
	accountID := os.Getenv("R2_ACCOUNT_ID")
	cfToken := os.Getenv("CF_R2_API_TOKEN")
	if cfToken == "" {
		cfToken = os.Getenv("CF_API_TOKEN")
	}
	bucketName := os.Getenv("R2_BUCKET_NAME")

	if accountID != "" && cfToken != "" && bucketName != "" {
		bu, err := r2.GetBucketUsage(ctx, accountID, cfToken, bucketName)
		if err == nil {
			usage.StorageBytes = bu.PayloadSize + bu.MetadataSize
			usage.StorageObjects = int(bu.ObjectCount)
		}
	}

	// Fallback: sum from S3 listing if API failed
	if usage.StorageBytes == 0 {
		packs, err := s.r2.ListKeysWithSize(ctx, "packs/")
		if err != nil {
			return nil, fmt.Errorf("list packs: %w", err)
		}
		media, err := s.r2.ListKeysWithSize(ctx, "media/")
		if err != nil {
			return nil, fmt.Errorf("list media: %w", err)
		}
		for _, o := range packs {
			usage.StorageBytes += o.Size
		}
		for _, o := range media {
			usage.StorageBytes += o.Size
		}
		usage.StorageObjects = len(packs) + len(media)
	}

	dbSize, err := s.repo.GetDatabaseSize()
	if err != nil {
		return nil, fmt.Errorf("get database size: %w", err)
	}
	usage.DBBytes = dbSize

	usage.TotalBytes = usage.StorageBytes + usage.DBBytes
	return usage, nil
}

// GetCachedStorageUsage returns cached storage usage if fresh (< 1 hour old).
func (s *Service) GetCachedStorageUsage() *StorageUsage {
	s.storageMu.RLock()
	defer s.storageMu.RUnlock()
	if s.storageUsage == nil || time.Since(s.storageUpdated) > 1*time.Hour {
		return nil
	}
	return s.storageUsage
}

// RefreshStorageUsage recomputes storage usage and stores it in the cache.
func (s *Service) RefreshStorageUsage(ctx context.Context) error {
	usage, err := s.GetStorageUsage(ctx)
	if err != nil {
		return err
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	s.storageUsage = usage
	s.storageUpdated = time.Now()
	return nil
}

// StartStorageRefresh launches a background goroutine that refreshes the
// cached storage usage every hour.
func (s *Service) StartStorageRefresh(ctx context.Context) {
	go func() {
		_ = s.RefreshStorageUsage(context.Background())
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.RefreshStorageUsage(context.Background())
			}
		}
	}()
}

// deletePackOrphans removes every R2 object under packs/{packID}/ that is not
// referenced by a live (created/pending/approved) submission of that pack, so a
// trashed or rejected pack leaves no images behind and a recreated pack with the
// same name never picks up stale objects.
func (s *Service) deletePackOrphans(ctx context.Context, packID string, excludeID uuid.UUID) error {
	keep := map[string]bool{}
	rows, err := s.repo.ListLivePackObjectKeys(packID, excludeID)
	if err != nil {
		return err
	}
	for _, k := range rows {
		keep[k] = true
	}
	keys, err := s.r2.ListKeys(ctx, "packs/"+packID+"/")
	if err != nil {
		return fmt.Errorf("list pack objects %s: %w", packID, err)
	}
	var orphans []string
	for _, k := range keys {
		if !keep[k] {
			orphans = append(orphans, k)
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	if err := s.r2.DeleteObjects(ctx, orphans); err != nil {
		return fmt.Errorf("delete pack orphans %s: %w", packID, err)
	}
	return nil
}

// Trash moves a submission owned by the user into the 'trashed' state and
// deletes its files from R2. It only works while the pack is a draft or has
// been rejected; it cannot be trashed while pending review or once approved.
func (s *Service) Trash(ctx context.Context, submissionID uuid.UUID, userID uuid.UUID) (*models.Submission, error) {
	sub, err := s.repo.GetSubmissionByIDForUser(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}
	if sub.Status != models.StatusCreated && sub.Status != models.StatusRejected {
		return nil, fmt.Errorf("submission cannot be trashed")
	}

	if err := s.deletePackOrphans(ctx, sub.PackID, submissionID); err != nil {
		return nil, err
	}
	if err := s.repo.DeleteSubmissionFiles(submissionID); err != nil {
		return nil, err
	}

	updated, err := s.repo.TrashSubmission(submissionID, userID)
	if err != nil {
		return nil, fmt.Errorf("submission not found")
	}

	if err := s.repo.AddLog(submissionID, "trashed", sub.AnonUserID, "", "Moved to trash"); err != nil {
		return nil, err
	}

	return updated, nil
}

// ListedApprovedPacks returns the approved packs in their public form, with the
// preview object key resolved to its canonical location, sorted and paginated.
// The database is the single source of truth; there is no static manifest
// document. Packs without a registered preview fall back to a real published
// background so the thumbnail is never a broken URL. It returns the packs and
// the total number of approved packs (for pagination).
func (s *Service) ListedApprovedPacks(sort string, limit, offset int) ([]models.Pack, int64, error) {
	packs, total, err := s.repo.ListApprovedPacks(sort, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if len(packs) == 0 {
		return packs, total, nil
	}
	packIDs := make([]string, len(packs))
	for i, p := range packs {
		packIDs[i] = p.PackID
	}
	art, err := s.repo.ListApprovedPackArt(packIDs)
	if err != nil {
		return nil, 0, err
	}
	backgrounds, err := s.repo.ListApprovedPackBackgrounds(packIDs)
	if err != nil {
		return nil, 0, err
	}
	contribCounts, err := s.repo.ListPendingPackContributionCounts(packIDs)
	if err != nil {
		return nil, 0, err
	}
	contributors, err := s.repo.ListPackContributorNames(packIDs)
	if err != nil {
		return nil, 0, err
	}
	for i := range packs {
		if key := art[packs[i].PackID]; key != "" {
			packs[i].Preview = key
		} else {
			packs[i].Preview = fmt.Sprintf("packs/%s/preview.webp", packs[i].PackID)
		}
		packs[i].Backgrounds = backgrounds[packs[i].PackID]
		packs[i].Contributions = contribCounts[packs[i].PackID]
		packs[i].Contributors = contributors[packs[i].PackID]
	}
	return packs, total, nil
}

// GetPackDetail returns the full public payload of an approved pack (metadata +
// every published file with public URLs, pending contributions and contributor
// names). It is a pure read: it does NOT bump the download counter, so casual
// web browsing never inflates the count.
func (s *Service) GetPackDetail(packID string) (*models.PackDetail, error) {
	detail, err := s.repo.GetPackDetail(packID)
	if err != nil {
		return nil, err
	}
	for i := range detail.Files {
		detail.Files[i].URL = s.PublicURL(detail.Files[i].ObjectKey)
	}
	contribs, err := s.repo.ListPackContributions(packID)
	if err != nil {
		return nil, err
	}
	detail.Contributions = contribs
	contributors, err := s.repo.ListPackContributorNames([]string{packID})
	if err != nil {
		return nil, err
	}
	detail.Contributors = contributors[packID]
	return detail, nil
}

// DownloadPack returns the pack detail like GetPackDetail and bumps its
// download counter. It is the "install/scrape" action: only front-ends that
// actually install a pack should call it.
func (s *Service) DownloadPack(packID string) (*models.PackDetail, error) {
	detail, err := s.GetPackDetail(packID)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.IncrementPackScrape(packID); err != nil {
		return nil, err
	}
	return detail, nil
}

func getExt(name string) string {
	idx := strings.LastIndex(name, ".")
	if idx < 0 {
		return ""
	}
	return name[idx:]
}
