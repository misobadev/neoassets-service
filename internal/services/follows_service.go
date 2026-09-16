package services

import (
	"fmt"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/progression"
	"neoassets/internal/repository"
)

// PublicProfile returns a user's public profile, excluding hidden users. If
// requesterID is set, it also reports whether that requester already follows.
func (s *Service) PublicProfile(username string, requesterID *uuid.UUID) (*models.PublicProfile, error) {
	u, err := s.repo.PublicProfileByUsername(username)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	followers, following, err := s.repo.FollowCounts(u.ID)
	if err != nil {
		return nil, err
	}
	approved, submitted, err := s.repo.UserStats(u.ID)
	if err != nil {
		return nil, err
	}
	level := progression.LevelForXP(u.XP)
	p := &models.PublicProfile{
		ID:          u.ID,
		Username:    u.Username,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
		XP:          u.XP,
		Level:       level,
		Rank:        progression.RankForLevel(level),
		Threads:     repository.ThreadsForUser(u.Role, u.DonorStatus, u.XP),
		DonorStatus: u.DonorStatus,
		AvatarKey:   u.AvatarKey,
		Approved:    approved,
		Submitted:   submitted,
		Followers:   followers,
		Following:   following,
	}
	if requesterID != nil && *requesterID != u.ID {
		oof, err := s.repo.IsFollowing(*requesterID, u.ID)
		if err != nil {
			return nil, err
		}
		p.IsFollowing = oof
	}
	return p, nil
}

// Follow makes the follower user follow the user identified by username.
func (s *Service) Follow(followerID uuid.UUID, username string) error {
	target, err := s.repo.PublicProfileByUsername(username)
	if err != nil {
		return fmt.Errorf("user not found")
	}
	if target.ID == followerID {
		return fmt.Errorf("you cannot follow yourself")
	}
	return s.repo.Follow(followerID, target.ID)
}

// Unfollow removes a follow relationship by username.
func (s *Service) Unfollow(followerID uuid.UUID, username string) error {
	target, err := s.repo.PublicProfileByUsername(username)
	if err != nil {
		return fmt.Errorf("user not found")
	}
	return s.repo.Unfollow(followerID, target.ID)
}

// Followers returns the usernames following the given user.
func (s *Service) Followers(username string) ([]string, error) {
	if _, err := s.repo.PublicProfileByUsername(username); err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return s.repo.FollowersByUsername(username)
}

// Following returns the usernames the given user follows.
func (s *Service) Following(username string) ([]string, error) {
	if _, err := s.repo.PublicProfileByUsername(username); err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return s.repo.FollowingByUsername(username)
}

// RecentSubmissions returns the latest submissions (SAP + metadata) for a user.
func (s *Service) RecentSubmissions(username string, limit int) ([]models.UserSubmissionItem, error) {
	u, err := s.repo.PublicProfileByUsername(username)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return s.repo.RecentUserSubmissions(u.ID, limit)
}
