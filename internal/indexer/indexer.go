// Package indexer assigns stable IDs and container metadata to raw file
// extracts.
package indexer

import (
	"fmt"
	"path/filepath"
	"sort"

	"srcinsight/internal/model"
)

// Result is the pure output of Index: ID-assigned functions with backfilled
// containers, the per-file imports for the resolver, and statistics.
type Result struct {
	Functions        []*model.Function
	Imports          map[string][]model.ImportStmt
	FilesScanned     int
	FunctionsIndexed int
}

// Index assigns stable IDs and backfills file/class containers for every
// function in extracts. It is the only entry point of this package.
func Index(extracts []model.FileExtract) *Result {
	var funcs []*model.Function
	imports := make(map[string][]model.ImportStmt, len(extracts))
	filesScan := 0
	for i := range extracts {
		fe := &extracts[i]
		filesScan++
		imports[fe.Path] = fe.Imports
		assignIDs(fe)
		backfillContainers(fe)
		for j := range fe.Functions {
			funcs = append(funcs, &fe.Functions[j])
		}
	}
	return &Result{
		Functions:        funcs,
		Imports:          imports,
		FilesScanned:     filesScan,
		FunctionsIndexed: len(funcs),
	}
}

// idKey groups definitions sharing one ordinal counter: same kind and qualified name in a file.
type idKey struct {
	kind      model.Kind
	qualified string
}

// assignIDs sorts functions into source order and assigns kind plus ordinal IDs.
// Sorting first keeps ordinals stable regardless of extractor emission order.
func assignIDs(fe *model.FileExtract) {
	sort.SliceStable(fe.Functions, func(i, j int) bool {
		return fe.Functions[i].LineRange.StartLine < fe.Functions[j].LineRange.StartLine
	})
	seen := make(map[idKey]int)
	for j := range fe.Functions {
		f := &fe.Functions[j]
		k := idKey{kind: f.Kind, qualified: f.QualifiedName()}
		seen[k]++
		f.ID = makeID(fe.Language, fe.Path, f.Kind, f.QualifiedName(), seen[k])
	}
}

func backfillContainers(fe *model.FileExtract) {
	fileID := model.EntryID("file:" + fe.Path)
	file := model.Container{Kind: model.ContainerFile, Name: filepath.Base(fe.Path), ID: fileID}

	classIDs := make(map[string]model.EntryID)
	funcIDs := make(map[string]model.EntryID)
	moduleIDs := make(map[string]model.EntryID)
	namespaceIDs := make(map[string]model.EntryID)
	for j := range fe.Functions {
		f := &fe.Functions[j]
		f.File = file
		switch f.Kind {
		case model.KindClassDef:
			if _, ok := classIDs[f.Name]; !ok {
				classIDs[f.Name] = f.ID
			}
		case model.KindFunction, model.KindAsyncFunction, model.KindGenerator, model.KindMethod, model.KindAsyncMethod, model.KindArrow, model.KindAsyncArrow, model.KindConstructor:
			if _, ok := funcIDs[f.QualifiedName()]; !ok {
				// Prefer simple name for container matching, but also qualified for uniqueness
				if _, ok2 := funcIDs[f.Name]; !ok2 {
					funcIDs[f.Name] = f.ID
				}
				funcIDs[f.QualifiedName()] = f.ID
			}
		case model.KindModule:
			if _, ok := moduleIDs[f.Name]; !ok {
				moduleIDs[f.Name] = f.ID
			}
		}
		// namespace/module containers are not currently indexed as nodes (internal_module),
		// but keep map for future.
		if f.Container != nil && f.Container.Kind == model.ContainerNamespace {
			if _, ok := namespaceIDs[f.Container.Name]; !ok {
				// leave empty, no node yet
			}
		}
	}
	for j := range fe.Functions {
		f := &fe.Functions[j]
		if f.Container == nil {
			continue
		}
		switch f.Container.Kind {
		case model.ContainerClass:
			if id, ok := classIDs[f.Container.Name]; ok {
				f.Container.ID = id
			}
		case model.ContainerFunction:
			if id, ok := funcIDs[f.Container.Name]; ok {
				f.Container.ID = id
			} else if id, ok := funcIDs[f.QualifiedName()]; ok {
				f.Container.ID = id
			}
		case model.ContainerModule:
			if id, ok := moduleIDs[f.Container.Name]; ok {
				f.Container.ID = id
			}
		case model.ContainerNamespace:
			if id, ok := namespaceIDs[f.Container.Name]; ok {
				f.Container.ID = id
			}
		}
	}
}

// makeID builds a stable EntryID:
// "<lang>:<relPath>:<kind>:<qualifiedName>:<ordinal>".
func makeID(lang model.Language, relPath string, kind model.Kind, qualifiedName string, ordinal int) model.EntryID {
	return model.EntryID(fmt.Sprintf("%s:%s:%s:%s:%d", lang, relPath, kind, qualifiedName, ordinal))
}
