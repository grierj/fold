package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"fold/internal/brand"
	"fold/internal/httpx"
	"fold/web"
)

// templates holds one template set per page: the layout, the partials and
// that page's file. Every value is escaped by html/template; nothing here
// marks user text as safe.
type templates struct {
	pages  map[string]*template.Template
	assets map[string]string // name -> content hash, for cache-busting URLs
}

func loadTemplates() (*templates, error) {
	t := &templates{pages: map[string]*template.Template{}, assets: map[string]string{}}
	err := fs.WalkDir(web.Static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(web.Static, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		t.assets[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:6])
		return nil
	})
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"asset": func(name string) (string, error) {
			h, ok := t.assets[name]
			if !ok {
				return "", fmt.Errorf("no static file %q", name)
			}
			return "/static/" + name + "?v=" + h, nil
		},
		"button": func(kind, label string) Button { return Button{Kind: kind, Label: label} },
		"wide":   func(b Button) Button { b.Wide = true; return b },
		"linkbutton": func(kind, label, href string) *LinkButton {
			return &LinkButton{Kind: kind, Label: label, Href: href}
		},
		"empty": func(image, title, text string, action *LinkButton) Empty {
			return Empty{Image: "illustrations/" + image, Title: title, Text: text, Action: action}
		},
		"emptypage": func(image, title, text string, action *LinkButton) Empty {
			return Empty{Image: "illustrations/" + image, Title: title, Text: text, Action: action, Main: true}
		},
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict needs pairs")
			}
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict keys are strings")
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"hasPrefix": strings.HasPrefix,
		"hidden":    func(name string, value any) Hidden { return Hidden{Name: name, Value: fmt.Sprint(value)} },
		"notice":    func(tone, title, text string) Notice { return Notice{Tone: tone, Title: title, Text: text} },
	}
	base, err := template.New("").Funcs(funcs).ParseFS(web.Templates, "templates/base.html", "templates/partials/*.html")
	if err != nil {
		return nil, err
	}
	pages, err := fs.Glob(web.Templates, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		c, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if _, err := c.ParseFS(web.Templates, p); err != nil {
			return nil, err
		}
		t.pages[strings.TrimSuffix(path.Base(p), ".html")] = c
	}
	return t, nil
}

// page is what every template receives.
type page struct {
	Path    string // the request's path, for marking the current tab
	Product string
	Church  string
	Viewer  *viewerInfo
	Unread  int // the bell's count
	CSRF    string
	Data    any
}

type viewerInfo struct {
	Name      string
	IsAdmin   bool
	ZoneUnset bool // the page asks the browser for its zone (DEC-58)
}

// render writes a whole page, or one named block of it for a fragment
// request. A render error is found before anything is written.
func (a *App) render(w http.ResponseWriter, r *http.Request, status int, name, block string, p page) {
	p.Product = brand.Name
	p.Path = r.URL.Path
	if p.CSRF == "" {
		p.CSRF = a.csrfToken(r)
	}
	t, ok := a.tmpl.pages[name]
	if !ok {
		panic("no page template " + name)
	}
	if block == "" {
		block = "base"
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, p); err != nil {
		a.log.Error("render", "page", name, "err", err)
		http.Error(w, "Something went wrong on our side. Please try again in a moment.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	a.renderPlain(w, r, http.StatusNotFound, "notfound", nil)
}

// problem is a page that says what happened and offers the next step.
type problem struct{ Title, Text, Next, NextHref string }

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("request failed", "path", httpx.RedactPath(r.URL.Path), "err", err)
	a.render(w, r, http.StatusInternalServerError, "problem", "", page{Data: problem{
		Title: "Something went wrong", Text: "That could not be finished because of a problem on our side. Nothing was changed. Please try again in a moment.",
		Next: "Go to the home page", NextHref: "/"}})
}
