package services

import (
	"context"
	"sync"

	"srcinsight/internal/indexer"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/adapters"
	"srcinsight/services/shared"
)

// runJobs runs fn over 0..n-1 on a fixed worker pool.
func runJobs(ctx context.Context, n, workers int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	jobs := make(chan int, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
			break
		}
	}
	close(jobs)
	wg.Wait()
}

// processFiles parses files in parallel; the caller commits sequentially.
func (p *Pipeline) processFiles(ctx context.Context, files []shared.DiskFile) (bundles, faults []cold.OriginBundle, err error) {
	type outcome struct {
		bundle cold.OriginBundle
		fault  cold.OriginBundle
		has    bool // bundle usable
		isBad  bool // fault bundle present
		skip   bool
	}
	out := make([]outcome, len(files))

	runJobs(ctx, len(files), p.pool.Size(), func(i int) {
		b, f, skip := p.processOne(files[i])
		out[i] = outcome{bundle: b, fault: f, has: !skip && !isZeroBundle(b), isBad: !skip && !isZeroBundle(f), skip: skip}
	})
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	for _, o := range out {
		switch {
		case o.skip:
			continue
		case o.has:
			bundles = append(bundles, o.bundle)
		case o.isBad:
			faults = append(faults, o.fault)
		}
	}
	return bundles, faults, nil
}

func isZeroBundle(b cold.OriginBundle) bool {
	return b.Origin.Path == ""
}

// rawFile holds disk content plus hash; parsing happens later in buildBundle.
type rawFile struct {
	df    shared.DiskFile
	src   []byte
	hash  string
	fault cold.OriginBundle
	skip  bool // vanished mid-run: retry next time, no vault write
	bad   bool // fault bundle present (too large / unreadable as fault)
}

// readRaws reads and hashes files in parallel without parsing.
func (p *Pipeline) readRaws(ctx context.Context, files []shared.DiskFile) ([]rawFile, error) {
	out := make([]rawFile, len(files))
	runJobs(ctx, len(files), p.pool.Size(), func(i int) {
		out[i] = p.readRaw(files[i])
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// buildBundles parses raws in parallel; callers commit sequentially.
func (p *Pipeline) buildBundles(ctx context.Context, raws []rawFile) (bundles, faults []cold.OriginBundle, skipped int) {
	type outcome struct {
		bundle cold.OriginBundle
		fault  cold.OriginBundle
		has    bool
		isBad  bool
		skip   bool
	}
	out := make([]outcome, len(raws))
	runJobs(ctx, len(raws), p.pool.Size(), func(i int) {
		b, f, skip := p.buildBundle(raws[i])
		out[i] = outcome{bundle: b, fault: f, has: !skip && !isZeroBundle(b), isBad: !skip && !isZeroBundle(f), skip: skip}
	})
	for _, o := range out {
		switch {
		case o.skip:
			skipped++
		case o.has:
			bundles = append(bundles, o.bundle)
		case o.isBad:
			faults = append(faults, o.fault)
		}
	}
	return bundles, faults, skipped
}

// processOne bundles one file; skip means vanished mid-run with no vault write.
func (p *Pipeline) processOne(df shared.DiskFile) (bundle, fault cold.OriginBundle, skip bool) {
	r := p.readRaw(df)
	return p.buildBundle(r)
}

// readRaw reads and hashes one file without parsing.
func (p *Pipeline) readRaw(df shared.DiskFile) rawFile {
	src, err := p.root.ReadFile(df.Rel)
	if err != nil {
		if shared.IsTooLarge(err) {
			return rawFile{df: df, bad: true,
				fault: shared.FaultBundle(df, model.Language(""), "", []string{"file too large: " + err.Error()})}
		}
		return rawFile{df: df, skip: true}
	}
	if _, ok := adapters.Lookup(df.Rel); !ok {
		return rawFile{df: df, skip: true}
	}
	return rawFile{df: df, src: src, hash: shared.HashBytes(src)}
}

// buildBundle parses, indexes, and resolves one raw file.
func (p *Pipeline) buildBundle(r rawFile) (bundle, fault cold.OriginBundle, skip bool) {
	if r.skip {
		return cold.OriginBundle{}, cold.OriginBundle{}, true
	}
	if r.bad {
		return cold.OriginBundle{}, r.fault, false
	}
	lang, ok := adapters.Lookup(r.df.Rel)
	if !ok {
		return cold.OriginBundle{}, cold.OriginBundle{}, true
	}
	fe, xerr := lang.Extract(p.pool, r.df.Rel, r.src)
	if xerr != nil {
		return cold.OriginBundle{}, shared.FaultBundle(r.df, lang.Dialect, r.hash, []string{xerr.Error()}), false
	}

	res := indexer.Index([]model.FileExtract{fe})
	var fns []*model.Function
	for i := range res.Functions {
		fns = append(fns, res.Functions[i])
	}
	imports := res.Imports[fe.Path]

	var entries []cold.EntryRow
	for _, f := range fns {
		entries = append(entries, cold.EntryRowOf(f, fe.Language))
	}
	links, loose := ResolveFileWithAlias(r.df.Rel, fns, imports, fe.Language, p.langs.InternalSourceFor(r.df.Rel))
	var impRows []cold.ImportRow
	for _, imp := range imports {
		impRows = append(impRows, cold.ImportRowOf(r.df.Rel, imp))
	}
	// Collect file-level and per-function receiver bindings.
	var bindRows []cold.RecvBindRow
	for _, b := range fe.RecvBindings {
		bindRows = append(bindRows, cold.RecvBindRowOf(r.df.Rel, "", b))
	}
	for _, f := range fns {
		for _, h := range f.RecvHints {
			bindRows = append(bindRows, cold.RecvBindRowOf(r.df.Rel, f.QualifiedName(), h))
		}
	}
	endpoints, calls := transportRows(fe, fns)
	return cold.OriginBundle{
		Origin: cold.OriginRecord{
			Path:        r.df.Rel,
			Fingerprint: r.hash,
			Dialect:     fe.Language,
			Bytes:       int64(len(r.src)),
			AlteredAt:   r.df.AlteredAt,
		},
		Entries:   entries,
		Links:     links,
		Imports:   impRows,
		LooseEnds: loose,
		RecvBinds: bindRows,
		Endpoints: endpoints,
		Calls:     calls,
	}, cold.OriginBundle{}, false
}
