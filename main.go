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
	"gorm.io/gorm"
)

// App wires together config, database, and session store.
type App struct {
	DB     *gorm.DB
	Store  *sessions.CookieStore
	Config Config
}

const sessionName = "church-session"

func main() {
	cfg := LoadConfig()

	if cfg.IsDev() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	db := openDatabase(cfg)

	store := sessions.NewCookieStore(cfg.SessionSecret)
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}

	app := &App{DB: db, Store: store, Config: cfg}
	r := app.setupRouter()

	log.Printf("Starting server on :%s (env=%s)", cfg.Port, cfg.Env)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func (a *App) setupRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	// Don't trust X-Forwarded-For by default; set explicit proxies if needed.
	_ = r.SetTrustedProxies(nil)

	r.SetFuncMap(template.FuncMap{
		"split": strings.Split,
	})
	r.LoadHTMLGlob("templates/*.html")
	r.Static("/static", "./static")

	// First-run setup wizard takes precedence over everything else.
	r.Use(a.setupRequired())
	r.Use(a.csrfMiddleware())

	// Setup wizard (only reachable when no users exist).
	r.GET("/setup", a.showSetup)
	r.POST("/setup", a.handleSetup)

	// Public routes.
	r.GET("/", a.listGroups)
	r.GET("/login", a.showLoginForm)
	r.POST("/login", a.login)
	r.GET("/logout", a.logout)

	// Protected routes.
	auth := r.Group("/", a.authMiddleware())
	auth.GET("/create", a.showCreateForm)
	auth.POST("/create", a.createGroup)
	auth.GET("/group/:id", a.viewGroup)

	// Admin routes.
	admin := r.Group("/admin", a.authMiddleware(), a.adminMiddleware())
	admin.GET("/", a.adminDashboard)
	admin.POST("/approve/:id", a.approveGroup)

	return r
}

// html renders a template, injecting the CSRF field and site name.
func (a *App) html(c *gin.Context, code int, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["csrfField"] = csrfField(c.GetString("csrfToken"))
	data["SiteName"] = a.siteName()
	c.HTML(code, name, data)
}

func (a *App) siteName() string {
	var s models.Setting
	if err := a.DB.Where("key = ?", "site_name").First(&s).Error; err != nil || s.Value == "" {
		return "Church Groups"
	}
	return s.Value
}

// setupRequired redirects everything to the setup wizard until an admin
// account exists. This replaces the old hardcoded seed users.
func (a *App) setupRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if p == "/setup" || strings.HasPrefix(p, "/static/") {
			c.Next()
			return
		}
		var n int64
		if err := a.DB.Model(&models.User{}).Count(&n).Error; err != nil {
			c.String(http.StatusInternalServerError, "Database error")
			c.Abort()
			return
		}
		if n == 0 {
			c.Redirect(http.StatusSeeOther, "/setup")
			c.Abort()
			return
		}
		c.Next()
	}
}

// Authentication middleware
func (a *App) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := a.Store.Get(c.Request, sessionName)
		if err != nil {
			c.Redirect(http.StatusSeeOther, "/login")
			c.Abort()
			return
		}
		userID, ok := session.Values["userID"]
		if !ok || userID == nil {
			c.Redirect(http.StatusSeeOther, "/login")
			c.Abort()
			return
		}
		uid, ok := userID.(uint)
		if !ok {
			c.Redirect(http.StatusSeeOther, "/login")
			c.Abort()
			return
		}
		c.Set("userID", uid)
		c.Next()
	}
}

// Admin middleware
func (a *App) adminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uint)
		var user models.User
		if err := a.DB.First(&user, userID).Error; err != nil || !user.IsAdmin {
			c.String(http.StatusForbidden, "Access denied")
			c.Abort()
			return
		}
		c.Next()
	}
}

// --- Setup wizard ---

func (a *App) showSetup(c *gin.Context) {
	var n int64
	a.DB.Model(&models.User{}).Count(&n)
	if n > 0 {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}
	a.html(c, http.StatusOK, "setup.html", nil)
}

func (a *App) handleSetup(c *gin.Context) {
	var n int64
	a.DB.Model(&models.User{}).Count(&n)
	if n > 0 {
		c.Redirect(http.StatusSeeOther, "/")
		return
	}

	siteName := strings.TrimSpace(c.PostForm("site_name"))
	username := strings.TrimSpace(c.PostForm("username"))
	password := c.PostForm("password")
	confirm := c.PostForm("password_confirm")

	fail := func(msg string) {
		a.html(c, http.StatusBadRequest, "setup.html", gin.H{"Error": msg})
	}

	if siteName == "" || username == "" || password == "" {
		fail("All fields are required.")
		return
	}
	if len(password) < 10 {
		fail("Password must be at least 10 characters.")
		return
	}
	if password != confirm {
		fail("Passwords do not match.")
		return
	}

	admin := models.User{Username: username, Password: password, IsAdmin: true}
	if err := a.DB.Create(&admin).Error; err != nil {
		log.Printf("Setup failed creating admin: %v", err)
		fail("Could not create admin account (username may be taken).")
		return
	}
	a.DB.Where(models.Setting{Key: "site_name"}).
		Assign(models.Setting{Value: siteName}).
		FirstOrCreate(&models.Setting{})

	log.Printf("Setup complete: site=%q admin=%q", siteName, username)
	c.Redirect(http.StatusSeeOther, "/login")
}

