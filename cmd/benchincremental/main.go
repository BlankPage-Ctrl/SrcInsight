package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"srcinsight/services"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: benchincremental <root>")
		os.Exit(1)
	}
	root := os.Args[1]
	p, err := services.New(root)
	if err != nil {
		panic(err)
	}
	defer p.Close()
	ctx := context.Background()

	// Prefer packages/*.ts targets.
	var target string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if target != "" {
			return filepath.SkipDir
		}
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".ts" && target == "" {
			rel, _ := filepath.Rel(root, path)
			if len(rel) > 9 && rel[:9] == "packages/" {
				target = rel
				return fmt.Errorf("found")
			}
		}
		return nil
	})
	if target == "" {
		// Fall back to the first ts file.
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if target != "" {
				return filepath.SkipDir
			}
			if err != nil || info.IsDir() {
				return nil
			}
			if filepath.Ext(path) == ".ts" {
				rel, _ := filepath.Rel(root, path)
				target = rel
				return fmt.Errorf("found")
			}
			return nil
		})
	}
	if target == "" {
		fmt.Println("no target found")
		return
	}
	abs := filepath.Join(root, target)
	orig, err := os.ReadFile(abs)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Target: %s\n", target)

	touch := string(orig) + "\n// bench incremental touch\n"
	if err := os.WriteFile(abs, []byte(touch), 0644); err != nil {
		panic(err)
	}
	start := time.Now()
	res, err := p.SyncWithHints(ctx, []string{target})
	if err != nil {
		panic(err)
	}
	fmt.Printf("incremental-scoped: checked=%d added=%d modified=%d removed=%d wall=%.3fs\n", res.FilesChecked, res.Added, res.Modified, res.Removed, time.Since(start).Seconds())
	if err := os.WriteFile(abs, []byte(string(orig)+"\n// bench incremental touch full\n"), 0644); err != nil {
		panic(err)
	}
	start = time.Now()
	res2, err := p.Sync(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("incremental-full: checked=%d added=%d modified=%d removed=%d wall=%.3fs\n", res2.FilesChecked, res2.Added, res2.Modified, res2.Removed, time.Since(start).Seconds())

	start = time.Now()
	res3, err := p.SyncWithHints(ctx, []string{target})
	if err != nil {
		panic(err)
	}
	fmt.Printf("incremental-scoped-noop: checked=%d changed=%d wall=%.3fs\n", res3.FilesChecked, res3.Changed(), time.Since(start).Seconds())

	start = time.Now()
	res4, err := p.Sync(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Printf("incremental-full-noop: checked=%d changed=%d wall=%.3fs\n", res4.FilesChecked, res4.Changed(), time.Since(start).Seconds())

	if err := os.WriteFile(abs, orig, 0644); err != nil {
		panic(err)
	}
	_, _ = p.SyncWithHints(ctx, []string{target})
	fmt.Println("reverted")
}
