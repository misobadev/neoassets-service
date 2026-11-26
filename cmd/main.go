package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"neoassets/internal/handlers"
	"neoassets/internal/services"
	"neoassets/internal/sitemap"
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
	DBMaxOpenConns                     int
	DBStatementTimeout                 time.Duration
	CatalogCacheTTL                    time.Duration
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
	PointsTextMetadata                 int
	PointsImageMetadata                int
	PointsVideoMetadata                int
	PointsSAPImage                     int
	PointsNewGame                      int
	ScrapeRPMPerThread                 int
	ScrapeMaxInFlight                  int
	ScrapeGuestRPMPerThread            int
	ScrapeGuestMaxInFlight             int
	ScrapeGuestOpenThreads             int
	ScrapeUserOpenThreads              int
	KofiVerificationToken              string
	PatreonWebhookSecret               string
	DonorSubscriptionGraceDays         int
	EnableDebugMode                    bool
	CORSOrigins                        []string
	CDNIPRanges                        string
	CDNClientIPHeader                  string
}

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfg := loadConfig()
	validateSecrets(cfg)
	if cfg.ProtectedAdminEmail == "" {
		log.Warn().Msg("PROTECTED_ADMIN_EMAIL is not set: no admin account is protected from demotion or deletion")
	}
	// The CDN whose client-address header the per-IP rate limits trust.
	if err := handlers.ConfigureCDN(cfg.CDNIPRanges, cfg.CDNClientIPHeader); err != nil {
		log.Fatal().Err(err).Msg("Invalid CDN configuration")
	}

	// Migrations run on their own connection, without the statement timeout:
	// a data migration may legitimately take longer than any request should.
	if err := runMigrations(cfg.DatabaseURL); err != nil {
		log.Fatal().Err(err).Msg("Failed to run migrations")
	}

	db, err := openDatabase(cfg.DatabaseURL, cfg.DBStatementTimeout, cfg.DBMaxOpenConns)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database")
	}
	defer db.Close()

	r2Client, err := r2.NewClient(context.Background(), cfg.R2AccountID, cfg.R2AccessKey, cfg.R2SecretKey, cfg.R2BucketName, log.Logger)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize R2 client")
	}

	a, err := buildApp(context.Background(), cfg, db, r2Client, newMailer(cfg))
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to start the service")
	}

	// Background jobs stop when the server shuts down.
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	defer stopJobs()
	a.startJobs(jobsCtx)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           a.router,
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
	stopJobs()
	a.scrapeSvc.FlushOutcomes(ctx)

	log.Info().Msg("Server exited")
}

