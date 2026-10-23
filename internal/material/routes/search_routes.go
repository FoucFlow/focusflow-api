package routes

import (
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupSearchRoutes registers all YouTube search-related endpoints under /api/courses.
//
// Routes:
//   - GET /api/courses/search?query=<keyword>&max_results=<n>     →  Search YouTube videos
//   - GET /api/courses/playlist?playlist_id=<id>&max_results=<n>  →  Expand a playlist into videos
//   - GET /api/courses/lookup?youtube_id=<id>                     →  Single-video metadata lookup
func SetupSearchRoutes(api *gin.RouterGroup, searchHandler *handler.SearchCourseMaterialHandler) {
	courses := api.Group("/courses")
	{
		courses.GET("/search", searchHandler.SearchYoutubeVideo)
		courses.GET("/playlist", searchHandler.GetPlaylistItems)
		courses.GET("/lookup", searchHandler.LookupVideo)
	}
}
