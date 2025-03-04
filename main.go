package main

import (
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"church-groups/models"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/sessions"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	db          *gorm.DB
	store       = sessions.NewCookieStore([]byte("your-secret-key"))
	sessionName = "church-session"
)

func main() {
	var err error
	db, err = gorm.Open(sqlite.Open("db.sqlite3"), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	db.AutoMigrate(&models.User{}, &models.Group{})
	seedDatabase()

	// Set Gin to debug mode for detailed logs
	gin.SetMode(gin.DebugMode)
	r := gin.Default()
	r.SetFuncMap(template.FuncMap{
		"split": strings.Split,
	})
	r.LoadHTMLGlob("templates/*.html")
	r.Static("/static", "./static")

	// Public routes
	r.GET("/", listGroups)
	r.GET("/login", showLoginForm)
	r.POST("/login", login)
	r.GET("/logout", logout)

	// Protected routes
	auth := r.Group("/", authMiddleware())
	auth.GET("/create", showCreateForm)
	auth.POST("/create", createGroup)
	auth.GET("/group/:id", viewGroup)

	// Admin routes
	admin := r.Group("/admin", authMiddleware(), adminMiddleware())
	admin.GET("/", adminDashboard)
	admin.POST("/approve/:id", approveGroup)

	log.Println("Starting server on :8080")
	r.Run(":8080")
}

// Seed initial users with passwords
func seedDatabase() {
	var userCount int64
	db.Model(&models.User{}).Count(&userCount)
	if userCount == 0 {
		log.Println("Seeding database with initial users...")
		users := []models.User{
			{Username: "user1", Password: "password123", IsAdmin: false},
			{Username: "admin", Password: "admin123", IsAdmin: true},
		}
		for _, user := range users {
			if err := db.Create(&user).Error; err != nil {
				log.Printf("Failed to seed user %s: %v", user.Username, err)
			} else {
				log.Printf("Seeded user: %s", user.Username)
			}
		}
	} else {
		log.Printf("Database already has %d users, skipping seed", userCount)
	}
}

// Authentication middleware
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := store.Get(c.Request, sessionName)
		if err != nil {
			log.Printf("Session error: %v", err)
			c.Redirect(http.StatusSeeOther, "/login")
			c.Abort()
			return
		}
		userID, ok := session.Values["userID"]
		if !ok || userID == nil {
			log.Println("No userID in session, redirecting to login")
			c.Redirect(http.StatusSeeOther, "/login")
			c.Abort()
			return
		}
		c.Set("userID", userID.(uint))
		c.Next()
	}
}

// Admin middleware
func adminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uint)
		var user models.User
		if err := db.First(&user, userID).Error; err != nil || !user.IsAdmin {
			log.Printf("Admin check failed for userID %d: %v", userID, err)
			c.String(http.StatusForbidden, "Access denied")
			c.Abort()
			return
		}
		c.Next()
	}
}

// List groups
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
	if err := query.Find(&groups).Error; err != nil {
		log.Printf("Error fetching groups: %v", err)
	}

	session, _ := store.Get(c.Request, sessionName)
	c.HTML(http.StatusOK, "index.html", gin.H{
		"Groups":   groups,
		"LoggedIn": session.Values["userID"] != nil,
	})
}

// Show login form
func showLoginForm(c *gin.Context) {
	c.HTML(http.StatusOK, "login.html", nil)
}

// Handle login
func login(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")
	log.Printf("Login attempt: username=%s", username)

	var user models.User
	if err := db.Where("username = ?", username).First(&user).Error; err != nil {
		log.Printf("User not found: %s, error: %v", username, err)
		c.HTML(http.StatusUnauthorized, "login.html", gin.H{"Error": "Invalid credentials"})
		return
	}

	log.Printf("Found user: %s, checking password...", user.Username)
	if !user.CheckPassword(password) {
		log.Println("Password mismatch")
		c.HTML(http.StatusUnauthorized, "login.html", gin.H{"Error": "Invalid credentials"})
		return
	}

	session, err := store.Get(c.Request, sessionName)
	if err != nil {
		log.Printf("Session retrieval error: %v", err)
		c.String(http.StatusInternalServerError, "Session error")
		return
	}
	session.Values["userID"] = user.ID
	if err := session.Save(c.Request, c.Writer); err != nil {
		log.Printf("Failed to save session: %v", err)
		c.String(http.StatusInternalServerError, "Failed to save session")
		return
	}

	log.Printf("Login successful for user: %s", username)
	c.Redirect(http.StatusSeeOther, "/")
}

// Handle logout
func logout(c *gin.Context) {
	session, _ := store.Get(c.Request, sessionName)
	session.Values["userID"] = nil
	session.Save(c.Request, c.Writer)
	c.Redirect(http.StatusSeeOther, "/")
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
		log.Printf("Invalid date/time format: %v", err)
		c.String(http.StatusBadRequest, "Invalid date/time format")
		return
	}
	isRecurring, _ := strconv.ParseBool(c.PostForm("is_recurring"))

	userID := c.MustGet("userID").(uint)
	group := models.Group{
		Title:       c.PostForm("title"),
		DateTime:    dateTime,
		IsRecurring: isRecurring,
		Description: c.PostForm("description"),
		ImageURL:    c.PostForm("image_url"),
		CreatorID:   userID,
		Approved:    false,
	}

	if err := db.Create(&group).Error; err != nil {
		log.Printf("Failed to create group: %v", err)
		c.String(http.StatusInternalServerError, "Failed to create group")
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

// Admin dashboard
func adminDashboard(c *gin.Context) {
	var pendingGroups []models.Group
	if err := db.Where("approved = ?", false).Find(&pendingGroups).Error; err != nil {
		log.Printf("Error fetching pending groups: %v", err)
	}

	c.HTML(http.StatusOK, "admin.html", gin.H{
		"PendingGroups": pendingGroups,
	})
}

// Approve group
func approveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	tags := c.PostForm("tags")

	var group models.Group
	if err := db.First(&group, id).Error; err != nil {
		log.Printf("Group not found: %d, error: %v", id, err)
		c.String(http.StatusNotFound, "Group not found")
		return
	}

	group.Approved = true
	group.Tags = tags
	if err := db.Save(&group).Error; err != nil {
		log.Printf("Failed to approve group: %v", err)
		c.String(http.StatusInternalServerError, "Failed to approve group")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

// View group
func viewGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var group models.Group
	if err := db.First(&group, id).Error; err != nil || !group.Approved {
		log.Printf("Group not found or not approved: %d, error: %v", id, err)
		c.String(http.StatusNotFound, "Group not found")
		return
	}

	c.HTML(http.StatusOK, "group.html", gin.H{
		"Group": group,
	})
}