func loadConfig() *Config {
	return &Config{
		Port:                  getEnvOrDefault("PORT", "8090"),
		DatabaseURL:           getEnvOrDefault("DATABASE_URL", "postgres://user:password@localhost/neoassets?sslmode=disable"),
		DBMaxOpenConns:        getEnvInt("DB_MAX_OPEN_CONNS", 20),
		DBStatementTimeout:    time.Duration(getEnvInt("DB_STATEMENT_TIMEOUT_SECONDS", 30)) * time.Second,
		CatalogCacheTTL:       time.Duration(getEnvInt("CATALOG_CACHE_SECONDS", int(services.DefaultCatalogCacheTTL/time.Second))) * time.Second,
		JWTSecret:             getEnvOrDefault("JWT_SECRET", "change-me"),
		R2AccountID:           getEnvOrDefault("R2_ACCOUNT_ID", ""),
		R2AccessKey:           getEnvOrDefault("R2_ACCESS_KEY", ""),
		R2SecretKey:           getEnvOrDefault("R2_SECRET_KEY", ""),
		R2BucketName:          getEnvOrDefault("R2_BUCKET_NAME", "neoassets"),
		R2PublicBase:          getEnvOrDefault("R2_PUBLIC_BASE_URL", "https://cdn.neoassets.dev"),
		AdminEmail:            getEnvOrDefault("ADMIN_SEED_EMAIL", ""),
		AdminPassword:         getEnvOrDefault("ADMIN_SEED_PASSWORD", ""),
		ProtectedAdminEmail:   strings.TrimSpace(os.Getenv("PROTECTED_ADMIN_EMAIL")),
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
		// Zero means "use the service default" (guest threads = 1, admin threads = 20,
		// donor bonuses: supporter +8 threads/+25% XP, monthly +12 threads/+50% XP,
		// RPM = threads * 60 (guests threads * 30), no daily limit, XP per
		// contribution 10/50/100/20/100).
		ScrapeGuestThreads:                 getEnvInt("SCRAPE_GUEST_THREADS", 0),
		ScrapeAdminThreads:                 getEnvInt("SCRAPE_ADMIN_THREADS", 0),
		ScrapeSupporterBonusThreads:        getEnvInt("SCRAPE_SUPPORTER_BONUS_THREADS", 0),
		ScrapeMonthlySupporterBonusThreads: getEnvInt("SCRAPE_MONTHLY_SUPPORTER_BONUS_THREADS", 0),
		XPSupporterBonusPct:                getEnvInt("XP_SUPPORTER_BONUS_PCT", 0),
		XPMonthlySupporterBonusPct:         getEnvInt("XP_MONTHLY_SUPPORTER_BONUS_PCT", 0),
		PointsTextMetadata:                 getEnvInt("POINTS_TEXT_METADATA", 0),
		PointsImageMetadata:                getEnvInt("POINTS_IMAGE_METADATA", 0),
		PointsVideoMetadata:                getEnvInt("POINTS_VIDEO_METADATA", 0),
		PointsSAPImage:                     getEnvInt("POINTS_SAP_IMAGE", 0),
		PointsNewGame:                      getEnvInt("POINTS_NEW_GAME", 0),
		ScrapeRPMPerThread:                 getEnvInt("SCRAPE_RPM_PER_THREAD", 0),
		ScrapeMaxInFlight:                  getEnvInt("SCRAPE_MAX_INFLIGHT", 0),
		ScrapeGuestRPMPerThread:            getEnvInt("SCRAPE_GUEST_RPM_PER_THREAD", 0),
		ScrapeGuestOpenThreads:             getEnvInt("SCRAPE_GUEST_OPEN_THREADS", 0),
		ScrapeUserOpenThreads:              getEnvInt("SCRAPE_USER_OPEN_THREADS", 0),
		ScrapeGuestMaxInFlight:             getEnvInt("SCRAPE_GUEST_MAX_INFLIGHT", 0),
		KofiVerificationToken:              getEnvOrDefault("KOFI_VERIFICATION_TOKEN", ""),
		PatreonWebhookSecret:               getEnvOrDefault("PATREON_WEBHOOK_SECRET", ""),
		DonorSubscriptionGraceDays:         getEnvInt("DONOR_SUBSCRIPTION_GRACE_DAYS", 0),
		EnableDebugMode:                    getEnvOrDefault("ENABLE_DEBUG_MODE", "false") == "true",
		CORSOrigins:                        csvEnv("CORS_ORIGINS", "http://localhost:5173,http://localhost:8091"),
		CDNIPRanges:                        getEnvOrDefault("CDN_IP_RANGES", ""),
		CDNClientIPHeader:                  getEnvOrDefault("CDN_CLIENT_IP_HEADER", ""),
	}
}

// validateSecrets fails fast when the service would boot with a known or weak
// secret. A publicly known JWT secret lets anyone forge admin tokens, so a
// misconfigured deployment must not start.
func validateSecrets(cfg *Config) {
	if err := checkSecrets(cfg); err != nil {
		log.Fatal().Msg(err.Error())
	}
}

// checkSecrets rejects a short JWT_SECRET and any value still carrying the
// "change-me" placeholder from .env.example, whatever its length.
func checkSecrets(cfg *Config) error {
	if len(cfg.JWTSecret) < 32 || isPlaceholder(cfg.JWTSecret) {
		return fmt.Errorf("JWT_SECRET is unset, shorter than 32 characters, or still the .env.example placeholder: " +
			"set a random value, e.g. `openssl rand -base64 48`")
	}
	if cfg.AdminEmail != "" && (cfg.AdminPassword == "" || isPlaceholder(cfg.AdminPassword)) {
		return fmt.Errorf("ADMIN_SEED_PASSWORD is unset or still the .env.example placeholder: set a strong password, " +
			"or unset ADMIN_SEED_EMAIL once the admin account exists")
	}
	return nil
}

// isPlaceholder reports whether a secret still contains the example value's
// "change-me" marker (in any case, with or without the hyphen).
func isPlaceholder(secret string) bool {
	s := strings.ToLower(secret)
	return strings.Contains(s, "change-me") || strings.Contains(s, "changeme")
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

// openDatabase opens the service's connection pool. It holds at most maxOpen
// connections, so a burst of slow requests waits for a connection instead of
// using up the database server's slots, and every statement runs under
// statementTimeout, which Postgres enforces itself, so a query whose HTTP
// request has already gone doesn't run on (0 disables either limit).
func openDatabase(databaseURL string, statementTimeout time.Duration, maxOpen int) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseDSN(databaseURL, statementTimeout))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	configurePool(db, maxOpen)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}

