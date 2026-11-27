// Command fold is the whole application: the web server and the few commands
// an operator runs on the server.
//
//	fold serve              run the site (the container's default)
//	fold backup             take a backup now
//	fold backups            list backups
//	fold restore NAME       restore a backup (ends every session)
//	fold setup-code         print the setup code while setup is unfinished
//	fold verify             check the audit log's chain
//	fold healthcheck        exit 0 if the site answers (for Docker)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // every IANA zone, whatever the image carries

	"fold/internal/audit"
	"fold/internal/backup"
	"fold/internal/clock"
	"fold/internal/db"
	"fold/internal/server"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(cmd, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "fold:", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	switch cmd {
	case "serve":
		env, err := server.ReadEnv(os.Getenv)
		if err != nil {
			return err
		}
		log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return server.Serve(ctx, env, log)
	case "healthcheck":
		addr := os.Getenv("LISTEN")
		if addr == "" || addr[0] == ':' {
			addr = "127.0.0.1" + addr
			if addr == "127.0.0.1" {
				addr = "127.0.0.1:8080"
			}
		}
		c := &http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get("http://" + addr + "/healthz")
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("health: %s", resp.Status)
		}
		return nil
	case "backup":
		var m backup.Meta
		if err := callServer("POST", "/backup", &m); err != nil {
			return err
		}
		fmt.Printf("Backup %s taken (audit checkpoint %s).\n", m.Name, m.Checkpoint)
		return nil
	case "backups":
		list, err := backup.List(dataDir())
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("No backups yet. One is taken every night at 3 am, church time.")
		}
		for _, m := range list {
			fmt.Printf("%s  taken %s  audit row %d\n", m.Name, m.TakenAt.Local().Format("Mon 2 Jan 2006 15:04"), m.Checkpoint.Seq)
		}
		return nil
	case "restore":
		return restore(args)
	case "setup-code":
		var code string
		if err := callServer("GET", "/setup-code", &code); err != nil {
			return err
		}
		if code == "" {
			fmt.Println("Setup is finished; there is no setup code.")
			return nil
		}
		fmt.Println(code)
		return nil
	case "verify":
		return verify()
	}
	return fmt.Errorf("unknown command %q (try serve, backup, backups, restore, setup-code, verify)", cmd)
}

func dataDir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return d
	}
	return "/data"
}

func callServer(method, path string, out any) error {
	ok, err := server.Call(dataDir(), method, path, out)
	if !ok && err == nil {
		return errors.New("the server is not running; start it first (docker compose up -d)")
	}
	return err
}

// restore asks the running server to restore. When the server is stopped
// (perhaps because the database will not open), it restores directly.
func restore(args []string) error {
	if len(args) != 1 || !backup.ValidName(args[0]) {
		return errors.New("name the backup to restore, for example: fold restore 20261125-100000 (fold backups lists them)")
	}
	var m backup.Meta
	ok, err := server.Call(dataDir(), "POST", "/restore?name="+args[0], &m)
	if !ok && err == nil {
		env, err := server.ReadEnv(os.Getenv)
		if err != nil {
			return err
		}
		o, err := openForRestore(env)
		if err != nil {
			return err
		}
		defer o.Close()
		m, err = backup.Restore(context.Background(), o.Exec, env.DataDir, args[0])
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	b, _ := json.Marshal(m.Checkpoint)
	fmt.Printf("Restored backup %s. Everyone was signed out and every emailed link stopped working. Audit checkpoint of the backup: %s\n", m.Name, b)
	return nil
}

func openForRestore(env server.Env) (*server.Opened, error) {
	o, err := server.Open(context.Background(), env.DataDir, env.Keys, clock.Real{})
	if err == nil {
		return o, nil
	}
	return nil, fmt.Errorf("could not open the database to restore into: %w", err)
}

// verify checks the chain of the live database, read-only.
func verify() error {
	r, err := db.OpenReader(server.DBPath(dataDir()))
	if err != nil {
		return err
	}
	defer r.Close()
	var rep audit.Report
	err = r.Read(context.Background(), func(q *sql.Tx) error {
		var err error
		rep, err = audit.Verify(context.Background(), q)
		return err
	})
	if err != nil {
		return err
	}
	if !rep.Intact() {
		return fmt.Errorf("the audit log is broken: %s", rep.Problem)
	}
	fmt.Printf("The audit log is whole: %d rows, checkpoint %s\n", rep.Rows, rep.Last)
	return nil
}
