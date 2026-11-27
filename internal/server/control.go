package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"fold/internal/accounts"
	"fold/internal/app"
	"fold/internal/backup"
	"fold/internal/db"
	"fold/internal/keys"
	"fold/internal/settings"
	"fold/internal/write"
)

// The control socket lets the operator's commands (fold backup, fold
// restore, fold setup-code) reach the running server, so a restore happens
// inside the one writer. It is a Unix socket in the data folder, readable
// only by the server's own user: reaching it already means being on the
// server with the data folder's owner's rights.

func socketPath(dataDir string) string { return filepath.Join(dataDir, "control.sock") }

type control struct {
	env    Env
	exec   *write.Executor
	reader *db.Reader
	app    *app.App
	log    *slog.Logger
}

func (c *control) listen() (net.Listener, error) {
	p := socketPath(c.env.DataDir)
	os.Remove(p) // a stale socket from a crash; the lock says we are the only server
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func (c *control) serve(ln net.Listener) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /backups", func(w http.ResponseWriter, r *http.Request) {
		list, err := backup.List(c.env.DataDir)
		reply(w, list, err)
	})
	mux.HandleFunc("POST /backup", func(w http.ResponseWriter, r *http.Request) {
		m, err := backup.Take(r.Context(), c.exec, c.env.DataDir)
		reply(w, m, err)
	})
	mux.HandleFunc("POST /restore", func(w http.ResponseWriter, r *http.Request) {
		m, err := backup.Restore(r.Context(), c.exec, c.env.DataDir, r.URL.Query().Get("name"))
		if err == nil {
			c.log.Warn("restored from a backup; every session ended and every link expired", "backup", m.Name)
			err = c.announceSetup(r.Context())
		}
		reply(w, m, err)
	})
	mux.HandleFunc("GET /setup-code", func(w http.ResponseWriter, r *http.Request) {
		code, err := c.setupCode(r.Context())
		reply(w, code, err)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	srv.Serve(ln)
}

func reply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(v)
}

// announceSetup prints the setup code to the log while setup is not done.
func (c *control) announceSetup(ctx context.Context) error {
	code, err := c.setupCode(ctx)
	if err != nil || code == "" {
		return err
	}
	c.log.Warn("setup is not finished; open the site and enter this setup code", "setup_code", code, "url", c.env.SiteURL.String()+"/setup")
	return nil
}

// setupCode is the current code while setup is not done, and "" after.
func (c *control) setupCode(ctx context.Context) (string, error) {
	var done bool
	err := c.reader.Read(ctx, func(q *sql.Tx) error {
		var err error
		done, err = accounts.IsSetupDone(ctx, q)
		return err
	})
	if err != nil || done {
		return "", err
	}
	return c.app.SetupCode(), nil
}

func decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// Call sends one command to the running server. ok is false when no server
// is listening.
func Call(dataDir, method, path string, out any) (bool, error) {
	client := &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath(dataDir))
		}}}
	req, _ := http.NewRequest(method, "http://server"+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) || errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		return true, errors.New(e.Error)
	}
	return true, json.NewDecoder(resp.Body).Decode(out)
}

// Reseal re-encrypts every sealed setting with the current secret after a
// rotation (SECRET_KEY_PREVIOUS set).
type Reseal struct{}

func (Reseal) Action() string { return "keys.reseal" }

func (Reseal) Apply(tx *write.Tx) error {
	s, err := settings.Load(tx.Context(), tx)
	if err != nil {
		return err
	}
	n := 0
	for _, key := range []string{settings.PostmarkToken, settings.SMTPPassword} {
		if !s.Has(key) {
			continue
		}
		raw, err := decode(s[key])
		if err != nil {
			return err
		}
		plain, usedPrevious, err := tx.Open(keys.Settings, raw)
		if err != nil {
			return fmt.Errorf("%s opens with neither secret: %w", key, err)
		}
		if usedPrevious {
			if err := settings.SaveSealed(tx, key, string(plain)); err != nil {
				return err
			}
			n++
		}
	}
	if n == 0 {
		return write.ErrNothing
	}
	tx.Detail("count", fmt.Sprint(n))
	return nil
}
