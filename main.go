package main

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"

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

	// Distroless images have no shell, so log the working directory at
	// startup: template/static paths are relative and this is the only
	// way to see what they resolve against.
	if wd, err := os.Getwd(); err == nil {
		log.Printf("Working directory: %s", wd)
	}

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
	// The SPA is served by a NoRoute fallback; Gin's default trailing-slash
	// redirect would 301-loop client-side routes (e.g. "/" -> "/"), so it
	// must stay off.
	r.RedirectTrailingSlash = false
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

	// Setup wizard (only reachable when no users exist; server-rendered).
	r.GET("/setup", a.showSetup)
	r.POST("/setup", a.handleSetup)

	// JSON API.
	a.registerAPIRoutes(r)

	// Embedded SPA for everything else.
	r.NoRoute(a.serveSPA())

	return r
}

//go:embed web/dist
var distFS embed.FS

// serveSPA serves the embedded frontend bundle, falling back to index.html
// for client-side routes. API paths get a JSON 404 instead.
func (a *App) serveSPA() gin.HandlerFunc {
	sub, err := fs.Sub(distFS, "web/dist")
	built := err == nil
	if built {
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			built = false
		}
	}
	if !built {
		log.Println("WARNING: web/dist is empty — run `npm run build` in web/ to build the frontend")
	}
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		if !built {
			c.String(http.StatusServiceUnavailable,
				"Frontend not built. Run `npm run build` in web/ and restart.")
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// Client-side route — let the SPA router handle it.
			path = "index.html"
		}
		// ServeContent, not http.FileServer: FileServer 301-redirects
		// index.html serves to "./", which breaks the SPA fallback.
		data, err := fs.ReadFile(sub, path)
		if err != nil {
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		info, err := fs.Stat(sub, path)
		if err != nil || info.IsDir() {
			jsonError(c, http.StatusNotFound, "not found")
			return
		}
		http.ServeContent(c.Writer, c.Request, path, info.ModTime(), bytes.NewReader(data))
	}
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

	admin := models.User{Username: username, Password: password, Role: models.RoleAdmin}
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

