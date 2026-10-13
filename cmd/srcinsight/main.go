// Command srcinsight runs index, sync, and serve over the services pipeline.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"srcinsight/services"
)

// Version metadata is injected at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "srcinsight:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: srcinsight <index|sync|serve|version> --root <dir> [--db <file|dir>] [--force]")
	}
	switch args[0] {
	case "index", "sync":
		return runWrite(args[0], args[1:])
	case "serve":
		return runServe(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("srcinsight %s (commit %s, built %s)\n", version, commit, date)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want index|sync|serve|version)", args[0])
	}
}

func runWrite(cmd string, args []string) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	root := fs.String("root", ".", "project root to index")
	db := fs.String("db", "", "database file or directory (relative to --root; default <root>/.srcinsight/srcinsight.db)")
	dbAlias := fs.String("database-path", "", "alias for --db")
	force := fs.Bool("force", false, "force a full rescan (index only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dbPath := *db
	if dbPath == "" {
		dbPath = *dbAlias
	}
	p, err := services.NewWithOptions(services.PipelineOptions{Root: *root, DBPath: dbPath})
	if err != nil {
		return err
	}
	defer p.Close()
	ctx := context.Background()
	var res services.SyncResult
	if cmd == "index" {
		res, err = p.FullScan(ctx, *force)
	} else {
		res, err = p.Sync(ctx)
	}
	if err != nil {
		return err
	}
	fmt.Printf("checked=%d added=%d modified=%d removed=%d\n",
		res.FilesChecked, res.Added, res.Modified, res.Removed)
	return nil
}

// runServe ensures the index then watches until SIGINT or SIGTERM.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	root := fs.String("root", ".", "project root to serve")
	db := fs.String("db", "", "database file or directory (relative to --root; default <root>/.srcinsight/srcinsight.db)")
	dbAlias := fs.String("database-path", "", "alias for --db")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dbPath := *db
	if dbPath == "" {
		dbPath = *dbAlias
	}
	p, err := services.NewWithOptions(services.PipelineOptions{Root: *root, DBPath: dbPath})
	if err != nil {
		return err
	}
	defer p.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return p.Serve(ctx, os.Stdin, os.Stdout)
}
