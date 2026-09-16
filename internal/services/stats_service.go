package services

import (
	"neoassets/internal/models"
)

// Dashboard gathers the app home leaderboards and recent published content
// (SAP + game metadata). Hidden users (NeoBot) are excluded from every list.
func (s *Service) Dashboard() (*models.Dashboard, error) {
	totalGames, err := s.repo.CountGames()
	if err != nil {
		return nil, err
	}
	totalPacks, err := s.repo.CountApprovedPacks()
	if err != nil {
		return nil, err
	}
	totalSystems, err := s.repo.CountMetadataSystems()
	if err != nil {
		return nil, err
	}
	totalUsers, err := s.repo.CountUsers()
	if err != nil {
		return nil, err
	}
	totalContributions, err := s.repo.CountApprovedContributions()
	if err != nil {
		return nil, err
	}
	topReviewers, err := s.repo.TopReviewers(5)
	if err != nil {
		return nil, err
	}
	topSubmitters, err := s.repo.TopSubmitters(10)
	if err != nil {
		return nil, err
	}
	topApproved, err := s.repo.TopApproved(10)
	if err != nil {
		return nil, err
	}
	topLevels, err := s.repo.TopLevels(10)
	if err != nil {
		return nil, err
	}
	topGames, err := s.repo.ListPopularGames("", 10)
	if err != nil {
		return nil, err
	}
	recentPacks, err := s.repo.RecentApprovedPacks(3)
	if err != nil {
		return nil, err
	}
	recentMetadata, err := s.repo.RecentApprovedMetadata(3)
	if err != nil {
		return nil, err
	}
	return &models.Dashboard{
		TotalGames:         totalGames,
		TotalPacks:         totalPacks,
		TotalSystems:       totalSystems,
		TotalUsers:         totalUsers,
		TotalContributions: totalContributions,
		TopReviewers:       topReviewers,
		TopSubmitters:      topSubmitters,
		TopApproved:        topApproved,
		TopLevels:          topLevels,
		TopGames:           topGames,
		RecentPacks:        recentPacks,
		RecentMetadata:     recentMetadata,
	}, nil
}
