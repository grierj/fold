package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"text/template"
	"time"

	"church-groups/models"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var db *gorm.DB

func main() {
	// Initialize SQLite database
	var err error
	db, err = gorm.Open(sqlite.Open("db.sqlite3"), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	// Auto-migrate schema
	db.AutoMigrate(&models.User{}, &models.Group{})

	// Seed initial data
	seedDatabase()

	r := gin.Default()

	// Load templates
	r.SetFuncMap(template.FuncMap{
		"split": strings.Split,
	})
	r.LoadHTMLGlob("templates/*.html")
	r.LoadHTMLGlob("templates/*.html")
	r.Static("/static", "./static")

	// Routes
	r.GET("/", listGroups)
	r.GET("/create", showCreateForm)
	r.POST("/create", createGroup)
	r.GET("/admin", adminDashboard)
	r.POST("/admin/approve/:id", approveGroup)
	r.GET("/group/:id", viewGroup)

	r.Run(":8080")
}

// Seed initial users
func seedDatabase() {
	var userCount int64
	db.Model(&models.User{}).Count(&userCount)
	if userCount == 0 {
		db.Create(&models.User{Username: "user1", IsAdmin: false})
		db.Create(&models.User{Username: "admin", IsAdmin: true})
	}
}

// List all approved groups with search and filter
func listGroups(c *gin.Context) {
	search := c.Query("search")
	tag := c.Query("tag")

	var groups []models.Group
	query := db.Where("approved = ?", true)

	if search != "" {
		search = "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(title) LIKE ? OR LOWER(description) LIKE ?", search, search)
	}
	if tag != "" {
		query = query.Where("tags LIKE ?", "%"+tag+"%")
	}

	query.Find(&groups)
	c.HTML(http.StatusOK, "index.html", gin.H{
		"Groups": groups,
	})
}

// Show group creation form
func showCreateForm(c *gin.Context) {
	c.HTML(http.StatusOK, "create.html", nil)
}

// Handle group creation
func createGroup(c *gin.Context) {
	dateTimeStr := c.PostForm("datetime")
	dateTime, err := time.Parse("2006-01-02T15:04", dateTimeStr)
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid date/time format")
		return
	}
	isRecurring, _ := strconv.ParseBool(c.PostForm("is_recurring"))

	group := models.Group{
		Title:       c.PostForm("title"),
		DateTime:    dateTime,
		IsRecurring: isRecurring,
		Description: c.PostForm("description"),
		ImageURL:    c.PostForm("image_url"),
		CreatorID:   1, // Hardcoded user for simplicity
		Approved:    false,
	}

	if err := db.Create(&group).Error; err != nil {
		c.String(http.StatusInternalServerError, "Failed to create group")
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

// Admin dashboard
func adminDashboard(c *gin.Context) {
	userID := uint(2) // Hardcoded admin ID for simplicity
	var user models.User
	if err := db.First(&user, userID).Error; err != nil || !user.IsAdmin {
		c.String(http.StatusForbidden, "Access denied")
		return
	}

	var pendingGroups []models.Group
	db.Where("approved = ?", false).Find(&pendingGroups)

	c.HTML(http.StatusOK, "admin.html", gin.H{
		"PendingGroups": pendingGroups,
	})
}

// Approve group and add tags
func approveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	tags := c.PostForm("tags")

	var group models.Group
	if err := db.First(&group, id).Error; err != nil {
		c.String(http.StatusNotFound, "Group not found")
		return
	}

	group.Approved = true
	group.Tags = tags
	if err := db.Save(&group).Error; err != nil {
		c.String(http.StatusInternalServerError, "Failed to approve group")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

// View individual group
func viewGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var group models.Group
	if err := db.First(&group, id).Error; err != nil || !group.Approved {
		c.String(http.StatusNotFound, "Group not found")
		return
	}

	c.HTML(http.StatusOK, "group.html", gin.H{
		"Group": group,
	})
}