// databaseDSN adds a statement_timeout to the connection string, unless the
// operator already set one there. lib/pq sends keys it doesn't know to the
// server as session settings, so every pooled connection gets it.
func databaseDSN(raw string, timeout time.Duration) string {
	if timeout <= 0 {
		return raw
	}
	ms := strconv.FormatInt(timeout.Milliseconds(), 10)
	if strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://") {
		u, err := url.Parse(raw)
		if err != nil {
			// sql.Open reports the malformed URL itself.
			return raw
		}
		q := u.Query()
		if q.Has("statement_timeout") || strings.Contains(q.Get("options"), "statement_timeout") {
			return raw
		}
		base, fragment, hasFragment := strings.Cut(raw, "#")
		sep := "?"
		if strings.Contains(base, "?") {
			sep = "&"
		}
		out := base + sep + "statement_timeout=" + ms
		if hasFragment {
			out += "#" + fragment
		}
		return out
	}
	if dsnSetsStatementTimeout.MatchString(raw) {
		return raw
	}
	return strings.TrimSpace(raw) + " statement_timeout=" + ms
}

// dsnSetsStatementTimeout matches a key/value connection string that already
// sets a statement timeout, directly or through options='-c ...'.
var dsnSetsStatementTimeout = regexp.MustCompile(`statement_timeout\s*=`)

