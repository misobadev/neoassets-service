package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"neoassets/internal/email"
	"neoassets/internal/handlers"
	"neoassets/internal/repository"
	"neoassets/internal/services"
	"neoassets/internal/systems"
	"neoassets/pkg/auth"
	"neoassets/pkg/r2"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Config struct {
	Port                               string
	DatabaseURL                        string
	JWTSecret                          string
	R2AccountID                        string
	R2AccessKey                        string
	R2SecretKey                        string
	R2BucketName                       string
	R2PublicBase                       string
	AdminEmail                         string
	AdminPassword                      string
	ProtectedAdminEmail                string
	SkipEmailVerification              bool
	SMTPHost                           string
	SMTPPort                           int
	SMTPUsername                       string
	SMTPPassword                       string
	SMTPFromEmail                      string
	SMTPFromName                       string
	FrontendURL                        string
	EmailLogoURL                       string
	TranslateWorkerURL                 string
	TranslateWorkerToken               string
	ScrapeGuestThreads                 int
	ScrapeAdminThreads                 int
	ScrapeSupporterBonusThreads        int
	ScrapeMonthlySupporterBonusThreads int
	XPSupporterBonusPct                int
	XPMonthlySupporterBonusPct         int
	ScrapeDailyPerThread               int
	PointsTextMetadata                 int
	PointsImageMetadata                int
	PointsVideoMetadata                int
	PointsSAPImage                     int
	PointsNewGame                      int
	ScrapeGuestRPM                     int
	ScrapeUserRPM                      int
	KofiVerificationToken              string
	PatreonWebhookSecret               string
	DonorSubscriptionGraceDays         int
	EnableDebugMode                    bool
	CORSOrigins                        []string
}

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfg := loadConfig()
	validateSecrets(cfg)

	// Apply the rewards/scrape economy before any service or repository reads it.
	repository.ConfigureThreads(cfg.ScrapeAdminThreads, cfg.ScrapeGuestThreads, cfg.ScrapeSupporterBonusThreads, cfg.ScrapeMonthlySupporterBonusThreads)
	repository.ConfigureRewards(cfg.ScrapeDailyPerThread, cfg.XPSupporterBonusPct, cfg.XPMonthlySupporterBonusPct)
	repository.ConfigureDonations(cfg.DonorSubscriptionGraceDays)
	services.ConfigurePoints(cfg.PointsTextMetadata, cfg.PointsImageMetadata, cfg.PointsVideoMetadata, cfg.PointsSAPImage, cfg.PointsNewGame)

	db, err := initDatabase(cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database")
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		log.Fatal().Err(err).Msg("Failed to run migrations")
	}

	repo := repository.NewRepository(db)

	// Load the embedded system catalog used for validation and by the web
	// endpoint (internal/systems/systems.json).
	catalog, err := systems.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load system catalog")
	}

	// Seed the initial admin account from environment.
	if cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		hash, err := auth.HashPassword(cfg.AdminPassword)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to hash seed admin password")
		}
		if err := repo.EnsureAdmin(cfg.AdminEmail, hash); err != nil {
			log.Fatal().Err(err).Msg("Failed to seed admin")
		}
		log.Info().Str("email", cfg.AdminEmail).Msg("Seed admin ensured")
	}

	// Ensure the hidden NeoBot integration user exists. NeoBot is the origin of
	// imported catalog data and is never shown in lists/leaderboards. Its password
	// is a random value so nobody can log in as it.
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	nbHash, err := auth.HashPassword("neobot-system-" + hex.EncodeToString(buf))
	if err != nil {
		log.Warn().Err(err).Msg("Failed to hash NeoBot password")
	} else if neobotID, err := repo.EnsureNeoBot(nbHash); err != nil {
		log.Warn().Err(err).Msg("Failed to seed NeoBot user")
	} else {
		log.Info().Str("id", neobotID.String()).Msg("NeoBot user ensured")
	}

	r2Client, err := r2.NewClient(context.Background(), cfg.R2AccountID, cfg.R2AccessKey, cfg.R2SecretKey, cfg.R2BucketName, log.Logger)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize R2 client")
	}

	// Account emails (verification + password reset). If SMTP is not
	// configured the mailer is nil and email sending is skipped.
	mailer, err := email.NewSender(email.Config{
		Host:        cfg.SMTPHost,
		Port:        cfg.SMTPPort,
		Username:    cfg.SMTPUsername,
		Password:    cfg.SMTPPassword,
		FromEmail:   cfg.SMTPFromEmail,
		FromName:    cfg.SMTPFromName,
		FrontendURL: cfg.FrontendURL,
		LogoURL:     cfg.EmailLogoURL,
	})
	if err != nil {
		log.Warn().Err(err).Msg("SMTP not configured, account emails disabled")
		mailer = nil
	}

	svc := services.NewService(repo, r2Client, cfg.R2PublicBase, cfg.JWTSecret, catalog)
	userSvc := services.NewUserService(repo, mailer, cfg.JWTSecret, 30*24*time.Hour, cfg.ProtectedAdminEmail)
	userSvc.SkipEmailVerification = cfg.SkipEmailVerification
	scrapeSvc := services.NewScrapeService(repo, svc, cfg.JWTSecret, cfg.ScrapeGuestThreads, cfg.ScrapeGuestRPM, cfg.ScrapeUserRPM, cfg.EnableDebugMode, !cfg.SkipEmailVerification)
	devSvc := services.NewDeveloperService(repo)
	donationSvc := services.NewDonationService(repo, mailer, cfg.KofiVerificationToken, cfg.PatreonWebhookSecret)
	handler := handlers.NewHandler(svc, userSvc, scrapeSvc, devSvc, donationSvc, cfg.JWTSecret, cfg.SkipEmailVerification)

	// Start background R2 usage refresh
	svc.StartStorageRefresh(context.Background())
	// Downgrade monthly supporters whose Ko-fi subscription stopped paying.
	donationSvc.StartExpiryJob(context.Background())

	r := setupRouter(handler, scrapeSvc, cfg)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      6 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Info().Str("port", cfg.Port).Msg("Starting NeoAssets service")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Server failed to start")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal().Err(err).Msg("Server forced to shutdown")
	}

	log.Info().Msg("Server exited")
}

