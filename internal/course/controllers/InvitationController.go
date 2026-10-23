package controllers

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"strconv"
	"strings"

	"focusflow-api/internal/course/models"
	"focusflow-api/internal/platform/mailer"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Mail sends invitation emails; set in main.
var Mail mailer.Mailer

type InvitationController struct {
	DB *gorm.DB
}

// InviteUserByEmail godoc
// @Summary      Invite a user to a course via Email
// @Description  Sends an email with a course share link to the provided email address
// @Tags         invitations
// @Accept       json
// @Produce      json
// @Param        id   path      int  true  "Course ID"
// @Router       /api/course/{id}/invite-email [post]
func (ic *InvitationController) InviteUserByEmail(c *gin.Context) {
	courseID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid course ID"})
		return
	}

	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload: requires a valid email"})
		return
	}

	senderID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	uid := senderID.(int)

	// Fetch sender's name
	senderName, _ := c.Get("user_name")
	if senderName == nil {
		senderName = "A user"
	}

	// 1. Look up the invited user (same database now, no auth-service call)
	var receiverID int
	if err := ic.DB.Table("users").Select("id").Where("email = ?", strings.ToLower(strings.TrimSpace(req.Email))).Scan(&receiverID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error looking up user"})
		return
	}
	if receiverID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No user found with that email address"})
		return
	}
	if receiverID == uid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "You cannot invite yourself"})
		return
	}

	// Check if course exists and sender is owner
	var course models.Course
	if err := ic.DB.First(&course, courseID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Course not found"})
		return
	}
	if course.UserId != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only the course owner can invite users"})
		return
	}

	// Check if already invited and pending
	var count int64
	ic.DB.Model(&models.CourseInvitation{}).Where("course_id = ? AND receiver_id = ? AND status = ?", courseID, receiverID, "pending").Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "User already has a pending invitation for this course"})
		return
	}

	// Check if already enrolled
	ic.DB.Model(&models.Enrollment{}).Where("course_id = ? AND user_id = ?", courseID, receiverID).Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "User is already enrolled in this course"})
		return
	}

	invitation := models.CourseInvitation{
		CourseID:   courseID,
		SenderID:   uid,
		ReceiverID: receiverID,
		Status:     "pending",
	}

	if err := ic.DB.Create(&invitation).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create invitation: " + err.Error()})
		return
	}

	// Construct the share link
	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000" // Default for local dev
	}
	shareLink := fmt.Sprintf("%s/courses/detail?id=%d", frontendURL, courseID)

	// Send the invitation through the shared mailer (Zoho in production)
	body := fmt.Sprintf(`
		<html>
		<body>
			<h2>Course Invitation</h2>
			<p>Hello,</p>
			<p><strong>%s</strong> has invited you to join their course: <strong>%s</strong>.</p>
			<p>Click the link below to view the course and enroll:</p>
			<a href="%s" style="display:inline-block;padding:10px 20px;background-color:#4F46E5;color:#ffffff;text-decoration:none;border-radius:5px;">View Course</a>
			<p>Or copy this link into your browser: <br>%s</p>
			<br>
			<p>Happy Learning!<br>FocusFlow Team</p>
		</body>
		</html>
	`, html.EscapeString(fmt.Sprint(senderName)), html.EscapeString(course.CourseTitle), shareLink, html.EscapeString(shareLink))

	if err := Mail.Send(req.Email, "You have been invited to join a course on FocusFlow!", body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send email: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Invitation email sent successfully!"})
}

// GetMyNotifications godoc
// @Summary      Get user notifications/invitations
// @Description  Retrieves all pending invitations for the logged-in user
// @Tags         invitations
// @Produce      json
// @Router       /api/notifications [get]
func (ic *InvitationController) GetMyNotifications(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	uid := userID.(int)

	var invitations []models.CourseInvitation
	if err := ic.DB.Preload("Course").Where("receiver_id = ? AND status = ?", uid, "pending").Find(&invitations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, invitations)
}

// RespondToInvite godoc
// @Summary      Respond to an invitation
// @Description  Accept or decline a course invitation
// @Tags         invitations
// @Accept       json
// @Produce      json
// @Param        id   path      int  true  "Invitation ID"
// @Router       /api/invitations/{id}/respond [post]
func (ic *InvitationController) RespondToInvite(c *gin.Context) {
	inviteID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid invitation ID"})
		return
	}

	var req struct {
		Action string `json:"action" binding:"required"` // accept or decline
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload: requires action (accept or decline)"})
		return
	}
	if req.Action != "accept" && req.Action != "decline" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Action must be 'accept' or 'decline'"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	uid := userID.(int)

	var invite models.CourseInvitation
	if err := ic.DB.First(&invite, inviteID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Invitation not found"})
		return
	}

	if invite.ReceiverID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "You are not authorized to respond to this invitation"})
		return
	}
	if invite.Status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invitation has already been processed"})
		return
	}

	// Begin transaction
	tx := ic.DB.Begin()

	if req.Action == "accept" {
		invite.Status = "accepted"
		if err := tx.Save(&invite).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update invitation"})
			return
		}

		// Create enrollment
		enrollment := models.Enrollment{
			CourseID: invite.CourseID,
			UserID:   uid,
		}
		if err := tx.Create(&enrollment).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create enrollment"})
			return
		}
	} else if req.Action == "decline" {
		invite.Status = "declined"
		if err := tx.Save(&invite).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update invitation"})
			return
		}
	}

	tx.Commit()
	c.JSON(http.StatusOK, gin.H{"message": "Successfully " + req.Action + "ed invitation"})
}
