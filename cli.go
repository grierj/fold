package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"fold/internal/dbopen"
	"fold/internal/push"
	"fold/migrations"

	"gorm.io/gorm/logger"
)

// version is set at build time with -ldflags "-X main.version=x.y.z".
var version = "dev"

const usage = `usage: fold [command] [--json]

commands:
  serve                        run the web server (the default)
  healthcheck                  probe the local server (used by Docker)
  migrate [up|status|down-one] apply or inspect schema migrations (needs DATABASE_URL)
  push keys show               print the web push public key (needs DATABASE_URL)
  push keys rotate [--force]   make new web push keys and remove every device
                               (needs the server's SESSION_SECRET and DATABASE_URL;
                               --force rotates with a SESSION_SECRET that can't
                               open the current key)
  push check                   connect to each push service on port 443
  restore settle-uploads       after a restore that brought uploads back, empty the profile photo
                               folders of erased and unknown accounts (needs DATABASE_URL and
                               UPLOAD_DIR; exits 1 if any remain, 2 if the database schema is not
                               this release's; for the restore procedure, with the site closed)
  backup status                say whether the nightly backup is current; exits 0 when it
                               is, 1 when it is overdue, missing or unreadable
                               (needs only FOLD_BACKUP_DIR, an absolute path)
  version                      print the version
`

// run dispatches subcommands. Each one loads only the configuration it
// needs (C11): version needs nothing, migrate needs only DATABASE_URL.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	asJSON := slices.Contains(args, "--json")
	args = slices.DeleteFunc(args, func(s string) bool { return s == "--json" })

	switch cmd {
	case "serve":
		return serve(stderr)
	case "healthcheck":
		return runHealthcheck(envPort())
	case "migrate":
		return runMigrate(args, asJSON, stdout, stderr)
	case "push":
		return runPush(args, asJSON, stdout, stderr)
	case "backup":
		return runBackup(args, asJSON, stdout, stderr)
	case "restore":
		return runRestore(args, asJSON, stdout, stderr)
	case "version":
		if asJSON {
			// recoveryPolicy is the restore tooling's compatibility level
			// (R4-RP) and restoreUploads says this binary has
			// `restore settle-uploads`; an older binary has neither field,
			// which reads as 0.
			_ = json.NewEncoder(stdout).Encode(map[string]any{"version": version, "recoveryPolicy": recoveryPolicyLevel, "restoreUploads": restoreUploadsLevel})
		} else {
			fmt.Fprintln(stdout, "fold", version)
		}
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "fold: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

func runMigrate(args []string, asJSON bool, stdout, stderr io.Writer) int {
	sub := "up"
	if len(args) > 0 {
		sub = args[0]
	}
	url, err := databaseURL()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	defer db.Close()
	p, err := migrations.NewProvider(db)
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	ctx := context.Background()

	type row struct {
		Version int64  `json:"version"`
		Name    string `json:"name"`
		State   string `json:"state"`
	}
	var rows []row
	switch sub {
	case "up":
		res, err := p.Up(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "fold: migrate up:", err)
			return 1
		}
		for _, r := range res {
			rows = append(rows, row{r.Source.Version, path.Base(r.Source.Path), "applied"})
		}
	case "down-one":
		r, err := p.Down(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "fold: migrate down-one:", err)
			return 1
		}
		rows = append(rows, row{r.Source.Version, path.Base(r.Source.Path), "rolled back"})
	case "status":
		st, err := p.Status(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "fold: migrate status:", err)
			return 1
		}
		for _, s := range st {
			rows = append(rows, row{s.Source.Version, path.Base(s.Source.Path), string(s.State)})
		}
	default:
		fmt.Fprintf(stderr, "fold: unknown migrate command %q (want up, status, or down-one)\n", sub)
		return 2
	}

	if asJSON {
		if rows == nil {
			rows = []row{}
		}
		_ = json.NewEncoder(stdout).Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no migrations to apply")
	}
	for _, r := range rows {
		fmt.Fprintf(stdout, "%05d  %-10s  %s\n", r.Version, r.State, r.Name)
	}
	return 0
}

// backupNow is the clock `fold backup status` judges by; tests replace it.
var backupNow = time.Now

// runBackup is `fold backup status` (M5.2). It reads only FOLD_BACKUP_DIR: no
// database and no session secret, so an operator can run it from a bare shell
// or a monitoring job. Exit 0 ok, 1 overdue, missing or unreadable, 2 bad
// usage or no directory to read. Output never names a path or file content.
func runBackup(args []string, asJSON bool, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "status" {
		fmt.Fprintf(stderr, "fold: unknown backup command %q (want status)\n", strings.Join(args, " "))
		return 2
	}
	dir, err := backupDir()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 2
	}
	if dir == "" {
		fmt.Fprintln(stderr, "fold: FOLD_BACKUP_DIR is not set. Set it to the directory that holds the backup service's LAST_OK (Compose mounts it at /backup-status in the app container)")
		return 2
	}
	st := readBackupStatus(dir, backupNow())
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(st)
	} else {
		writeBackupStatus(stdout, st)
	}
	if st.State == backupOK {
		return 0
	}
	return 1
}

