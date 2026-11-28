package app

import "net/http"

// routes registers every route, here and nowhere else, so the budget count
// in AGENTS.md is one grep. A route that writes is a POST, except GET
// /l/{token} in the browser that asked for the link (R26). /l/, /invite/
// and /g/ carry secrets, which the access log never writes.
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

	mux.Handle("GET /calendar", http.HandlerFunc(a.getCalendar))

	mux.Handle("GET /notices", http.HandlerFunc(a.getNotices))
	mux.Handle("POST /notices/{id}/open", http.HandlerFunc(a.postNoticeOpen))
	mux.Handle("POST /notices/read", http.HandlerFunc(a.postNoticesRead))

	mux.Handle("GET /groups", http.HandlerFunc(a.getGroups))
	mux.Handle("GET /groups/new", http.HandlerFunc(a.getNewGroup))
	mux.Handle("POST /groups/new", http.HandlerFunc(a.postNewGroup))
	mux.Handle("GET /groups/{uid}", http.HandlerFunc(a.getGroup))
	mux.Handle("GET /groups/{uid}/edit", http.HandlerFunc(a.getEditGroup))
	mux.Handle("POST /groups/{uid}/edit", http.HandlerFunc(a.postEditGroup))
	mux.Handle("POST /groups/{uid}/join", http.HandlerFunc(a.postJoin))
	mux.Handle("POST /groups/{uid}/leave", http.HandlerFunc(a.postLeave))
	mux.Handle("POST /groups/{uid}/archive", http.HandlerFunc(a.postArchive))
	mux.Handle("POST /groups/{uid}/unarchive", http.HandlerFunc(a.postUnarchive))
	mux.Handle("POST /groups/{uid}/delete", http.HandlerFunc(a.postDeleteGroup))
	mux.Handle("GET /groups/{uid}/events/new", http.HandlerFunc(a.getNewEvent))
	mux.Handle("POST /groups/{uid}/events/new", http.HandlerFunc(a.postNewEvent))
	mux.Handle("GET /events/{uid}", http.HandlerFunc(a.getEvent))
	mux.Handle("GET /events/{uid}/edit", http.HandlerFunc(a.getEditEvent))
	mux.Handle("POST /events/{uid}/edit", http.HandlerFunc(a.postEditEvent))
	mux.Handle("POST /events/{uid}/delete", http.HandlerFunc(a.postDeleteEvent))
	mux.Handle("GET /dates/{uid}", http.HandlerFunc(a.getDate))
	mux.Handle("GET /dates/{uid}/calendar.ics", http.HandlerFunc(a.getDateICS))
	mux.Handle("POST /dates/{uid}/answer", http.HandlerFunc(a.postAnswer))
	mux.Handle("GET /dates/{uid}/guest", http.HandlerFunc(a.getGuest))
	mux.Handle("POST /dates/{uid}/guest", http.HandlerFunc(a.postGuest))
	mux.Handle("POST /dates/{uid}/withdraw", http.HandlerFunc(a.postGuestCancel))
	mux.Handle("GET /g/{token}", http.HandlerFunc(a.getManage))
	mux.Handle("POST /g/{token}", http.HandlerFunc(a.postManage))
	mux.Handle("POST /dates/{uid}/cancel", http.HandlerFunc(a.postCancelDate))
	mux.Handle("POST /dates/{uid}/restore", http.HandlerFunc(a.postRestoreDate))
	mux.Handle("POST /dates/{uid}/people/{person}/remove", http.HandlerFunc(a.postRemoveFromDate))
	mux.Handle("POST /events/{uid}/people/{person}/remove", http.HandlerFunc(a.postRemoveFromEvent))
	mux.Handle("GET /dates/{uid}/open", http.HandlerFunc(a.getOpenDate))
	mux.Handle("POST /dates/{uid}/open", http.HandlerFunc(a.postOpenDate))
	mux.Handle("GET /events/new", http.HandlerFunc(a.getNewChurchEvent))
	mux.Handle("POST /events/new", http.HandlerFunc(a.postNewChurchEvent))
	mux.Handle("GET /events/{uid}/hosts", http.HandlerFunc(a.getHosts))
	mux.Handle("POST /events/{uid}/hosts/add", http.HandlerFunc(a.postAddHost))
	mux.Handle("POST /events/{uid}/hosts/{person}/remove", http.HandlerFunc(a.postRemoveHost))
	mux.Handle("GET /admin/events", http.HandlerFunc(a.getAdminEvents))
	mux.Handle("GET /groups/{uid}/members", http.HandlerFunc(a.getRoster))
	mux.Handle("POST /groups/{uid}/members/add", http.HandlerFunc(a.postAddMember))
	mux.Handle("POST /groups/{uid}/members/ask", http.HandlerFunc(a.postInvitePerson))
	mux.Handle("GET /invitations/{uid}", http.HandlerFunc(a.getInvitation))
	mux.Handle("POST /invitations/{uid}/accept", a.postAnswerInvitation(true))
	mux.Handle("POST /invitations/{uid}/decline", a.postAnswerInvitation(false))
	mux.Handle("GET /invite/{token}", http.HandlerFunc(a.getRedeem))
	mux.Handle("POST /invite/{token}", http.HandlerFunc(a.postRedeem))
	mux.Handle("POST /groups/{uid}/members/{person}/leader", http.HandlerFunc(a.postMakeLeader))
	mux.Handle("POST /groups/{uid}/members/{person}/member", http.HandlerFunc(a.postMakeMember))
	mux.Handle("POST /groups/{uid}/members/{person}/remove", http.HandlerFunc(a.postRemoveMember))
	mux.Handle("POST /groups/{uid}/invite", http.HandlerFunc(a.postInvite))
	mux.Handle("POST /admin/invite", http.HandlerFunc(a.postAdminInvite))
	mux.Handle("GET /admin/people", http.HandlerFunc(a.getAdminPeople))
	mux.Handle("GET /admin/people/{uid}", http.HandlerFunc(a.getAdminPerson))
	mux.Handle("POST /admin/people/{uid}/disable", http.HandlerFunc(a.postDisable))
	mux.Handle("POST /admin/people/{uid}/enable", http.HandlerFunc(a.postEnable))

	mux.Handle("GET /profile", http.HandlerFunc(a.getProfile))
	mux.Handle("POST /profile/name", http.HandlerFunc(a.postProfileName))
	mux.Handle("POST /profile/contact", http.HandlerFunc(a.postProfileContact))
	mux.Handle("POST /profile/zone", http.HandlerFunc(a.postProfileZone))

	mux.Handle("GET /admin/groups", http.HandlerFunc(a.getAdminGroups))
	mux.Handle("POST /admin/groups/{uid}/approve", a.postDecideGroup(true))
	mux.Handle("POST /admin/groups/{uid}/reject", a.postDecideGroup(false))
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
