package services

import (
	"context"
	"neoassets/internal/models"
)

// Dashboard gathers the app home leaderboards and recent published content
// (SAP + game metadata). Hidden users (NeoBot) are excluded from every list.
func (s *Service) Dashboard(ctx context.Context) (*models.Dashboard, error) {
	totalGames, err := s.repo.CountGames(ctx)
	if err != nil {
		return nil, err
	}
	totalPacks, err := s.repo.CountApprovedPacks(ctx)
	if err != nil {
		return nil, err
	}
	totalSystems, err := s.repo.CountMetadataSystems(ctx)
	if err != nil {
		return nil, err
	}
	totalUsers, err := s.repo.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	totalContributions, err := s.repo.CountApprovedContributions(ctx)
	if err != nil {
		return nil, err
	}
	topContributions, err := s.repo.TopContributions(ctx, 10)
	if err != nil {
		return nil, err
	}
	topApprovedWeek, err := s.repo.TopApprovedThisWeek(ctx, 10)
	if err != nil {
		return nil, err
	}
	topLevels, err := s.repo.TopLevels(ctx, 10)
	if err != nil {
		return nil, err
	}
	topGames, err := s.repo.ListPopularGames(ctx, "", 10)
	if err != nil {
		return nil, err
	}
	topSystems, err := s.repo.ListPopularSystems(ctx, 10)
	if err != nil {
		return nil, err
	}
	recentPacks, err := s.repo.RecentApprovedPacks(ctx, 5)
	if err != nil {
		return nil, err
	}
	recentMetadata, err := s.repo.RecentApprovedMetadata(ctx, 5)
	if err != nil {
		return nil, err
	}
	dash := &models.Dashboard{
		TotalGames:         totalGames,
		TotalPacks:         totalPacks,
		TotalSystems:       totalSystems,
		TotalUsers:         totalUsers,
		TotalContributions: totalContributions,
		TopContributions:   topContributions,
		TopApprovedWeek:    topApprovedWeek,
		TopLevels:          topLevels,
		TopGames:           topGames,
		TopSystems:         topSystems,
		RecentPacks:        recentPacks,
		RecentMetadata:     recentMetadata,
	}
	// Only the total used space is exposed publicly (from the hourly cache); the
	// media/DB breakdown stays behind the admin endpoint.
	if u := s.GetCachedStorageUsage(); u != nil {
		dash.Storage = &models.DashboardStorage{TotalBytes: u.TotalBytes}
	}
	return dash, nil
}
