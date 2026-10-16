package services

import (
	"context"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/convention"
	"srcinsight/internal/extract/frontend"
	goextract "srcinsight/internal/extract/go"
	pyextract "srcinsight/internal/extract/python"
	tsextract "srcinsight/internal/extract/typescript"
	"srcinsight/internal/indexer"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
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

	runJobs(ctx, len(files), p.reg.Size(), func(i int) {
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
	runJobs(ctx, len(files), p.reg.Size(), func(i int) {
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
	runJobs(ctx, len(raws), p.reg.Size(), func(i int) {
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
	if _, ok := LanguageOf(df.Rel); !ok {
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
	lang, ok := LanguageOf(r.df.Rel)
	if !ok {
		return cold.OriginBundle{}, cold.OriginBundle{}, true
	}
	fe, xerr := p.extract(r.df.Rel, lang, r.src)
	if xerr != nil {
		return cold.OriginBundle{}, shared.FaultBundle(r.df, lang, r.hash, []string{xerr.Error()}), false
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
	links, loose := ResolveFileWithAlias(r.df.Rel, fns, imports, fe.Language, p.isInternalSource(r.df.Rel))
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

// extract parses src with a pooled parser and runs the language extractor.
func (p *Pipeline) extract(rel string, lang model.Language, src []byte) (model.FileExtract, error) {
	switch lang {
	case model.LangTypeScript:
		if convention.IsVueFile(rel) {
			return p.extractVue(rel, src)
		}
		var fe model.FileExtract
		err := p.reg.ParseTSFile(rel, src, func(tree *sitter.Tree) error {
			var xerr error
			fe, xerr = tsextract.Extract(rel, src, tree)
			return xerr
		})
		if err != nil {
			return model.FileExtract{}, err
		}
		return fe, nil
	case model.LangPython:
		var fe model.FileExtract
		err := p.reg.Parse(lang, src, func(tree *sitter.Tree) error {
			var xerr error
			fe, xerr = pyextract.Extract(rel, src, tree)
			return xerr
		})
		if err != nil {
			return model.FileExtract{}, err
		}
		return fe, nil
	case model.LangGo:
		var fe model.FileExtract
		err := p.reg.Parse(lang, src, func(tree *sitter.Tree) error {
			var xerr error
			fe, xerr = goextract.Extract(rel, src, tree)
			return xerr
		})
		if err != nil {
			return model.FileExtract{}, err
		}
		return fe, nil
	default:
		return model.FileExtract{}, errUnsupported(lang)
	}
}

// Each <script> block parses under a synthetic path so queries match the parser grammar.
func (p *Pipeline) extractVue(rel string, src []byte) (model.FileExtract, error) {
	sfc := frontend.Split(src)
	comp := convention.VueComponentName(rel)
	var fe model.FileExtract
	fe.Path = rel
	fe.Language = model.LangTypeScript
	for _, sb := range sfc.Scripts {
		if len(sb.Content) == 0 {
			continue
		}
		synth := rel + sb.ParserExt
		err := p.reg.ParseTSFile(synth, sb.Content, func(tree *sitter.Tree) error {
			sfe, xerr := tsextract.Extract(synth, sb.Content, tree)
			if xerr != nil {
				return xerr
			}
			frontend.ShiftLines(&sfe, sb.StartLine-1, rel)
			fe.Functions = append(fe.Functions, sfe.Functions...)
			fe.Imports = append(fe.Imports, sfe.Imports...)
			fe.RecvBindings = append(fe.RecvBindings, sfe.RecvBindings...)
			fe.Endpoints = append(fe.Endpoints, sfe.Endpoints...)
			fe.Calls = append(fe.Calls, sfe.Calls...)
			return nil
		})
		if err != nil {
			return model.FileExtract{}, err
		}
	}
	fe.Imports = frontend.DedupeImports(fe.Imports)
	frontend.AttachComponent(&fe, comp)
	refs := templateRefsOf(rel, src)
	if len(refs) > 0 || len(fe.Functions) == 0 {
		if comp != "" {
			fe.Functions = append(fe.Functions, frontend.SyntheticRoot(rel, comp, refs))
		}
	}
	return fe, nil
}

// templateRefsOf reads template bindings through the matching plugin.
// Files without a Templater (plain scripts) yield nothing.
func templateRefsOf(rel string, src []byte) []model.FunctionRef {
	pl, ok := frontend.ForFile(rel)
	if !ok {
		return nil
	}
	tmpl, ok := pl.(frontend.Templater)
	if !ok {
		return nil
	}
	return tmpl.TemplateRefs(rel, src)
}
