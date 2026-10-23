package controllers

import (
	"focusflow-api/internal/platform/storage"
	"path/filepath"

	"focusflow-api/internal/course/models"
	"github.com/google/uuid"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type CourseService struct {
	DB *gorm.DB
}

// CreateCourse godoc
// @Summary      Create a new course
// @Description  Takes course details in JSON and saves them to the PostgreSQL database
// @Tags         courses
// @Accept       json
// @Produce      json
// @Param        course  body      models.Course  true  "Course Data"
// @Success      201     {object}  models.Course
// @Failure      400     {object}  map[string]string "Invalid JSON input"
// @Failure      500     {object}  map[string]string "Database error"
// @Router       /api/course [post]

func (cc *CourseService) CreateCourse(c *gin.Context) {

	// 1. Thumbnail is optional. When a file is supplied we upload it to S3;
	// otherwise the course is created with an empty thumbnail and the
	// frontend falls back to the first lesson's YouTube thumbnail.
	var fileURL string
	file, err := c.FormFile("course_thumb")
	if err == nil {
		// 2. Open the file to read its contents
		openedFile, err := file.Open()
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to open uploaded file"})
			return
		}
		defer openedFile.Close()

		// Read the file into a byte buffer
		buffer := make([]byte, file.Size)
		_, err = openedFile.Read(buffer)
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to read file buffer"})
			return
		}

		// 3. Upload to AWS S3 using our new utility
		fileURL, err = storage.Upload(c.Request.Context(), "thumbnails", uuid.NewString()+filepath.Ext(file.Filename), buffer, file.Header.Get("Content-Type"))
		if err != nil {
			c.JSON(500, gin.H{"error": "Thumbnail upload failed: " + err.Error()})
			return
		}
	}

	// 4. Manually parse form fields
	// (ShouldBindJSON does not work with multipart/form-data)
	categoryID, _ := strconv.Atoi(c.PostForm("course_category_id"))
	progress, _ := strconv.Atoi(c.PostForm("course_progress"))

	// 4.5. The course owner is ALWAYS the authenticated user — never trust the
	// client-supplied user_id form field (it would let anyone create courses
	// "owned by" arbitrary users). AuthMiddleware sets user_id from the session.
	creatorID, exists := c.Get("user_id")
	if !exists {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	userID, ok := creatorID.(int)
	if !ok {
		c.JSON(500, gin.H{"error": "Invalid user session"})
		return
	}

	// If no file was uploaded, accept a thumbnail URL passed as a plain form
	// field (still optional — may be empty).
	if fileURL == "" {
		fileURL = c.PostForm("course_thumb")
	}

	newCourse := models.Course{
		CourseTitle:       c.PostForm("course_title"),
		CourseThumb:       fileURL, // S3 URL, passed-through URL, or empty
		CourseCategoryId:  categoryID,
		UserId:            userID,
		CourseVisibility:  c.PostForm("course_visibility"),
		CourseDescription: c.PostForm("course_description"),
		CourseProgress:    progress,
	}

	// 5. Save to PostgreSQL (Supabase)

	result := cc.DB.Create(&newCourse)
	if result.Error != nil {
		c.JSON(500, gin.H{"error": "Database error: " + result.Error.Error()})
		return
	}

	// 6. Auto-enroll the creator in their own course
	enrollment := models.Enrollment{
		CourseID: *newCourse.CourseId,
		UserID:   userID,
	}
	cc.DB.Create(&enrollment)

	c.JSON(201, newCourse)

}

// CreateCourse godoc
// @Summary      Get all Courses
// @Description  Retrive all the courses from the database and sends them as json response to the user
// @Tags         courses
// @Produce      json
// @Param            models.Course  true  "Course Data"
// @Success      201     {object}  models.Course
// @Failure      400     {object}  map[string]string "Invalid JSON input"
// @Failure      500     {object}  map[string]string "Database error"
// @Router       /api/course [get]

func (cc *CourseService) GetCourses(c *gin.Context) {
	var allCourses []models.Course
	batchSize := 100

	// GORM automatically appends results to &allCourses
	err := cc.DB.Model(&models.Course{}).
		Preload("Category").
		FindInBatches(&allCourses, batchSize, func(tx *gorm.DB, batch int) error {
			// You don't need to do anything here unless you want to
			// process each batch (like logging or calculations).
			return nil
		}).Error

	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to fetch data"})
		return
	}

	c.JSON(200, allCourses)
}