func loadConfig() *Config {
	return &Config{
		Port:                  getEnvOrDefault("PORT", "8090"),
		DatabaseURL:           getEnvOrDefault("DATABASE_URL", "postgres://user:password@localhost/neoassets?sslmode=disable"),
		JWTSecret:             getEnvOrDefault("JWT_SECRET", "change-me"),
		R2AccountID:           getEnvOrDefault("R2_ACCOUNT_ID", ""),
		R2AccessKey:           getEnvOrDefault("R2_ACCESS_KEY", ""),
		R2SecretKey:           getEnvOrDefault("R2_SECRET_KEY", ""),
		R2BucketName:          getEnvOrDefault("R2_BUCKET_NAME", "neoassets"),
		R2PublicBase:          getEnvOrDefault("R2_PUBLIC_BASE_URL", "https://cdn.neoassets.dev"),
		AdminEmail:            getEnvOrDefault("ADMIN_SEED_EMAIL", ""),
		AdminPassword:         getEnvOrDefault("ADMIN_SEED_PASSWORD", ""),
		ProtectedAdminEmail:   getEnvOrDefault("PROTECTED_ADMIN_EMAIL", "miguel.soto@neostation.dev"),
		SkipEmailVerification: getEnvOrDefault("SKIP_EMAIL_VERIFICATION", "false") == "true",
		SMTPHost:              getEnvOrDefault("SMTP_HOST", ""),
		SMTPPort:              getEnvInt("SMTP_PORT", 587),
		SMTPUsername:          getEnvOrDefault("SMTP_USERNAME", ""),
		SMTPPassword:          getEnvOrDefault("SMTP_PASSWORD", ""),
		SMTPFromEmail:         getEnvOrDefault("SMTP_FROM_EMAIL", ""),
		SMTPFromName:          getEnvOrDefault("SMTP_FROM_NAME", "NeoAssets Team"),
		FrontendURL:           getEnvOrDefault("FRONTEND_URL", "http://localhost:8091"),
		EmailLogoURL:          getEnvOrDefault("EMAIL_LOGO_URL", ""),
		TranslateWorkerURL:    getEnvOrDefault("TRANSLATE_WORKER_URL", ""),
		TranslateWorkerToken:  getEnvOrDefault("TRANSLATE_WORKER_TOKEN", ""),
		// Zero means "use the service default" (guest threads = 2, admin threads = 16,
		// donor bonuses: supporter +2 threads/+25% XP, monthly +4 threads/+50% XP,
		// daily = threads * 1000, XP per contribution 10/40/100/5,
		// guest 10 req/min, user 60 req/min).
		ScrapeGuestThreads:                 getEnvInt("SCRAPE_GUEST_THREADS", 0),
		ScrapeAdminThreads:                 getEnvInt("SCRAPE_ADMIN_THREADS", 0),
		ScrapeSupporterBonusThreads:        getEnvInt("SCRAPE_SUPPORTER_BONUS_THREADS", 0),
		ScrapeMonthlySupporterBonusThreads: getEnvInt("SCRAPE_MONTHLY_SUPPORTER_BONUS_THREADS", 0),
		XPSupporterBonusPct:                getEnvInt("XP_SUPPORTER_BONUS_PCT", 0),
		XPMonthlySupporterBonusPct:         getEnvInt("XP_MONTHLY_SUPPORTER_BONUS_PCT", 0),
		ScrapeDailyPerThread:               getEnvInt("SCRAPE_DAILY_GAMES_PER_THREAD", 0),
		PointsTextMetadata:                 getEnvInt("POINTS_TEXT_METADATA", 0),
		PointsImageMetadata:                getEnvInt("POINTS_IMAGE_METADATA", 0),
		PointsVideoMetadata:                getEnvInt("POINTS_VIDEO_METADATA", 0),
		PointsSAPImage:                     getEnvInt("POINTS_SAP_IMAGE", 0),
		PointsNewGame:                      getEnvInt("POINTS_NEW_GAME", 0),
		ScrapeGuestRPM:                     getEnvInt("SCRAPE_GUEST_RPM", 0),
		ScrapeUserRPM:                      getEnvInt("SCRAPE_USER_RPM", 0),
		KofiVerificationToken:              getEnvOrDefault("KOFI_VERIFICATION_TOKEN", ""),
		PatreonWebhookSecret:               getEnvOrDefault("PATREON_WEBHOOK_SECRET", ""),
		DonorSubscriptionGraceDays:         getEnvInt("DONOR_SUBSCRIPTION_GRACE_DAYS", 0),
		EnableDebugMode:                    getEnvOrDefault("ENABLE_DEBUG_MODE", "false") == "true",
		CORSOrigins:                        csvEnv("CORS_ORIGINS", "http://localhost:5173,http://localhost:8091"),
	}
}

