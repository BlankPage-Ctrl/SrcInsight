package adapters

import (
	"context"

	"srcinsight/internal/storage/cold"
	"srcinsight/services/resolve"
)

// ForwardPass runs after the language-agnostic cross pass.
type ForwardPass struct {
	Name    string
	Enabled func() bool
	Run     func(context.Context, resolve.Store, []cold.OriginBundle, resolve.Pending, map[string][]cold.ImportRow, *resolve.LookupCache) error
}

// ShardPass runs over one worker's bundle index range.
type ShardPass struct {
	Name    string
	Enabled func() bool
	Run     func(context.Context, resolve.Store, []cold.OriginBundle, []int, resolve.Pending, *resolve.LookupCache) error
}

// ReverseStage selects which reverse batch a call site wants.
type ReverseStage int

const (
	// ReverseCross promotes other files' loose ends for fresh entries.
	ReverseCross ReverseStage = iota
	// ReverseLanguage runs language extras such as TypeScript DI.
	ReverseLanguage
	// ReverseRecv runs receiver promotion.
	ReverseRecv
)

// ReversePass promotes loose ends after new bundles are indexed.
type ReversePass struct {
	Name    string
	Stage   ReverseStage
	Enabled func() bool
	Run     func(context.Context, resolve.Store, []cold.OriginBundle) error
}
