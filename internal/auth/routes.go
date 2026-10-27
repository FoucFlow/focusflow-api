package auth

import "github.com/gin-gonic/gin"

// Routes keeps the paths the Laravel service used, so the Nuxt proxies only
// change their base URL.
func (h *Handler) Routes(r *gin.Engine) {
	r.POST("/register", h.Register)
	r.POST("/login", h.Login)
	r.POST("/logout", h.Logout)
	r.POST("/forgot-password", h.ForgotPassword)
	r.POST("/reset-password", h.ResetPassword)
	r.GET("/verify-email/:id/:hash", h.VerifyEmail)
	r.POST("/email/verification-notification", AuthMiddleware(), h.ResendVerification)

	r.GET("/api/v1/auth/google/redirect", h.GoogleRedirect)
	r.GET("/api/v1/auth/google/callback", h.GoogleCallback)

	user := r.Group("/api/user", AuthMiddleware())
	user.GET("", h.Me)
	user.GET("/lookup", h.Lookup)
	user.GET("/:id", h.Show)
	user.PUT("/:id", h.Update)
	user.POST("/:id/avatar", h.UploadAvatar)
	user.PUT("/:id/password", h.ChangePassword)

	admin := r.Group("/api/admin", AuthMiddleware(), RequireRole(RoleAdmin))
	admin.GET("/users", h.AdminListUsers)
	admin.GET("/users/:id", h.AdminShowUser)
	admin.PUT("/users/:id/role", h.AdminUpdateRole)
	admin.PUT("/users/:id/plan", h.AdminUpdatePlan)
	admin.GET("/stats", h.AdminStats)
	admin.GET("/analytics/overview", h.AnalyticsOverview)
	admin.GET("/analytics/users/growth", h.AnalyticsUserGrowth)
	admin.GET("/analytics/users/activity", h.AnalyticsUserActivity)
	admin.GET("/analytics/users/roles", h.AnalyticsRoles)
	admin.GET("/analytics/reports/export", h.AnalyticsExport)
}
