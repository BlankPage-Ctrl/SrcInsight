package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/methods"
)

func main() {
	ctx := context.Background()
	work := os.Args[1]
	if work == "" {
		work = "."
	}
	dbPath, _ := cold.DefaultPath(work)
	v, err := cold.Open(dbPath)
	if err != nil {
		panic(err)
	}
	defer v.Close()
	rows, _ := v.SearchPrefix(ctx, "bootstrap", 1)
	fid := ""
	if len(rows) > 0 {
		fid = string(rows[0].ID)
	} else {
		r2, _ := v.SearchPrefix(ctx, "", 1)
		if len(r2) > 0 {
			fid = string(r2[0].ID)
		}
	}
	start := time.Now()
	g, _ := methods.Graph(ctx, v, methods.GraphOptions{ID: fid})
	fmt.Printf("Graph fid=%s nodes=%d edges=%d latency=%s\n", fid, len(g.Nodes), len(g.Edges), time.Since(start))
	start = time.Now()
	r, _ := methods.Search(ctx, v, methods.SearchOptions{Query: "bootstrapApplication"})
	fmt.Printf("Search hits=%d latency=%s\n", len(r.Hits), time.Since(start))
}
