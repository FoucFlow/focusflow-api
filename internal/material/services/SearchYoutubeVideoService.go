package services

import (
	"fmt"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
)

// SearchCourseMaterialRepositoryService defines the business-level contract for video search.
// The handler layer depends on this interface, making it testable with mocks.
type SearchCourseMaterialRepositoryService interface {
	// SearchYoutubeVideo searches YouTube for videos matching the query, up to maxResults.
	SearchYoutubeVideo(query string, maxResults int) ([]model.YouTubeSearchResult, error)

	// GetPlaylistItems expands a YouTube playlist into its video items.
	GetPlaylistItems(playlistID string, maxResults int) ([]model.YouTubeSearchResult, error)

	// LookupVideo returns metadata for a single YouTube video by ID.
	LookupVideo(videoID string) (model.YouTubeSearchResult, error)
}

// searchCourseMaterialService is the concrete implementation of SearchCourseMaterialRepositoryService.
type searchCourseMaterialService struct {
	repo repository.SearchCourseMaterialRepository
}

// NewSearchCourseMaterialService creates a new search service with the given repository.
func NewSearchCourseMaterialService(repo repository.SearchCourseMaterialRepository) SearchCourseMaterialRepositoryService {
	return &searchCourseMaterialService{
		repo: repo,
	}
}

// SearchYoutubeVideo applies business rules to the search request before delegating
// to the repository. It validates the query and clamps maxResults to a safe range (1–50).
func (s *searchCourseMaterialService) SearchYoutubeVideo(query string, maxResults int) ([]model.YouTubeSearchResult, error) {

	// Reject empty search queries — the repository should never receive blank input.
	if query == "" {
		return nil, fmt.Errorf("search query cannot be empty")
	}

	// Enforce a minimum query length to avoid low-quality results.
	if len(query) < 2 {
		return nil, fmt.Errorf("search query must be at least 2 characters")
	}

	// Clamp maxResults to a sensible range. YouTube Data API v3 supports 1–50.
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	// Delegate the actual API call to the repository layer.
	results, err := s.repo.SearchYoutubeVideo(query, maxResults)
	if err != nil {
		return nil, fmt.Errorf("failed to search courses: %w", err)
	}

	// Return an empty slice (not nil) when no results are found, for consistent JSON output.
	if len(results) == 0 {
		return []model.YouTubeSearchResult{}, nil
	}

	return results, nil
}

// GetPlaylistItems validates the playlist id and delegates to the repository.
// Clamps maxResults to 1–50 (the YouTube API maximum per page).
func (s *searchCourseMaterialService) GetPlaylistItems(playlistID string, maxResults int) ([]model.YouTubeSearchResult, error) {
	if playlistID == "" {
		return nil, fmt.Errorf("playlist_id is required")
	}
	if maxResults <= 0 {
		maxResults = 25
	}
	if maxResults > 50 {
		maxResults = 50
	}
	results, err := s.repo.GetPlaylistItems(playlistID, maxResults)
	if err != nil {
		return nil, fmt.Errorf("failed to expand playlist: %w", err)
	}
	if len(results) == 0 {
		return []model.YouTubeSearchResult{}, nil
	}
	return results, nil
}

// LookupVideo validates the video id and delegates to the repository.
func (s *searchCourseMaterialService) LookupVideo(videoID string) (model.YouTubeSearchResult, error) {
	if videoID == "" {
		return model.YouTubeSearchResult{}, fmt.Errorf("youtube_id is required")
	}
	result, err := s.repo.LookupVideo(videoID)
	if err != nil {
		return model.YouTubeSearchResult{}, fmt.Errorf("failed to look up video: %w", err)
	}
	return result, nil
}
