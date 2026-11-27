package app

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"fold/internal/accounts"
	"fold/internal/httpx"
	"fold/internal/keys"
	"fold/internal/mail"
	"fold/internal/people"
	"fold/internal/write"
)

// The setup code stops a stranger who reaches a fresh install from making
// themselves its admin (docs/HAZARDS.md section 5). It lives only in memory
// and in the server's log; a restart prints a new one.
type setupCode struct {
	mu   sync.Mutex
	code string
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O or 1/I

// SetupCode is the current code, made on first use: 16 characters, 80 bits,
// in groups of four.
func (a *App) SetupCode() string {
	a.setup.mu.Lock()
	defer a.setup.mu.Unlock()
	if a.setup.code != "" {
		return a.setup.code
	}
	var b [16]byte
	rand.Read(b[:])
	var s strings.Builder
	for i, x := range b {
		if i > 0 && i%4 == 0 {
			s.WriteByte('-')
		}
		s.WriteByte(codeAlphabet[int(x)%len(codeAlphabet)])
	}
	a.setup.code = s.String()
	return a.setup.code
}

func normalizeCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

func (a *App) currentCode() string {
	a.setup.mu.Lock()
	defer a.setup.mu.Unlock()
	return a.setup.code
}

// unlockValue is the cookie that says this browser entered the current code.
func (a *App) unlockValue() string {
	code := a.currentCode()
	if code == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(a.keys.MAC(keys.Setup, []byte("unlocked:"+normalizeCode(code))))
}

func (a *App) setupUnlocked(r *http.Request) bool {
	want := a.unlockValue()
	return want != "" && constantEqual(a.cookie(r, "setup"), want)
}

// setupDoneOr404 answers setup pages after setup is finished.
func (a *App) setupState(w http.ResponseWriter, r *http.Request) (done bool, ok bool) {
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		done, err = accounts.IsSetupDone(r.Context(), q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return false, false
	}
	return done, true
}

func (a *App) getSetup(w http.ResponseWriter, r *http.Request) {
	done, ok := a.setupState(w, r)
	if !ok {
		return
	}
	if done {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !a.setupUnlocked(r) {
		a.renderSetupCode(w, r, http.StatusOK, "")
		return
	}
	var in accounts.SetupInput
	var hasToken, hasPassword bool
	var st accounts.SetupStatus
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		if in, hasToken, hasPassword, err = accounts.SetupAnswers(r.Context(), q); err != nil {
			return err
		}
		st, err = accounts.LoadSetupStatus(r.Context(), q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if st.State != "" && r.URL.Query().Get("edit") == "" {
		http.Redirect(w, r, "/setup/sent", http.StatusSeeOther)
		return
	}
	a.renderSetupForm(w, r, http.StatusOK, a.setupForm(in, nil, hasToken, hasPassword, false))
}

func (a *App) renderSetupCode(w http.ResponseWriter, r *http.Request, status int, problem string) {
	f := Field{ID: "code", Name: "code", Label: "Setup code", Hint: "Sixteen letters and numbers, like ABCD-EFGH-JKLM-NPQR.",
		Autocomplete: "off", MaxLength: 40, Required: true, Autofocus: true}
	a.render(w, r, status, "setup_code", "", page{Data: struct {
		Code  Field
		Error string
	}{f, problem}})
}

func (a *App) postSetupCode(w http.ResponseWriter, r *http.Request) {
	done, ok := a.setupState(w, r)
	if !ok {
		return
	}
	if done {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	client := httpx.ClientKey(a.cfg.Proxies.ClientIP(r))
	if !a.limits.setupCode.Allow(client) {
		a.log.Warn("setup: too many code attempts", "client", client)
		a.renderSetupCode(w, r, http.StatusOK, "That code does not match. Check the server's log for the current code; it changes each time the server starts.")
		return
	}
	code := a.currentCode()
	got := r.PostFormValue("code")
	if code == "" || len(got) > 40 || !constantEqual(normalizeCode(got), normalizeCode(code)) {
		a.renderSetupCode(w, r, http.StatusOK, "That code does not match. Check the server's log for the current code; it changes each time the server starts.")
		return
	}
	a.setCookie(w, "setup", a.unlockValue(), 24*time.Hour)
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

// setupFormData is the setup form's fields.
type setupFormData struct {
	ChurchName, Zone, Name, Email, From Field
	PostmarkToken, SMTPHost, SMTPPort   Field
	SMTPUsername, SMTPPassword          Field
	Adult                               Choice
	Providers                           []Choice
	Errors                              []FieldError
}

const (
	maxName  = 100
	maxEmail = 254
	maxHost  = 253
	maxToken = 200
)

func (a *App) setupForm(in accounts.SetupInput, errs map[string]string, hasToken, hasPassword, adult bool) setupFormData {
	zoneDefault := in.Zone
	port := ""
	if in.Mail.SMTPPort != 0 {
		port = strconv.Itoa(in.Mail.SMTPPort)
	}
	keep := func(has bool, what string) string {
		if has {
			return "One is saved. Leave this blank to keep it."
		}
		return what
	}
	d := setupFormData{
		ChurchName: Field{ID: "church_name", Name: "church_name", Label: "Church name", Value: in.ChurchName, MaxLength: maxName, Required: true, Autocomplete: "organization"},
		Zone: Field{ID: "zone", Name: "zone", Label: "Time zone", Hint: "Where your church meets, so event times are right. Start typing a city, for example Boise, then choose from the list.",
			Value: zoneDefault, List: "zones", Options: zoneNames, MaxLength: 64, Required: true, Autocomplete: "off"},
		Name:  Field{ID: "name", Name: "name", Label: "Your name", Hint: "As people at church know you.", Value: in.Name, MaxLength: maxName, Required: true, Autocomplete: "name"},
		Email: Field{ID: "email", Name: "email", Label: "Your email", Hint: "You sign in with it. The test message goes here.", Type: "email", Value: in.Email, MaxLength: maxEmail, Required: true, Autocomplete: "email"},
		Adult: Choice{ID: "adult", Name: "adult", Type: "checkbox", Value: "yes", Label: "I am 18 or older", Hint: "The first administrator must be an adult.", Checked: adult},
		From: Field{ID: "from", Name: "from", Label: "Send email from", Hint: "The address your church's email comes from, for example office@yourchurch.org. Your mail service must allow it.",
			Type: "email", Value: in.Mail.From, MaxLength: maxEmail, Required: true, Autocomplete: "off"},
		PostmarkToken: Field{ID: "postmark_token", Name: "postmark_token", Label: "Postmark server token", Type: "password", Autocomplete: "off", MaxLength: maxToken,
			Hint: keep(hasToken, "In Postmark, open your server, then API Tokens.")},
		SMTPHost:     Field{ID: "smtp_host", Name: "smtp_host", Label: "Mail server", Hint: "For example smtp.example.org.", Value: in.Mail.SMTPHost, MaxLength: maxHost, Autocomplete: "off"},
		SMTPPort:     Field{ID: "smtp_port", Name: "smtp_port", Label: "Port", Hint: "587 or 465. The server must offer encryption.", Type: "text", Inputmode: "numeric", Value: port, MaxLength: 5, Autocomplete: "off"},
		SMTPUsername: Field{ID: "smtp_username", Name: "smtp_username", Label: "Username", Value: in.Mail.SMTPUsername, MaxLength: maxEmail, Autocomplete: "off"},
		SMTPPassword: Field{ID: "smtp_password", Name: "smtp_password", Label: "Password", Type: "password", Autocomplete: "off", MaxLength: maxToken,
			Hint: keep(hasPassword, "The mail server's password for that username.")},
	}
	provider := in.Mail.Provider
	if provider == "" {
		provider = "postmark"
	}
	d.Providers = []Choice{
		{ID: "provider-postmark", Name: "provider", Type: "radio", Value: "postmark", Label: "Postmark", Checked: provider == "postmark"},
		{ID: "provider-smtp", Name: "provider", Type: "radio", Value: "smtp", Label: "Another mail server (SMTP)", Checked: provider == "smtp"},
	}
	if a.cfg.Dev {
		d.Providers = append(d.Providers, Choice{ID: "provider-outbox", Name: "provider", Type: "radio", Value: "outbox",
			Label: "Development outbox", Hint: "Keeps mail on this computer at /dev/outbox.", Checked: provider == "outbox"})
	}
	set := func(f *Field) { f.Error = errs[f.Name] }
	for _, f := range []*Field{&d.ChurchName, &d.Zone, &d.Name, &d.Email, &d.From, &d.PostmarkToken, &d.SMTPHost, &d.SMTPPort, &d.SMTPUsername, &d.SMTPPassword} {
		set(f)
	}
	d.Adult.Error = errs["adult"]
	if e := errs["provider"]; e != "" {
		d.Providers[0].Error = e
	}
	d.Errors = errorSummary(d.ChurchName, d.Zone, d.Name, d.Email)
	if d.Adult.Error != "" {
		d.Errors = append(d.Errors, FieldError{ID: d.Adult.ID, Message: d.Adult.Error})
	}
	d.Errors = append(d.Errors, errorSummary(d.From, d.PostmarkToken, d.SMTPHost, d.SMTPPort, d.SMTPUsername, d.SMTPPassword)...)
	if errs == nil {
		d.ChurchName.Autofocus = true
	} else {
		focusFirstError(&d.ChurchName, &d.Zone, &d.Name, &d.Email, &d.From, &d.PostmarkToken, &d.SMTPHost, &d.SMTPPort, &d.SMTPUsername, &d.SMTPPassword)
	}
	return d
}

func (a *App) renderSetupForm(w http.ResponseWriter, r *http.Request, status int, d setupFormData) {
	a.render(w, r, status, "setup", "", page{Data: d})
}

// text reads a form value, trimmed, and says whether it is acceptable text:
// valid UTF-8, no control characters, at most max characters. A value that
// is not is refused before any lookup (docs/HAZARDS.md section 3).
func text(r *http.Request, name string, max int) (string, bool) {
	v := strings.TrimSpace(r.PostFormValue(name))
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max {
		return v, false
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return v, false
		}
	}
	return v, true
}

func (a *App) postSetup(w http.ResponseWriter, r *http.Request) {
	done, ok := a.setupState(w, r)
	if !ok {
		return
	}
	if done {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !a.setupUnlocked(r) {
		a.renderSetupCode(w, r, http.StatusOK, "Enter the setup code again. It changes each time the server starts.")
		return
	}
	errs := map[string]string{}
	field := func(name string, max int, missing string) string {
		v, ok := text(r, name, max)
		switch {
		case !ok:
			errs[name] = "That is too long or has characters we can't use."
		case v == "" && missing != "":
			errs[name] = missing
		}
		return v
	}
	var in accounts.SetupInput
	in.ChurchName = field("church_name", maxName, "Enter your church's name.")
	in.Zone = field("zone", 64, "Choose your church's time zone.")
	if in.Zone != "" && !slices.Contains(zoneNames, in.Zone) {
		errs["zone"] = "Choose a time zone from the list, for example America/Boise."
	}
	in.Name = field("name", maxName, "Enter your name.")
	in.Email = people.NormalizeEmail(field("email", maxEmail, "Enter your email address."))
	if in.Email != "" && errs["email"] == "" && !mail.ValidAddress(in.Email) {
		errs["email"] = "Enter an email address like name@example.org."
	}
	adult := r.PostFormValue("adult") == "yes"
	if !adult {
		errs["adult"] = "Confirm that you are 18 or older."
	}
	m := &in.Mail
	m.Provider = r.PostFormValue("provider")
	m.From = people.NormalizeEmail(field("from", maxEmail, "Enter the address your church's email comes from."))
	if m.From != "" && errs["from"] == "" && !mail.ValidAddress(m.From) {
		errs["from"] = "Enter an email address like office@yourchurch.org."
	}
	var hasToken, hasPassword bool
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		_, hasToken, hasPassword, err = accounts.SetupAnswers(r.Context(), q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	switch m.Provider {
	case "postmark":
		m.PostmarkToken = field("postmark_token", maxToken, "")
		if m.PostmarkToken == "" && !hasToken && errs["postmark_token"] == "" {
			errs["postmark_token"] = "Enter your Postmark server token."
		}
	case "smtp":
		m.SMTPHost = strings.ToLower(field("smtp_host", maxHost, "Enter your mail server's name."))
		if m.SMTPHost != "" && strings.ContainsAny(m.SMTPHost, " /:@") {
			errs["smtp_host"] = "Enter just the server's name, like smtp.example.org."
		}
		portText := field("smtp_port", 5, "Enter the port, usually 587.")
		if portText != "" {
			p, err := strconv.Atoi(portText)
			if err != nil || p < 1 || p > 65535 {
				errs["smtp_port"] = "Enter a port number, usually 587 or 465."
			}
			m.SMTPPort = p
		}
		m.SMTPUsername = field("smtp_username", maxEmail, "")
		m.SMTPPassword = field("smtp_password", maxToken, "")
		if m.SMTPUsername != "" && m.SMTPPassword == "" && !hasPassword && errs["smtp_password"] == "" {
			errs["smtp_password"] = "Enter the password for that username."
		}
	case "outbox":
		if !a.cfg.Dev {
			errs["provider"] = "Choose a mail service."
		}
	default:
		errs["provider"] = "Choose a mail service."
	}
	if len(errs) > 0 {
		a.renderSetupForm(w, r, http.StatusUnprocessableEntity, a.setupForm(in, errs, hasToken, hasPassword, adult))
		return
	}
	a.startSetup(w, r, in)
}

func (a *App) startSetup(w http.ResponseWriter, r *http.Request, in accounts.SetupInput) {
	// One person with the code may resend a few times; past that the page
	// says what happened rather than sending more.
	if !a.limits.setupMail.Allow("setup") {
		a.render(w, r, http.StatusOK, "problem", "", page{Data: problem{
			Title: "That is a lot of test messages", Text: "To protect your inbox, no more are being sent for a while. The last one is still good for 24 hours.",
			Next: "Back to setup", NextHref: "/setup/sent"}})
		return
	}
	err := a.exec.Run(r.Context(), write.Anonymous(), accounts.StartSetup{CodeVerified: a.setupUnlocked(r), In: in})
	if errors.Is(err, write.ErrNotAllowed) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/setup/sent", http.StatusSeeOther)
}

func (a *App) postSetupResend(w http.ResponseWriter, r *http.Request) {
	done, ok := a.setupState(w, r)
	if !ok {
		return
	}
	if done || !a.setupUnlocked(r) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	var in accounts.SetupInput
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		in, _, _, err = accounts.SetupAnswers(r.Context(), q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if in.Email == "" {
		http.Redirect(w, r, "/setup?edit=1", http.StatusSeeOther)
		return
	}
	a.startSetup(w, r, in)
}

func (a *App) setupSent(w http.ResponseWriter, r *http.Request, block string) {
	done, ok := a.setupState(w, r)
	if !ok {
		return
	}
	if done {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !a.setupUnlocked(r) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	var st accounts.SetupStatus
	err := a.reader.Read(r.Context(), func(q *sql.Tx) error {
		var err error
		st, err = accounts.LoadSetupStatus(r.Context(), q)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, "setup_sent", block, page{Data: st})
}
