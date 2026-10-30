package adapters

import (
	"context"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/model"
	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
	goresolve "srcinsight/services/resolve/go"
	"srcinsight/services/resolve/ts"
)

// TSState holds the per-root TypeScript config and workspace caches.
type TSState struct {
	Alias *ts.Resolver
	WS    *ts.Workspace
}

// GoState holds the per-root go.mod caches.
type GoState struct {
	Mods *goresolve.ModResolver
}

// Runtime is the per-root state descriptors close over.
type Runtime struct {
	TS *TSState
	Go *GoState
}

// Set binds the language registry to one project root.
type Set struct {
	runtime *Runtime
}

// NewSet builds per-root resolver state; nil root yields stateless resolvers.
func NewSet(root *filesystem.Root) *Set {
	if root == nil {
		return &Set{}
	}
	return &Set{runtime: &Runtime{
		TS: &TSState{Alias: ts.New(root), WS: ts.NewWorkspace(root)},
		Go: &GoState{Mods: goresolve.New(root)},
	}}
}

// For returns the descriptor for lang.
func (s *Set) For(lang model.Language) *Language { return Get(lang) }

// ForFile resolves a path to its language descriptor.
func (s *Set) ForFile(rel string) (*Language, bool) { return Lookup(rel) }

// ResolverFor returns the alias-bound resolver, or stateless when unconfigured.
func (s *Set) ResolverFor(lang model.Language) resolve.ImportResolver {
	l := Get(lang)
	if l == nil || l.Resolver == nil {
		return nil
	}
	var rt *Runtime
	if s != nil {
		rt = s.runtime
	}
	return l.Resolver(rt)
}

// InternalSourceFor returns a per-file internal-source check; nil when no
// language can classify the file.
func (s *Set) InternalSourceFor(file string) func(source string) bool {
	var matchers []func(string) bool
	for _, l := range All() {
		if l.InternalSource == nil {
			continue
		}
		l := l
		var rt *Runtime
		if s != nil {
			rt = s.runtime
		}
		matchers = append(matchers, func(source string) bool {
			return l.InternalSource(rt, file, source)
		})
	}
	if len(matchers) == 0 {
		return nil
	}
	return func(source string) bool {
		for _, m := range matchers {
			if m(source) {
				return true
			}
		}
		return false
	}
}

// InvalidateAll drops resolver caches so edited configs re-read lazily.
func (s *Set) InvalidateAll() {
	if s == nil || s.runtime == nil {
		return
	}
	for _, l := range All() {
		if l.Invalidate != nil {
			l.Invalidate(s.runtime)
		}
	}
}

// Forward runs language passes in registry order after the generic cross pass.
func (s *Set) Forward(ctx context.Context, store resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, imports map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	for _, l := range All() {
		for _, pass := range l.Forward {
			if pass.Enabled != nil && !pass.Enabled() {
				continue
			}
			if err := pass.Run(ctx, store, bundles, pending, imports, lc); err != nil {
				return err
			}
		}
	}
	return nil
}

// ForwardShard runs per-shard language passes over one worker's bundles.
func (s *Set) ForwardShard(ctx context.Context, store resolve.Store, bundles []cold.OriginBundle, pending resolve.Pending, imports map[string][]cold.ImportRow, lc *resolve.LookupCache) error {
	for _, l := range All() {
		for _, pass := range l.ForwardShard {
			if pass.Enabled != nil && !pass.Enabled() {
				continue
			}
			if err := pass.Run(ctx, store, bundles, pending, imports, lc); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecvShard runs whole-batch receiver passes over one worker's index range.
func (s *Set) RecvShard(ctx context.Context, store resolve.Store, bundles []cold.OriginBundle, idx []int, pending resolve.Pending, lc *resolve.LookupCache) error {
	for _, l := range All() {
		for _, pass := range l.RecvShard {
			if pass.Enabled != nil && !pass.Enabled() {
				continue
			}
			if err := pass.Run(ctx, store, bundles, idx, pending, lc); err != nil {
				return err
			}
		}
	}
	return nil
}

// HasRecvShard reports whether any enabled receiver shard pass exists.
func (s *Set) HasRecvShard() bool {
	for _, l := range All() {
		for _, pass := range l.RecvShard {
			if pass.Enabled == nil || pass.Enabled() {
				return true
			}
		}
	}
	return false
}

// Reverse runs language passes for one stage.
func (s *Set) Reverse(ctx context.Context, store resolve.Store, bundles []cold.OriginBundle, stage ReverseStage) error {
	for _, l := range All() {
		for _, pass := range l.Reverse {
			if pass.Stage != stage {
				continue
			}
			if pass.Enabled != nil && !pass.Enabled() {
				continue
			}
			if err := pass.Run(ctx, store, bundles); err != nil {
				return err
			}
		}
	}
	return nil
}
