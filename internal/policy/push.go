package policy

// Web push (M3.13b, DEC-122, DEC-124): who may turn on notifications on a
// device. Push needs an active account: a teen with one needs no separate
// consent, but a teen still waiting for consent has no feed, and a guest
// never signs in.

// Account statuses policy decides on (models.User.Status names them).
const (
	AccountActive         = "active"
	AccountGuest          = "guest"
	AccountPendingConsent = "pending_consent"
)

// Why a device can't turn on push; PushOK means it can.
const (
	PushOK             = ""
	PushGuest          = "guest"
	PushPendingConsent = "pending_consent"
	PushInactive       = "inactive"
	PushChurchOff      = "church_off"
)

// PushRefusal says why an account with status may not turn on push for a
// device, or PushOK. churchOn is the church's push_enabled switch; the
// account's own state is reported first, so a guest hears about accounts,
// not the church's setting.
func PushRefusal(status string, churchOn bool) string {
	switch status {
	case AccountActive:
	case AccountGuest:
		return PushGuest
	case AccountPendingConsent:
		return PushPendingConsent
	default:
		return PushInactive
	}
	if !churchOn {
		return PushChurchOff
	}
	return PushOK
}
