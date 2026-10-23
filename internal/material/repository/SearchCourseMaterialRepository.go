package repository

import (
	"encoding/json"
	"fmt"
	"focusflow-api/internal/material/model"
	"net/http"
	"net/url"
)

// SearchCourseMaterialRepository defines the contract for searching external video sources.
// This abstraction allows swapping YouTube for another provider without changing the service layer.
type SearchCourseMaterialRepository interface {
	// SearchYoutubeVideo queries the YouTube Data API and returns matching video results.
	SearchYoutubeVideo(query string, maxResults int) ([]model.YouTubeSearchResult, error)

	// GetPlaylistItems expands a YouTube playlist into its constituent video items.
	// Used by the playlist-paste flow when a learner provides a playlist URL.
	GetPlaylistItems(playlistID string, maxResults int) ([]model.YouTubeSearchResult, error)

	// LookupVideo retrieves metadata for a single YouTube video by ID.
	// Used by the single-URL-paste flow. Costs 1 quota unit on the YouTube
	// Data API (vs 100 for search), so this is the preferred path when the
	// learner already knows the exact video they want.
	LookupVideo(videoID string) (model.YouTubeSearchResult, error)
}

// searchCourseMaterialRepository is the YouTube-backed implementation of SearchCourseMaterialRepository.
type searchCourseMaterialRepository struct {
	apiKey     string       // YouTube Data API v3 key
	httpClient *http.Client // reusable HTTP client for outbound requests
}

// NewSearchCourseMaterialRepository creates a repository configured with the given YouTube API key.
func NewSearchCourseMaterialRepository(apiKey string) SearchCourseMaterialRepository {
	return &searchCourseMaterialRepository{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// youtubeResponse is a private struct that mirrors the relevant fields of the YouTube Data API v3
// search response. It is used only for JSON deserialization within this package.
type youtubeResponse struct {
	Items []struct {
		ID struct {
			VideoID string `json:"videoId"`
		} `json:"id"`
		Snippet struct {
			Title        string `json:"title"`
			Description  string `json:"description"`
			ChannelTitle string `json:"channelTitle"`
			Thumbnails   struct {
				Medium struct {
					URL string `json:"url"`
				} `json:"medium"`
			} `json:"thumbnails"`
		} `json:"snippet"`
	} `json:"items"`
}

// SearchYoutubeVideo queries the YouTube Data API v3 for videos matching the given query.
// It builds the request URL, performs the HTTP call, and maps the response into domain models.
func (r *searchCourseMaterialRepository) SearchYoutubeVideo(query string, maxResults int) ([]model.YouTubeSearchResult, error) {

	// Build the YouTube Data API search URL with query parameters.
	baseURL := "https://www.googleapis.com/youtube/v3/search"
	params := url.Values{}
	params.Set("part", "snippet")
	params.Set("q", query)
	params.Set("type", "video") // restrict to video results only
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))
	params.Set("key", r.apiKey)

	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	// Execute the HTTP GET request against the YouTube API.
	resp, err := r.httpClient.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("failed to call YouTube API: %w", err)
	}
	defer resp.Body.Close()

	// Verify the API returned a successful response.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube API returned status: %d", resp.StatusCode)
	}

	// Deserialize the JSON response body into the internal YouTube response struct.
	var ytResponse youtubeResponse
	if err := json.NewDecoder(resp.Body).Decode(&ytResponse); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Map YouTube API items into our domain model. We populate the JSON-exposed
	// public fields (YouTubeID, ThumbnailURL) — the older Thumbnail/VideoID
	// fields are kept on the struct for backward compatibility but excluded
	// from JSON via `json:"-"`.
	var results []model.YouTubeSearchResult
	for _, item := range ytResponse.Items {
		results = append(results, model.YouTubeSearchResult{
			YouTubeID:    item.ID.VideoID,
			Title:        item.Snippet.Title,
			Description:  item.Snippet.Description,
			ThumbnailURL: item.Snippet.Thumbnails.Medium.URL,
			ChannelName:  item.Snippet.ChannelTitle,
		})
	}

	return results, nil
}