// writeBackupStatus is the plain-text answer: one line for the state, then
// what is known. It reads only the typed fields.
func writeBackupStatus(w io.Writer, st backupStatusJSON) {
	const stamp = "2006-01-02 15:04 UTC"
	switch st.State {
	case backupOK:
		fmt.Fprintf(w, "backups: ok, last finished %s\n", st.LastOkAt.UTC().Format(stamp))
	case backupStale:
		fmt.Fprintf(w, "backups: overdue, last finished %s\n", st.LastOkAt.UTC().Format(stamp))
	case backupMissing:
		fmt.Fprintln(w, "backups: none has finished yet")
	default:
		fmt.Fprintln(w, "backups: the status can't be read")
	}
	if st.DumpBytes != nil && st.UploadsBytes != nil && st.Offsite != nil {
		fmt.Fprintf(w, "database %d bytes, photos %d bytes, offsite copy %s\n", *st.DumpBytes, *st.UploadsBytes, *st.Offsite)
	}
	if st.LastFailedAt != nil {
		step := "an unknown step"
		if st.LastFailedStep != nil {
			step = "step " + *st.LastFailedStep
		}
		fmt.Fprintf(w, "last try failed %s, at %s\n", st.LastFailedAt.UTC().Format(stamp), step)
	}
}

// pushCheckDial is how `fold push check` connects; tests replace it so
// they need no network. nil is push's own dialer.
var pushCheckDial push.DialFunc

// runPush is `fold push keys show|rotate` and `fold push check` (M3.13a,
// PLAN 11.4). Each loads only what it needs: check needs nothing, show
// only DATABASE_URL, rotate SESSION_SECRET as well.
func runPush(args []string, asJSON bool, stdout, stderr io.Writer) int {
	force := slices.Contains(args, "--force")
	args = slices.DeleteFunc(args, func(s string) bool { return s == "--force" })
	sub := strings.Join(args, " ")
	if force && sub != "keys rotate" {
		fmt.Fprintln(stderr, "fold: --force goes only with push keys rotate")
		return 2
	}
	switch sub {
	case "check":
		return runPushCheck(asJSON, stdout, stderr)
	case "keys show", "keys rotate":
	default:
		fmt.Fprintf(stderr, "fold: unknown push command %q (want keys show, keys rotate, or check)\n", sub)
		return 2
	}
	var secret string
	if sub == "keys rotate" {
		var err error
		if secret, err = sessionSecret(envName() == "dev"); err != nil {
			fmt.Fprintln(stderr, "fold:", err)
			return 1
		}
	}
	url, err := databaseURL()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	// No migrations here: `fold migrate` and `fold serve` do that.
	db, err := dbopen.Open(url, logger.Discard)
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}
	var removed int64
	if sub == "keys rotate" {
		if removed, err = rotateVAPIDKeys(db, []byte(secret), force); err != nil {
			fmt.Fprintln(stderr, "fold: push keys rotate:", err)
			return 1
		}
	}
	public, _, err := readVAPID(db)
	if err != nil {
		fmt.Fprintf(stderr, "fold: push %s: %v\n", sub, err)
		return 1
	}
	switch {
	case sub == "keys rotate" && asJSON:
		_ = json.NewEncoder(stdout).Encode(map[string]any{"publicKey": public, "devicesRemoved": removed})
	case sub == "keys rotate":
		fmt.Fprintf(stdout, "made new web push keys; removed %d devices (members need to turn notifications on again)\n", removed)
	case public == "":
		fmt.Fprintln(stderr, "fold: no web push keys yet; the server makes them when it starts")
		return 1
	case asJSON:
		_ = json.NewEncoder(stdout).Encode(map[string]string{"publicKey": public})
	default:
		fmt.Fprintln(stdout, public)
	}
	return 0
}

// runPushCheck resolves and connects to each allow-listed push host on
// 443 and says which it can't reach. It exits 1 if any fails.
func runPushCheck(asJSON bool, stdout, stderr io.Writer) int {
	extra, err := envPushHosts()
	if err != nil {
		fmt.Fprintln(stderr, "fold:", err)
		return 1
	}
	results := push.Check(context.Background(), push.NewAllowList(extra...).Hosts(), pushCheckDial)
	failed := false
	for _, r := range results {
		failed = failed || !r.OK
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(results)
	} else {
		for _, r := range results {
			if r.OK {
				fmt.Fprintf(stdout, "OK    %s:443\n", r.Host)
			} else {
				fmt.Fprintf(stdout, "FAIL  %s:443  %s\n", r.Host, r.Error)
			}
		}
		if failed {
			fmt.Fprintln(stderr, "fold: some push services can't be reached, so notifications to browsers that use them will fail. Allow outbound https (port 443) to them.")
		}
	}
	if failed {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
