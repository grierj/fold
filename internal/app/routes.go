package app

import "net/http"

// routes registers every route, here and nowhere else, so the budget count
// in AGENTS.md is one grep. A route that writes is a POST, except GET
// /l/{token} in the browser that asked for the link (R26).
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
	mux.Handle("GET /request", http.HandlerFunc(a.getRequest))
	mux.Handle("POST /request", http.HandlerFunc(a.postRequest))

	mux.Handle("GET /notices", http.HandlerFunc(a.getNotices))
	mux.Handle("POST /notices/{id}/open", http.HandlerFunc(a.postNoticeOpen))
	mux.Handle("POST /notices/read", http.HandlerFunc(a.postNoticesRead))

	mux.Handle("GET /admin/requests", http.HandlerFunc(a.getAdminRequests))
	mux.Handle("POST /admin/requests/{uid}/approve", a.postDecideRequest(true))
	mux.Handle("POST /admin/requests/{uid}/decline", a.postDecideRequest(false))

	mux.Handle("GET /admin/gallery", http.HandlerFunc(a.gallery))
	mux.Handle("POST /admin/gallery/toggle", http.HandlerFunc(a.galleryToggle))

	if a.cfg.Dev {
		mux.Handle("GET /dev/outbox", http.HandlerFunc(a.devOutbox))
	}
	mux.Handle("/", http.HandlerFunc(a.notFound))
}