// configurePool bounds the connection pool (0 leaves it unbounded).
func configurePool(db *sql.DB, maxOpen int) {
	if maxOpen <= 0 {
		return
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxIdleTime(5 * time.Minute)
}

func runMigrations(databaseURL string) error {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return fmt.Errorf("failed to open database for migrations: %w", err)
	}
	defer db.Close()

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("failed to create migration driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance("file://./migrations", "postgres", driver)
	if err != nil {
		return fmt.Errorf("failed to create migrate instance: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

func setupRouter(h *handlers.Handler, scrapeSvc *services.ScrapeService, sitemaps *sitemap.Handler, cfg *Config) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(handlers.SecurityHeaders)
	r.Use(handlers.LimitBody)
	// Approving a SAP can move dozens of files to their canonical keys
	// sequentially, which can exceed a minute; give requests more headroom.
	r.Use(middleware.Timeout(5 * time.Minute))
	// Writes finish even if the client disconnects; reads are cancelled.
	r.Use(handlers.DetachWrites)

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
			// Rate and concurrency state, readable from browser clients.
			"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-Concurrency-Limit", "Retry-After",
		},
		// The browser session is an httpOnly cookie, so credentialed requests
		// must be allowed. AllowedOrigins is an explicit allow-list (never "*").
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Per-IP limiters for the unauthenticated surfaces.
	authLimiter := handlers.NewIPLimiter(10, 10)
	// The dashboard is public and runs several aggregate queries per request,
	// so it gets its own (generous but bounded) per-IP limiter.
	dashboardLimiter := handlers.NewIPLimiter(60, 30)
	// Donation webhooks are unauthenticated; limit them to bound abuse churn.
	webhookLimiter := handlers.NewIPLimiter(60, 30)
	// Browsing the catalog pages quickly (pagination, opening details) must not
	// hit the limiter, so these are generous; the scraping API has its own quota
	// and must NOT share them.
	metadataLimiter := handlers.NewIPLimiter(240, 240)
	catalogLimiter := handlers.NewIPLimiter(240, 240)
	// Pack downloads are keyed by (IP, pack): a client may install a given pack
	// up to ten times per minute, so download counters cannot be inflated by
	// hammering the endpoint while legitimate installs still work.
	downloadLimiter := handlers.NewIPLimiter(10, 10)
	// Donor claims are keyed by account: a few code requests and checks a
	// minute, on top of the per-claim attempt limit and the code cooldown.
	donorClaimLimiter := handlers.NewIPLimiter(10, 5)
	byUser := func(r *http.Request) string {
		if claims, ok := auth.UserFromContext(r.Context()); ok && claims != nil {
			return "user:" + claims.UserID.String()
		}
		return "ip:" + handlers.ClientIP(r)
	}

	r.Get("/health", h.HealthCheck)

	// Sitemaps of the public web pages (games, systems, art packs), referenced
	// from the web's robots.txt.
	r.Group(func(r chi.Router) {
		r.Use(metadataLimiter.Handler)
		sitemaps.Routes(r)
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Public
		r.With(metadataLimiter.Handler).Get("/packs", h.ListPacks)
		r.With(metadataLimiter.Handler).Get("/packs/{packID}", h.GetPack)
		r.With(metadataLimiter.Handler, downloadLimiter.KeyedHandler(func(r *http.Request) string {
			return handlers.ClientKey(r) + "|" + chi.URLParam(r, "packID")
		})).Get("/packs/{packID}/download", h.DownloadPack)
		r.Get("/systems", h.ListSystems)
		r.Get("/config", h.GetConfig)
		// Clears the httpOnly session cookies. Public and side-effect free.
		r.Post("/logout", h.Logout)
		// Community dashboard (leaderboards + recent content) is public so
		// guests can browse it; personal rewards/submissions stay behind auth.
		r.With(dashboardLimiter.Handler).Get("/dashboard", h.DashStats)

		// Donation webhooks (Ko-fi). Unauthenticated by nature; the shared
		// verification token inside the payload is checked in constant time.
		r.With(webhookLimiter.Handler).Post("/webhooks/kofi", h.KofiWebhook)
		// Patreon member webhooks, verified via the X-Patreon-Signature HMAC.
		r.With(webhookLimiter.Handler).Post("/webhooks/patreon", h.PatreonWebhook)

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
			r.Get("/metadata/genres", h.ListGenres)
			r.Get("/metadata/regions", h.ListRegions)
		})

		// Live, public state of the scraping API (no credentials, cached).
		r.With(dashboardLimiter.Handler).Get("/scrape/status", h.ScrapeStatus)

		// Public scraping API. Developer app credentials are always required;
		// user credentials are optional and fall back to guest mode.
		r.Route("/scrape", func(r chi.Router) {
			r.Use(auth.ScrapeMiddleware(scrapeSvc, handlers.ClientKey))
			// An account's threads are its concurrent requests.
			r.Use(h.ScrapeAdmission)
			r.Get("/systems", h.ListScrapeSystems)
			r.Get("/families", h.ListScrapeFamilies)
			r.Get("/groups", h.ListScrapeGroups)
			// Game lookups also share a global in-flight cap.
			r.With(h.ScrapeGlobalAdmission).Get("/games", h.ScrapeGames)
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
			r.With(donorClaimLimiter.KeyedHandler(byUser)).Post("/auth/donor/claim", h.DonorClaim)
			r.With(donorClaimLimiter.KeyedHandler(byUser)).Post("/auth/donor/claim/verify", h.DonorClaimVerify)

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
			r.Get("/auth/api-keys/{id}/key", h.RevealAPIKey)

			// Notification badge (lightweight review feed)
			r.Get("/auth/reviews/summary", h.ReviewSummary)

			// Metadata contributions
			r.Get("/auth/metadata/submissions", h.ListMyMetadataSubmissions)
			r.Get("/metadata/games/{id}/pending", h.GetMetadataPending)
			r.Post("/metadata/submissions", h.CreateMetadataSubmission)
			r.Post("/metadata/submissions/upload-url", h.MetadataUploadURLPre)
			r.Post("/metadata/submissions/{id}/upload", h.MetadataUploadURL)
			r.Post("/metadata/submissions/{id}/submit", h.SubmitMetadataSubmission)
			r.Delete("/metadata/submissions/{id}", h.CancelMetadataSubmission)
		})

		// Review (admin or reviewer role, re-checked against the DB)
		r.With(authLimiter.Handler).Post("/admin/login", h.AdminLogin)
		r.Group(func(r chi.Router) {
			r.Use(auth.ReviewMiddleware(cfg.JWTSecret))
			r.Use(h.RequireReviewer)
			r.Get("/admin/submissions", h.ListSubmissions)
			r.Get("/admin/submissions/{id}", h.GetSubmission)
			r.Post("/admin/submissions/{id}/approve", h.Approve)
			r.Post("/admin/submissions/{id}/reject", h.Reject)
			r.Get("/admin/storage-usage", h.GetStorageUsage)

			// Metadata review
			r.Get("/admin/metadata/submissions", h.ListMetadataSubmissions)
			r.Get("/admin/metadata/submissions/{id}", h.GetMetadataSubmission)
			r.Post("/admin/metadata/submissions/{id}/approve", h.ApproveMetadataSubmission)
			r.Post("/admin/metadata/submissions/{id}/reject", h.RejectMetadataSubmission)
			r.Post("/admin/metadata/games/{id}/retranslate", h.RetranslateGame)
			r.Post("/admin/metadata/systems/{id}/retranslate", h.RetranslateSystem)
		})

		// Admin only, re-checked against the DB. Like the review routes it
		// accepts the normal user session, so an admin is not locked out when
		// the shorter-lived admin cookie expires before the user session.
		r.Group(func(r chi.Router) {
			r.Use(auth.ReviewMiddleware(cfg.JWTSecret))
			r.Use(h.RequireAdmin)
			r.Get("/admin/users", h.ListUsers)
			r.Put("/admin/users/{id}/role", h.SetUserRole)
			r.Put("/admin/users/{id}/donor", h.SetDonorStatus)
			r.Post("/admin/donations/import", h.ImportDonations)
			// Deleting a submission removes its files for good: admin only, as
			// documented (it used to sit in the reviewer group).
			r.Delete("/admin/submissions/{id}", h.DeleteSubmission)
			r.Delete("/admin/packs/{packID}", h.DeletePack)
			r.Put("/admin/packs/{packID}/donation", h.SetPackDonationURL)
			r.Get("/admin/developer", h.AdminDeveloperOverview)
		})
	})

	return r
}
