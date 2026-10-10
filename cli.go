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

	"fold/migrations"
)

// version is set at build time with -ldflags "-X main.version=x.y.z".
var version = "dev"

const usage = `usage: fold [command] [--json]

commands:
  serve                        run the web server (the default)
  healthcheck                  probe the local server (used by Docker)
  migrate [up|status|down-one] apply or inspect schema migrations (needs DATABASE_URL)
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
		return runHealthcheck(getEnv("PORT", "8080"))
	case "migrate":
		return runMigrate(args, asJSON, stdout, stderr)
	case "version":
		if asJSON {
			_ = json.NewEncoder(stdout).Encode(map[string]string{"version": version})
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

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
