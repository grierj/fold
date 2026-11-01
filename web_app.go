package main

import (
	"net/http"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// The browser side of web push (M3.13c1, research 2.3): the service worker
// is a file in the built bundle (web/src/sw.ts, built to dist/sw.js and
// served by serveSPA with no-cache), and the manifest is made here so the
// Home Screen app carries the church's name (DEC-124).

// serviceWorkerPath is the worker's file in web/dist. Served from the root,
// its scope is the whole site.
const serviceWorkerPath = "sw.js"

// Manifest colors are the page and primary tokens in web/src/index.css
// (--color-page, --color-primary). The manifest can't read CSS variables.
const (
	manifestBackground = "#faf8f5" // color-ok: --color-page (stone-50) for the manifest
	manifestTheme      = "#7f47c7" // color-ok: --color-primary (violet-600) for the manifest
)

// manifestShortNameMax: a longer name is cut off under a Home Screen icon.
const manifestShortNameMax = 12

type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

type webManifest struct {
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	StartURL        string         `json:"start_url"`
	Scope           string         `json:"scope"`
	Display         string         `json:"display"`
	BackgroundColor string         `json:"background_color"`
	ThemeColor      string         `json:"theme_color"`
	Icons           []manifestIcon `json:"icons"`
}

// manifestFor is the manifest for a church name. start_url's source lets
// the SPA send a signed-out Home Screen app to sign-in (research 2.3).
func manifestFor(siteName string) webManifest {
	short := siteName
	if utf8.RuneCountInString(short) > manifestShortNameMax {
		short = "Fold"
	}
	return webManifest{
		Name: siteName, ShortName: short,
		StartURL: "/?source=homescreen", Scope: "/", Display: "standalone",
		BackgroundColor: manifestBackground, ThemeColor: manifestTheme,
		Icons: []manifestIcon{
			{Src: "/assets/brand/fold-icon-192.png", Sizes: "192x192", Type: "image/png"},
			{Src: "/assets/brand/fold-icon-512.png", Sizes: "512x512", Type: "image/png"},
			{Src: "/assets/brand/fold-maskable-512.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
		},
	}
}

// serveManifest answers GET /manifest.webmanifest. no-cache: a renamed
// church shows on the next install without waiting out a cache.
func (a *App) serveManifest(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Header("Content-Type", "application/manifest+json")
	c.JSON(http.StatusOK, manifestFor(a.siteName()))
}