// playlistItemsResponse mirrors the relevant fields of the YouTube Data API
// v3 playlistItems response. Only the medium thumbnail and the resource
// videoId are used.
type playlistItemsResponse struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		Snippet struct {
			Title        string `json:"title"`
			Description  string `json:"description"`
			ChannelTitle string `json:"channelTitle"`
			Position     int    `json:"position"`
			ResourceID   struct {
				VideoID string `json:"videoId"`
			} `json:"resourceId"`
			Thumbnails struct {
				Medium struct {
					URL string `json:"url"`
				} `json:"medium"`
			} `json:"thumbnails"`
		} `json:"snippet"`
	} `json:"items"`
}

// GetPlaylistItems expands a YouTube playlist into video results. Pages
// through the API up to a single page of `maxResults` items (clamped at 50
// by the YouTube API). For longer playlists we'd loop on `nextPageToken`,
// but the UI caps adds at 50 lessons per playlist so a single page is enough.
func (r *searchCourseMaterialRepository) GetPlaylistItems(playlistID string, maxResults int) ([]model.YouTubeSearchResult, error) {
	baseURL := "https://www.googleapis.com/youtube/v3/playlistItems"
	params := url.Values{}
	params.Set("part", "snippet")
	params.Set("playlistId", playlistID)
	params.Set("maxResults", fmt.Sprintf("%d", maxResults))
	params.Set("key", r.apiKey)

	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	resp, err := r.httpClient.Get(fullURL)
	if err != nil {
		return nil, fmt.Errorf("failed to call YouTube playlistItems API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("youtube playlistItems API returned status: %d", resp.StatusCode)
	}

	var pl playlistItemsResponse
	if err := json.NewDecoder(resp.Body).Decode(&pl); err != nil {
		return nil, fmt.Errorf("failed to decode playlistItems response: %w", err)
	}

	var results []model.YouTubeSearchResult
	for _, item := range pl.Items {
		if item.Snippet.ResourceID.VideoID == "" {
			continue // skip non-video items (e.g. deleted videos)
		}
		results = append(results, model.YouTubeSearchResult{
			YouTubeID:    item.Snippet.ResourceID.VideoID,
			Title:        item.Snippet.Title,
			Description:  item.Snippet.Description,
			ThumbnailURL: item.Snippet.Thumbnails.Medium.URL,
			ChannelName:  item.Snippet.ChannelTitle,
		})
	}
	return results, nil
}

// videosResponse mirrors the YouTube Data API v3 `videos` endpoint response.
type videosResponse struct {
	Items []struct {
		ID      string `json:"id"`
		Snippet struct {
			Title        string `json:"title"`
			Description  string `json:"description"`
			ChannelTitle string `json:"channelTitle"`
			Thumbnails   struct {
				Medium struct {
					URL string `json:"url"`
				} `json:"medium"`
			} `json:"thumbnails"`
		} `json:"snippet"`
	} `json:"items"`
}

// LookupVideo returns metadata for a single video by its YouTube ID.
// Returns an error if the ID is unknown or has been deleted.
func (r *searchCourseMaterialRepository) LookupVideo(videoID string) (model.YouTubeSearchResult, error) {
	baseURL := "https://www.googleapis.com/youtube/v3/videos"
	params := url.Values{}
	params.Set("part", "snippet")
	params.Set("id", videoID)
	params.Set("key", r.apiKey)

	fullURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())

	resp, err := r.httpClient.Get(fullURL)
	if err != nil {
		return model.YouTubeSearchResult{}, fmt.Errorf("failed to call YouTube videos API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return model.YouTubeSearchResult{}, fmt.Errorf("youtube videos API returned status: %d", resp.StatusCode)
	}

	var vr videosResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return model.YouTubeSearchResult{}, fmt.Errorf("failed to decode videos response: %w", err)
	}
	if len(vr.Items) == 0 {
		return model.YouTubeSearchResult{}, fmt.Errorf("no video found for id %s", videoID)
	}
	item := vr.Items[0]
	return model.YouTubeSearchResult{
		YouTubeID:    item.ID,
		Title:        item.Snippet.Title,
		Description:  item.Snippet.Description,
		ThumbnailURL: item.Snippet.Thumbnails.Medium.URL,
		ChannelName:  item.Snippet.ChannelTitle,
	}, nil
}
