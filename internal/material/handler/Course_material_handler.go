package handler

import (
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/services"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// searchCoursesResponse is the standard envelope returned by the search endpoint.
type searchCoursesResponse struct {
	Message    string                      `json:"message"`
	Query      string                      `json:"query"`
	TotalCount int                         `json:"total_count"`
	Results    []model.YouTubeSearchResult `json:"results"`
}

// errorResponse is a lightweight error envelope shared across search handlers.
type errorResponse struct {
	Error string `json:"error"`
}

// SearchCourseMaterialHandler handles HTTP requests for YouTube video search operations.
type SearchCourseMaterialHandler struct {
	service services.SearchCourseMaterialRepositoryService
}

// NewSearchCourseMaterialHandler creates a new handler with the given search service.
func NewSearchCourseMaterialHandler(service services.SearchCourseMaterialRepositoryService) *SearchCourseMaterialHandler {
	return &SearchCourseMaterialHandler{
		service: service,
	}
}

// SearchYoutubeVideo godoc
// @Summary      Search YouTube videos
// @Description  Search for YouTube videos by query keyword
// @Tags         Search
// @Produce      json
// @Param        query        query     string  true   "Search keyword"
// @Param        max_results  query     int     false  "Maximum number of results (default 10, max 50)"
// @Success      200          {object}  searchCoursesResponse
// @Failure      400          {object}  errorResponse
// @Failure      500          {object}  errorResponse
// @Router       /courses/search [get]
func (h *SearchCourseMaterialHandler) SearchYoutubeVideo(c *gin.Context) {

	// Extract and validate the required "query" parameter.
	query := c.Query("query")
	if query == "" {
		c.JSON(http.StatusBadRequest, errorResponse{
			Error: "query parameter is required",
		})
		return
	}

	// Parse the optional "max_results" parameter; default to 10 if omitted.
	maxResults := 10
	if maxStr := c.Query("max_results"); maxStr != "" {
		parsed, err := strconv.Atoi(maxStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{
				Error: "max_results must be a valid number",
			})
			return
		}
		maxResults = parsed
	}

	// Delegate to the service layer which applies business rules and calls the repository.
	results, err := h.service.SearchYoutubeVideo(query, maxResults)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{
			Error: err.Error(),
		})
		return
	}

	// Return the search results wrapped in a standard response envelope.
	c.JSON(http.StatusOK, searchCoursesResponse{
		Message:    "courses fetched successfully",
		Query:      query,
		TotalCount: len(results),
		Results:    results,
	})
}

// GetPlaylistItems godoc
// @Summary      Expand a YouTube playlist into its videos
// @Description  Given a YouTube playlist ID, returns up to max_results videos in the playlist. Use this for the playlist-paste flow when a learner provides a playlist URL.
// @Tags         Search
// @Produce      json
// @Param        playlist_id  query     string  true   "YouTube playlist ID (the value of the `list` query parameter on youtube.com)"
// @Param        max_results  query     int     false  "Maximum number of videos to return (default 25, max 50)"
// @Success      200          {object}  searchCoursesResponse
// @Failure      400          {object}  errorResponse
// @Failure      500          {object}  errorResponse
// @Router       /courses/playlist [get]
func (h *SearchCourseMaterialHandler) GetPlaylistItems(c *gin.Context) {
	playlistID := c.Query("playlist_id")
	if playlistID == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "playlist_id parameter is required"})
		return
	}

	maxResults := 25
	if maxStr := c.Query("max_results"); maxStr != "" {
		parsed, err := strconv.Atoi(maxStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "max_results must be a valid number"})
			return
		}
		maxResults = parsed
	}

	results, err := h.service.GetPlaylistItems(playlistID, maxResults)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, searchCoursesResponse{
		Message:    "playlist items fetched successfully",
		Query:      playlistID,
		TotalCount: len(results),
		Results:    results,
	})
}

// LookupVideo godoc
// @Summary      Look up a single YouTube video by ID
// @Description  Returns metadata for one video. Use this for the single-URL-paste flow — it costs 1 quota unit on the YouTube Data API (vs 100 for search).
// @Tags         Search
// @Produce      json
// @Param        youtube_id  query     string  true  "YouTube video ID (the value of the `v` query parameter on youtube.com)"
// @Success      200         {object}  map[string]interface{}
// @Failure      400         {object}  errorResponse
// @Failure      404         {object}  errorResponse
// @Router       /courses/lookup [get]
func (h *SearchCourseMaterialHandler) LookupVideo(c *gin.Context) {
	videoID := c.Query("youtube_id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "youtube_id parameter is required"})
		return
	}

	result, err := h.service.LookupVideo(videoID)
	if err != nil {
		// Distinguish not-found from other failures.
		status := http.StatusInternalServerError
		msg := err.Error()
		if contains(msg, "no video found") {
			status = http.StatusNotFound
		}
		c.JSON(status, errorResponse{Error: msg})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "video fetched successfully",
		"data":    result,
	})
}
