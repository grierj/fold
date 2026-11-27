package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"fold/internal/accounts"
	"fold/internal/app"
	"fold/internal/server"
	"fold/internal/httpx"
	"fold/internal/mail"
	"fold/internal/testenv"
	"fold/internal/write"
)

// site is a running server over a fresh database, with the job runner and
// the sign-in queue driven by the test, never by timers.
type site struct {
	*testenv.Env
	t      *testing.T
	app    *app.App
	srv    *httptest.Server
	runner *write.Runner
	queue  *accounts.Queue
	outbox *mail.Outbox
}

func newSite(t *testing.T) *site {
	t.Helper()
	env := testenv.New(t)
	outbox := &mail.Outbox{}
	queue := accounts.NewQueue(env.Exec, env.Log)
	srv := httptest.NewUnstartedServer(nil)
	u, _ := url.Parse("http://" + srv.Listener.Addr().String())
	a, err := app.New(app.Config{SiteURL: u, Dev: true, Proxies: httpx.Proxies{}},
		app.Deps{Reader: env.Reader, Exec: env.Exec, Queue: queue, Keys: env.Keys, Clock: env.Clock, Log: env.Log, Outbox: outbox})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = a.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	runner := write.NewRunner(env.Exec, env.Log)
	server.RegisterJobs(runner, server.JobDeps{Exec: env.Exec, Reader: env.Reader, Keys: env.Keys, SiteURL: u.String(),
		Outbox: outbox, Dev: true, Clock: env.Clock, DataDir: env.Dir, Log: env.Log})
	return &site{Env: env, t: t, app: a, srv: srv, runner: runner, queue: queue, outbox: outbox}
}

// work runs everything queued: sign-in requests, then due jobs.
func (s *site) work() {
	s.t.Helper()
	s.queue.Drain(context.Background())
	if _, err := s.runner.RunDue(context.Background()); err != nil {
		s.t.Fatal(err)
	}
}

// browser is one person's browser: its own cookies.
type browser struct {
	s *site
	c *http.Client
}

func (s *site) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{s: s, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type resp struct {
	status int
	body   string
	header http.Header
}

func (b *browser) get(path string) resp {
	b.s.t.Helper()
	r, err := b.c.Get(b.s.srv.URL + path)
	if err != nil {
		b.s.t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, string(body), r.Header}
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// post submits a form from page, with the CSRF token that page carried.
func (b *browser) post(page, path string, form url.Values) resp {
	b.s.t.Helper()
	p := b.get(page)
	m := csrfPattern.FindStringSubmatch(p.body)
	if m == nil {
		b.s.t.Fatalf("no CSRF token on %s (status %d)", page, p.status)
	}
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", m[1])
	return b.postRaw(path, form, nil)
}

func (b *browser) postRaw(path string, form url.Values, header http.Header) resp {
	b.s.t.Helper()
	req, _ := http.NewRequest("POST", b.s.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header[k] = v
	}
	r, err := b.c.Do(req)
	if err != nil {
		b.s.t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, string(body), r.Header}
}

var linkPattern = regexp.MustCompile(`/l/([A-Za-z0-9_-]{43})`)

// linkIn is the token in the newest email to addr.
func (s *site) linkIn(addr string) string {
	s.t.Helper()
	m, ok := s.outbox.LatestTo(addr)
	if !ok {
		s.t.Fatalf("no email to %s", addr)
	}
	l := linkPattern.FindStringSubmatch(m.Text)
	if l == nil {
		s.t.Fatalf("no link in %q", m.Text)
	}
	return l[1]
}

func setupForm(email string) url.Values {
	return url.Values{
		"church_name": {"Cloverdale Church of God"}, "zone": {"America/Boise"},
		"name": {"Pat Admin"}, "email": {email}, "adult": {"yes"},
		"provider": {"outbox"}, "from": {"office@cloverdale.example"},
	}
}

// unlock enters the setup code in b.
func (b *browser) unlock() {
	b.s.t.Helper()
	r := b.post("/setup", "/setup/code", url.Values{"code": {b.s.app.SetupCode()}})
	if r.status != http.StatusSeeOther {
		b.s.t.Fatalf("unlock: %d %s", r.status, r.body)
	}
}

// finishSetup runs the whole setup and returns the admin's browser.
func (s *site) finishSetup() *browser {
	s.t.Helper()
	b := s.browser()
	b.unlock()
	if r := b.post("/setup", "/setup", setupForm("pat@cloverdale.example")); r.status != http.StatusSeeOther {
		s.t.Fatalf("setup: %d %s", r.status, r.body)
	}
	s.work()
	tok := s.linkIn("pat@cloverdale.example")
	if r := b.post("/signin", "/l/"+tok, nil); r.status != http.StatusSeeOther {
		s.t.Fatalf("finish: %d %s", r.status, r.body)
	}
	return b
}
