package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"neoassets/internal/economy"
	"neoassets/internal/email"
	"neoassets/internal/handlers"
	"neoassets/internal/repository"
	"neoassets/internal/services"
	"neoassets/internal/sitemap"
	"neoassets/internal/systems"
	"neoassets/pkg/auth"
	"neoassets/pkg/r2"

	"github.com/rs/zerolog/log"
)

// userSessionTTL is how long a user session lives. A password change bumps
// token_version and revokes every outstanding token immediately.
const userSessionTTL = 7 * 24 * time.Hour

// app is the wired service: the HTTP router plus the services that own
// background jobs, which main starts and stops.
type app struct {
	router      http.Handler
	svc         *services.Service
	scrapeSvc   *services.ScrapeService
	donationSvc *services.DonationService
}

// economyFromConfig builds the rewards/scrape economy: the defaults with any
// positive configured value applied (an unset variable keeps the default).
func economyFromConfig(cfg *Config) economy.Config {
	return economy.Default().Override(economy.Config{
		AdminThreads:                 cfg.ScrapeAdminThreads,
		GuestThreads:                 cfg.ScrapeGuestThreads,
		SupporterBonusThreads:        cfg.ScrapeSupporterBonusThreads,
		MonthlySupporterBonusThreads: cfg.ScrapeMonthlySupporterBonusThreads,
		SupporterXPBonusPct:          cfg.XPSupporterBonusPct,
		MonthlySupporterXPBonusPct:   cfg.XPMonthlySupporterBonusPct,
		DailyGamesPerThread:          cfg.ScrapeDailyPerThread,
		DonorGraceDays:               cfg.DonorSubscriptionGraceDays,
		Points: economy.Points{
			TextMetadata:  cfg.PointsTextMetadata,
			ImageMetadata: cfg.PointsImageMetadata,
			VideoMetadata: cfg.PointsVideoMetadata,
			SAPImage:      cfg.PointsSAPImage,
			NewGame:       cfg.PointsNewGame,
		},
	})
}

// newMailer returns the account mailer (verification and password reset), or
// nil when SMTP is not configured, which disables account emails.
func newMailer(cfg *Config) *email.Sender {
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
		return nil
	}
	return mailer
}

// buildApp wires the repository, services, handlers and router over a
// migrated database, the object store and the mailer (nil disables account
// emails). It also seeds the configured admin and the hidden NeoBot user.
// main and the HTTP tests share it, so tests exercise the production wiring.
func buildApp(ctx context.Context, cfg *Config, db *sql.DB, bucket r2.Client, mailer *email.Sender) (*app, error) {
	repo := repository.NewRepository(db, repository.WithEconomy(economyFromConfig(cfg)))

	// The embedded system catalog used for validation and by the web endpoint
	// (internal/systems/systems.json).
	catalog, err := systems.Load()
	if err != nil {
		return nil, fmt.Errorf("load system catalog: %w", err)
	}

	if err := seedAdmin(ctx, cfg, repo); err != nil {
		return nil, err
	}
	seedNeoBot(ctx, repo)

	svc := services.NewService(repo, bucket, cfg.R2PublicBase, cfg.JWTSecret, catalog,
		services.WithTranslator(services.NewTranslator(cfg.TranslateWorkerURL, cfg.TranslateWorkerToken)))
	userSvc := services.NewUserService(repo, mailer, cfg.JWTSecret, userSessionTTL, cfg.ProtectedAdminEmail)
	userSvc.SkipEmailVerification = cfg.SkipEmailVerification
	scrapeSvc := services.NewScrapeService(repo, svc, cfg.JWTSecret, cfg.ScrapeGuestThreads, cfg.ScrapeRPMPerThread, cfg.EnableDebugMode, !cfg.SkipEmailVerification)
	devSvc := services.NewDeveloperService(repo)
	donationSvc := services.NewDonationService(repo, mailer, cfg.KofiVerificationToken, cfg.PatreonWebhookSecret)
	handler := handlers.NewHandler(svc, userSvc, scrapeSvc, devSvc, donationSvc, cfg.JWTSecret, cfg.SkipEmailVerification)

	return &app{
		router:      setupRouter(handler, scrapeSvc, sitemap.NewHandler(repo, cfg.FrontendURL), cfg),
		svc:         svc,
		scrapeSvc:   scrapeSvc,
		donationSvc: donationSvc,
	}, nil
}

// startJobs starts the background jobs; they stop when ctx is cancelled.
func (a *app) startJobs(ctx context.Context) {
	// Refresh the R2 storage usage shown to admins.
	a.svc.StartStorageRefresh(ctx)
	// Downgrade monthly supporters whose Ko-fi subscription stopped paying.
	a.donationSvc.StartExpiryJob(ctx)
	// Write the batched counts of rate-limited scrape requests.
	a.scrapeSvc.StartRefusalFlush(ctx)
}

// seedAdmin ensures the initial admin account from the environment exists.
func seedAdmin(ctx context.Context, cfg *Config, repo *repository.Repository) error {
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminPassword)
	if err != nil {
		return fmt.Errorf("hash seed admin password: %w", err)
	}
	if err := repo.EnsureAdmin(ctx, cfg.AdminEmail, hash); err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	log.Info().Msg("Seed admin ensured")
	return nil
}

// seedNeoBot ensures the hidden NeoBot integration user exists. NeoBot is the
// origin of imported catalog data and is never shown in lists or leaderboards.
// Its password is a random value so nobody can log in as it. Failures are
// logged, not fatal, as before.
func seedNeoBot(ctx context.Context, repo *repository.Repository) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	nbHash, err := auth.HashPassword("neobot-system-" + hex.EncodeToString(buf))
	if err != nil {
		log.Warn().Err(err).Msg("Failed to hash NeoBot password")
		return
	}
	neobotID, err := repo.EnsureNeoBot(ctx, nbHash)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to seed NeoBot user")
		return
	}
	log.Info().Str("id", neobotID.String()).Msg("NeoBot user ensured")
}