// validateSecrets fails fast when the service would boot with a known or weak
// secret. A publicly known JWT secret lets anyone forge admin tokens, so a
// misconfigured deployment must not start.
func validateSecrets(cfg *Config) {
	if len(cfg.JWTSecret) < 32 || cfg.JWTSecret == "change-me" {
		log.Fatal().Msg("JWT_SECRET must be set to a random value of at least 32 characters")
	}
	if cfg.AdminEmail != "" && (cfg.AdminPassword == "" || cfg.AdminPassword == "change-me") {
		log.Fatal().Msg("ADMIN_SEED_PASSWORD must be set when ADMIN_SEED_EMAIL is configured")
	}
}

// csvEnv splits a comma-separated environment value into a slice, falling back
// to the default when the variable is empty.
func csvEnv(key, defaultValue string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		raw = defaultValue
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return defaultValue
}

func initDatabase(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}

func runMigrations(db *sql.DB) error {
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("failed to create migration driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance("file://./migrations", "postgres", driver)
	if err != nil {
		return fmt.Errorf("failed to create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

func setupRouter(h *handlers.Handler, scrapeSvc *services.ScrapeService, cfg *Config) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(handlers.SecurityHeaders)
	r.Use(handlers.LimitBody)
	// Approving a SAP can move dozens of files to their canonical keys
	// sequentially, which can exceed a minute; give requests more headroom.
	r.Use(middleware.Timeout(5 * time.Minute))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: cfg.CORSOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{
			"Accept", "Authorization", "Content-Type",
			// Public scraping API credential headers.
			"X-Client-Id", "X-Client-Secret", "X-Software-Name", "X-Debug-Password",
		},
		ExposedHeaders: []string{
			"Link",
			// Quota state, readable from browser clients.
			"X-Quota-Limit", "X-Quota-Remaining", "X-Quota-Reset",
		},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Per-IP limiters for the unauthenticated surfaces.
	authLimiter := handlers.NewIPLimiter(10, 10)
	metadataLimiter := handlers.NewIPLimiter(30, 30)
	// The catalog list endpoints (web browse) get a stricter per-IP limit; the
	// scraping API has its own quota and must NOT share this.
	catalogLimiter := handlers.NewIPLimiter(20, 20)
	// Pack downloads are keyed by (IP, pack): a client may install a given pack
	// only a few times per minute, so download counters cannot be inflated by
	// hammering the endpoint while legitimate installs still work.
	downloadLimiter := handlers.NewIPLimiter(4, 3)

	r.Get("/health", h.HealthCheck)

	r.Route("/api/v1", func(r chi.Router) {
		// Public
		r.Get("/packs", h.ListPacks)
		r.With(metadataLimiter.Handler).Get("/packs/{packID}", h.GetPack)
		r.With(metadataLimiter.Handler, downloadLimiter.KeyedHandler(func(r *http.Request) string {
			return handlers.ClientIP(r) + "|" + chi.URLParam(r, "packID")
		})).Get("/packs/{packID}/download", h.DownloadPack)
		r.Get("/systems", h.ListSystems)
		r.Get("/config", h.GetConfig)
		// Community dashboard (leaderboards + recent content) is public so
		// guests can browse it; personal rewards/submissions stay behind auth.
		r.Get("/dashboard", h.DashStats)

		// Donation webhooks (Ko-fi). Unauthenticated by nature; the shared
		// verification token inside the payload is checked in constant time.
		r.Post("/webhooks/kofi", h.KofiWebhook)
		// Patreon member webhooks, verified via the X-Patreon-Signature HMAC.
		r.Post("/webhooks/patreon", h.PatreonWebhook)

		// Metadata catalog (public browse + lookup, per-IP rate limited). The list
		// endpoints get a stricter limiter; no edge cache is used so the catalog
		// is always fresh.
		r.Group(func(r chi.Router) {
			r.Use(metadataLimiter.Handler)
			r.Get("/metadata/systems", h.ListMetadataSystems)
			r.With(catalogLimiter.Handler).Get("/metadata/systems/{id}/games", h.ListGamesBySystem)
			r.With(catalogLimiter.Handler).Get("/metadata/games", h.SearchGames)
			r.Get("/metadata/games/lookup", h.LookupGame)
			r.Get("/metadata/games/{id}", h.GetGameDetail)
			r.Get("/metadata/languages", h.ListMetadataLanguages)
		})

		// Public scraping API. Developer app credentials are always required;
		// user credentials are optional and fall back to guest mode.
		r.Route("/scrape", func(r chi.Router) {
			r.Use(auth.ScrapeMiddleware(scrapeSvc))
			r.Get("/systems", h.ListScrapeSystems)
			r.Get("/families", h.ListScrapeFamilies)
			r.Get("/groups", h.ListScrapeGroups)
			r.Get("/games", h.ScrapeGames)
			r.Get("/popular", h.ListScrapePopular)
			r.Get("/account", h.ScrapeAccount)
		})

		// Account registration, verification and recovery (per-IP rate limited)
		r.Group(func(r chi.Router) {
			r.Use(authLimiter.Handler)
			r.Post("/register", h.Register)
			r.Post("/login", h.Login)
			r.Post("/verify-email", h.VerifyEmail)
			r.Get("/verify-email", h.VerifyEmailByLink)
			r.Post("/resend-verification", h.ResendVerification)
			r.Post("/forgot-password", h.ForgotPassword)
			r.Post("/reset-password", h.ResetPassword)
		})

		// Authenticated submission flow (user JWT)
		r.Group(func(r chi.Router) {
			r.Use(auth.UserMiddleware(cfg.JWTSecret))
			r.Use(h.RequireVerifiedUser)
			r.Get("/rewards", h.GetRewards)
			r.Get("/auth/me", h.GetMe)
			r.Put("/auth/me", h.UpdateProfile)
			r.Post("/auth/password", h.ChangePassword)
			r.Post("/auth/me/avatar/upload", h.AvatarUploadURL)
			r.Post("/auth/me/avatar", h.SetAvatar)
			r.Delete("/auth/me/avatar", h.RemoveAvatar)

			// Donor status claims (donations made with another email)
			r.Post("/auth/donor/claim", h.DonorClaim)
			r.Post("/auth/donor/claim/verify", h.DonorClaimVerify)

			// Public profiles + follows
			r.Get("/users/{username}", h.PublicProfile)
			r.Get("/users/{username}/followers", h.Followers)
			r.Get("/users/{username}/following", h.Following)
			r.Get("/users/{username}/submissions", h.UserSubmissions)
			r.Post("/users/{username}/follow", h.Follow)
			r.Delete("/users/{username}/follow", h.Unfollow)
			r.Get("/auth/submissions", h.ListUserSubmissions)
			r.Get("/auth/submissions/{id}", h.GetUserSubmission)
			r.Post("/submissions", h.CreateSubmission)
			r.Post("/submissions/upload-url", h.SubmissionUploadURLPre)
			r.Put("/submissions/{id}", h.UpdateSubmission)
			r.Post("/submissions/{id}/files", h.AddSubmissionFiles)
			r.Delete("/submissions/{id}/files", h.RemoveSubmissionFile)
			r.Post("/submissions/{id}/upload", h.GetUploadURL)
			r.Post("/submissions/{id}/submit", h.SubmitSubmission)
			r.Post("/submissions/{id}/trash", h.TrashSubmission)

			// Self-service scraping credentials (developer apps + personal API keys)
			r.Get("/auth/developer/apps", h.ListDeveloperApps)
			r.Post("/auth/developer/apps", h.CreateDeveloperApp)
			r.Delete("/auth/developer/apps/{id}", h.RevokeDeveloperApp)
			r.Post("/auth/developer/apps/{id}/rotate", h.RotateDeveloperApp)
			r.Get("/auth/api-keys", h.ListAPIKeys)
			r.Post("/auth/api-keys", h.CreateAPIKey)
			r.Delete("/auth/api-keys/{id}", h.RevokeAPIKey)

			// Metadata contributions
			r.Get("/auth/metadata/submissions", h.ListMyMetadataSubmissions)
			r.Get("/metadata/games/{id}/pending", h.GetMetadataPending)
			r.Post("/metadata/submissions", h.CreateMetadataSubmission)
			r.Post("/metadata/submissions/upload-url", h.MetadataUploadURLPre)
			r.Post("/metadata/submissions/{id}/upload", h.MetadataUploadURL)
			r.Post("/metadata/submissions/{id}/submit", h.SubmitMetadataSubmission)
		})

		// Review (admin or reviewer role, re-checked against the DB)
		r.Post("/admin/login", h.AdminLogin)
		r.Group(func(r chi.Router) {
			r.Use(auth.ReviewMiddleware(cfg.JWTSecret))
			r.Use(h.RequireReviewer)
			r.Get("/admin/submissions", h.ListSubmissions)
			r.Get("/admin/submissions/{id}", h.GetSubmission)
			r.Post("/admin/submissions/{id}/approve", h.Approve)
			r.Post("/admin/submissions/{id}/reject", h.Reject)
			r.Delete("/admin/submissions/{id}", h.DeleteSubmission)
			r.Get("/admin/storage-usage", h.GetStorageUsage)

			// Metadata review
			r.Get("/admin/metadata/submissions", h.ListMetadataSubmissions)
			r.Get("/admin/metadata/submissions/{id}", h.GetMetadataSubmission)
			r.Post("/admin/metadata/submissions/{id}/approve", h.ApproveMetadataSubmission)
			r.Post("/admin/metadata/submissions/{id}/reject", h.RejectMetadataSubmission)
			r.Post("/admin/metadata/games/{id}/retranslate", h.RetranslateGame)
			r.Post("/admin/metadata/systems/{id}/retranslate", h.RetranslateSystem)
		})

		// Admin only (role management, re-checked against the DB)
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware(cfg.JWTSecret))
			r.Use(h.RequireAdmin)
			r.Get("/admin/users", h.ListUsers)
			r.Put("/admin/users/{id}/role", h.SetUserRole)
			r.Put("/admin/users/{id}/donor", h.SetDonorStatus)
			r.Post("/admin/donations/import", h.ImportDonations)
		})
	})

	return r
}
