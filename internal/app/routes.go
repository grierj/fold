package app

import "net/http"

// routes registers every route, here and nowhere else, so the budget count
// in AGENTS.md is one grep. A route that writes is a POST.
func (a *App) routes(mux *http.ServeMux) {
	mux.Handle("GET /{$}", http.HandlerFunc(a.home))
	mux.Handle("GET /static/", a.static())
	mux.Handle("GET /healthz", http.HandlerFunc(a.healthz))

	mux.Handle("GET /setup", http.HandlerFunc(a.getSetup))
	mux.Handle("POST /setup/code", http.HandlerFunc(a.postSetupCode))
	mux.Handle("POST /setup", http.HandlerFunc(a.postSetup))
	mux.Handle("GET /setup/sent", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.setupSent(w, r, "") }))
	mux.Handle("GET /setup/status", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.setupSent(w, r, "setupstatus") }))
	mux.Handle("POST /setup/resend", http.HandlerFunc(a.postSetupResend))

	mux.Handle("GET /signin", http.HandlerFunc(a.getSignin))
	mux.Handle("POST /signin", http.HandlerFunc(a.postSignin))
	mux.Handle("GET /l/{token}", http.HandlerFunc(a.getLink))
	mux.Handle("POST /l/{token}", http.HandlerFunc(a.postLink))
	mux.Handle("POST /signout", http.HandlerFunc(a.postSignout))

	mux.Handle("GET /admin/gallery", http.HandlerFunc(a.gallery))
	mux.Handle("POST /admin/gallery/toggle", http.HandlerFunc(a.galleryToggle))

	if a.cfg.Dev {
		mux.Handle("GET /dev/outbox", http.HandlerFunc(a.devOutbox))
	}
	mux.Handle("/", http.HandlerFunc(a.notFound))
}
