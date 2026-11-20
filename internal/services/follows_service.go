package services

import (
	"context"
	"neoassets/internal/apperr"

	"github.com/google/uuid"

	"neoassets/internal/models"
	"neoassets/internal/progression"
)

// PublicProfile returns a user's public profile, excluding hidden users. If
// requesterID is set, it also reports whether that requester already follows.
func (s *Service) PublicProfile(ctx context.Context, username string, requesterID *uuid.UUID) (*models.PublicProfile, error) {
	u, err := s.repo.PublicProfileByUsername(ctx, username)
	if err != nil {
		return nil, apperr.NotFound("user not found")
	}
	followers, following, err := s.repo.FollowCounts(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	approved, submitted, err := s.repo.UserStats(ctx, u.ID)
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
		Threads:     s.repo.Economy().ThreadsForUser(u.Role, u.DonorStatus, u.XP),
		DonorStatus: u.DonorStatus,
		AvatarKey:   u.AvatarKey,
		Approved:    approved,
		Submitted:   submitted,
		Followers:   followers,
		Following:   following,
	}
	if requesterID != nil && *requesterID != u.ID {
		oof, err := s.repo.IsFollowing(ctx, *requesterID, u.ID)
		if err != nil {
			return nil, err
		}
		p.IsFollowing = oof
	}
	return p, nil
}

// Follow makes the follower user follow the user identified by username.
func (s *Service) Follow(ctx context.Context, followerID uuid.UUID, username string) error {
	target, err := s.repo.PublicProfileByUsername(ctx, username)
	if err != nil {
		return apperr.NotFound("user not found")
	}
	if target.ID == followerID {
		return apperr.Invalid("you cannot follow yourself")
	}
	return s.repo.Follow(ctx, followerID, target.ID)
}

// Unfollow removes a follow relationship by username.
func (s *Service) Unfollow(ctx context.Context, followerID uuid.UUID, username string) error {
	target, err := s.repo.PublicProfileByUsername(ctx, username)
	if err != nil {
		return apperr.NotFound("user not found")
	}
	return s.repo.Unfollow(ctx, followerID, target.ID)
}

// Followers returns the usernames following the given user.
func (s *Service) Followers(ctx context.Context, username string) ([]string, error) {
	if _, err := s.repo.PublicProfileByUsername(ctx, username); err != nil {
		return nil, apperr.NotFound("user not found")
	}
	return s.repo.FollowersByUsername(ctx, username)
}

// Following returns the usernames the given user follows.
func (s *Service) Following(ctx context.Context, username string) ([]string, error) {
	if _, err := s.repo.PublicProfileByUsername(ctx, username); err != nil {
		return nil, apperr.NotFound("user not found")
	}
	return s.repo.FollowingByUsername(ctx, username)
}

// RecentSubmissions returns the latest submissions (SAP + metadata) for a user.
func (s *Service) RecentSubmissions(ctx context.Context, username string, limit int) ([]models.UserSubmissionItem, error) {
	u, err := s.repo.PublicProfileByUsername(ctx, username)
	if err != nil {
		return nil, apperr.NotFound("user not found")
	}
	return s.repo.RecentUserSubmissions(ctx, u.ID, limit)
}