// --- Handlers ---

// List groups
func (a *App) listGroups(c *gin.Context) {
	search := c.Query("search")
	tag := c.Query("tag")

	var groups []models.Group
	query := a.DB.Where("approved = ?", true)
	if search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where("LOWER(title) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if tag != "" {
		query = query.Where("tags LIKE ?", "%"+tag+"%")
	}
	if err := query.Find(&groups).Error; err != nil {
		log.Printf("Error fetching groups: %v", err)
	}

	session, _ := a.Store.Get(c.Request, sessionName)
	a.html(c, http.StatusOK, "index.html", gin.H{
		"Groups":   groups,
		"LoggedIn": session.Values["userID"] != nil,
	})
}

// Show login form
func (a *App) showLoginForm(c *gin.Context) {
	a.html(c, http.StatusOK, "login.html", nil)
}

// Handle login
func (a *App) login(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")

	var user models.User
	if err := a.DB.Where("username = ?", username).First(&user).Error; err != nil {
		a.html(c, http.StatusUnauthorized, "login.html", gin.H{"Error": "Invalid credentials"})
		return
	}
	if !user.CheckPassword(password) {
		a.html(c, http.StatusUnauthorized, "login.html", gin.H{"Error": "Invalid credentials"})
		return
	}

	session, err := a.Store.Get(c.Request, sessionName)
	if err != nil {
		c.String(http.StatusInternalServerError, "Session error")
		return
	}
	session.Values["userID"] = user.ID
	if err := session.Save(c.Request, c.Writer); err != nil {
		c.String(http.StatusInternalServerError, "Failed to save session")
		return
	}

	c.Redirect(http.StatusSeeOther, "/")
}

// Handle logout
func (a *App) logout(c *gin.Context) {
	session, _ := a.Store.Get(c.Request, sessionName)
	session.Values["userID"] = nil
	_ = session.Save(c.Request, c.Writer)
	c.Redirect(http.StatusSeeOther, "/")
}

// Show group creation form
func (a *App) showCreateForm(c *gin.Context) {
	a.html(c, http.StatusOK, "create.html", nil)
}

// Handle group creation
func (a *App) createGroup(c *gin.Context) {
	dateTime, err := time.Parse("2006-01-02T15:04", c.PostForm("datetime"))
	if err != nil {
		c.String(http.StatusBadRequest, "Invalid date/time format")
		return
	}
	isRecurring, _ := strconv.ParseBool(c.PostForm("is_recurring"))

	userID := c.MustGet("userID").(uint)
	group := models.Group{
		Title:       strings.TrimSpace(c.PostForm("title")),
		DateTime:    dateTime,
		IsRecurring: isRecurring,
		Description: strings.TrimSpace(c.PostForm("description")),
		ImageURL:    strings.TrimSpace(c.PostForm("image_url")),
		CreatorID:   userID,
		Approved:    false,
	}
	if group.Title == "" || group.Description == "" {
		c.String(http.StatusBadRequest, "Title and description are required")
		return
	}

	if err := a.DB.Create(&group).Error; err != nil {
		log.Printf("Failed to create group: %v", err)
		c.String(http.StatusInternalServerError, "Failed to create group")
		return
	}
	c.Redirect(http.StatusSeeOther, "/")
}

// Admin dashboard
func (a *App) adminDashboard(c *gin.Context) {
	var pendingGroups []models.Group
	if err := a.DB.Where("approved = ?", false).Find(&pendingGroups).Error; err != nil {
		log.Printf("Error fetching pending groups: %v", err)
	}

	a.html(c, http.StatusOK, "admin.html", gin.H{
		"PendingGroups": pendingGroups,
	})
}

// Approve group
func (a *App) approveGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	tags := c.PostForm("tags")

	var group models.Group
	if err := a.DB.First(&group, id).Error; err != nil {
		c.String(http.StatusNotFound, "Group not found")
		return
	}

	group.Approved = true
	group.Tags = tags
	if err := a.DB.Save(&group).Error; err != nil {
		log.Printf("Failed to approve group: %v", err)
		c.String(http.StatusInternalServerError, "Failed to approve group")
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin")
}

// View group
func (a *App) viewGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var group models.Group
	if err := a.DB.First(&group, id).Error; err != nil || !group.Approved {
		c.String(http.StatusNotFound, "Group not found")
		return
	}

	a.html(c, http.StatusOK, "group.html", gin.H{
		"Group": group,
	})
}