// GetCourseByID godoc
// @Summary      Get a single course
// @Description  Retrieves one course (with its category) by its course_id
// @Tags         courses
// @Produce      json
// @Param        id   path      int  true  "Course ID"
// @Success      200  {object}  models.Course
// @Failure      404  {object}  map[string]string "Course not found"
// @Router       /api/course/{id} [get]
func (cc *CourseService) GetCourseByID(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "Invalid course id"})
		return
	}

	var course models.Course
	result := cc.DB.Preload("Category").First(&course, id)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			c.JSON(404, gin.H{"error": "Course not found"})
			return
		}
		c.JSON(500, gin.H{"error": "Database error: " + result.Error.Error()})
		return
	}

	c.JSON(200, course)
}

// UpdateCourse godoc
// @Summary      Update a course
// @Description  Partially updates a course. Accepts multipart/form-data so a
//
//	new thumbnail can optionally be uploaded. Only the fields
//	present in the request are changed (e.g. course_visibility
//	alone to toggle public/private).
//
// @Tags         courses
// @Accept       mpfd
// @Produce      json
// @Param        id   path      int  true  "Course ID"
// @Success      200  {object}  models.Course
// @Failure      404  {object}  map[string]string "Course not found"
// @Router       /api/course/{id} [put]
func (cc *CourseService) UpdateCourse(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "Invalid course id"})
		return
	}

	// 1. Load the existing course (also confirms it exists).
	var course models.Course
	if result := cc.DB.First(&course, id); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			c.JSON(404, gin.H{"error": "Course not found"})
			return
		}
		c.JSON(500, gin.H{"error": "Database error: " + result.Error.Error()})
		return
	}

	// 1.5. Check ownership (admin is exempt if you want, but for now we enforce owner)
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	if course.UserId != userID.(int) {
		c.JSON(403, gin.H{"error": "Forbidden: You are not the owner of this course"})
		return
	}

	// 2. If a new thumbnail file was uploaded, push it to S3 and replace the URL.
	if file, err := c.FormFile("course_thumb"); err == nil {
		openedFile, err := file.Open()
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to open uploaded file"})
			return
		}
		defer openedFile.Close()

		buffer := make([]byte, file.Size)
		if _, err := openedFile.Read(buffer); err != nil {
			c.JSON(500, gin.H{"error": "Failed to read file buffer"})
			return
		}

		fileURL, err := storage.Upload(c.Request.Context(), "thumbnails", uuid.NewString()+filepath.Ext(file.Filename), buffer, file.Header.Get("Content-Type"))
		if err != nil {
			c.JSON(500, gin.H{"error": "Thumbnail upload failed: " + err.Error()})
			return
		}
		course.CourseThumb = fileURL
	} else if v, ok := c.GetPostForm("course_thumb"); ok {
		// Thumbnail passed as a plain URL (or cleared with an empty value).
		course.CourseThumb = v
	}

	// 3. Apply any text fields that were included in the request. Missing
	//    fields are left untouched so callers can send partial updates.
	if v, ok := c.GetPostForm("course_title"); ok {
		course.CourseTitle = v
	}
	if v, ok := c.GetPostForm("course_description"); ok {
		course.CourseDescription = v
	}
	if v, ok := c.GetPostForm("course_visibility"); ok {
		course.CourseVisibility = v
	}
	if v, ok := c.GetPostForm("course_category_id"); ok {
		if categoryID, err := strconv.Atoi(v); err == nil {
			course.CourseCategoryId = categoryID
		}
	}
	if v, ok := c.GetPostForm("course_progress"); ok {
		if progress, err := strconv.Atoi(v); err == nil {
			course.CourseProgress = progress
		}
	}

	// 4. Persist and return the updated record (with category).
	if result := cc.DB.Save(&course); result.Error != nil {
		c.JSON(500, gin.H{"error": "Database error: " + result.Error.Error()})
		return
	}
	cc.DB.Preload("Category").First(&course, id)

	c.JSON(200, course)
}

// DeleteCourse godoc
// @Summary      Delete a course
// @Description  Permanently removes a course by its course_id
// @Tags         courses
// @Produce      json
// @Param        id   path      int  true  "Course ID"
// @Success      200  {object}  map[string]string "Deleted"
// @Failure      404  {object}  map[string]string "Course not found"
// @Router       /api/course/{id} [delete]
func (cc *CourseService) DeleteCourse(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "Invalid course id"})
		return
	}

	// 1. Fetch the course to check ownership
	var course models.Course
	if result := cc.DB.First(&course, id); result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			c.JSON(404, gin.H{"error": "Course not found"})
			return
		}
		c.JSON(500, gin.H{"error": "Database error: " + result.Error.Error()})
		return
	}

	// 2. Check ownership
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	if course.UserId != userID.(int) {
		c.JSON(403, gin.H{"error": "Forbidden: You are not the owner of this course"})
		return
	}

	// 3. Perform delete
	result := cc.DB.Delete(&models.Course{}, id)
	if result.Error != nil {
		c.JSON(500, gin.H{"error": "Database error: " + result.Error.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "Course deleted successfully"})
}
