package convention

// TestCallees are call names taking a titled case like test("name", fn).
// Shape-based, not framework-based, so one table covers vitest, jest, mocha, and others.
var TestCallees = map[string]struct{}{
	"test": {}, "it": {}, "specify": {},
	"each": {}, "skip": {}, "only": {}, "todo": {}, "failing": {},
}

// MaxTestTitle caps a captured title: longer is a pathological literal,
// and the index stores labels, not prose.
const MaxTestTitle = 160

// IsTestCalleeName reports test-style callees: bare names or members ending in one (test.each, it.only).
func IsTestCalleeName(name string) bool {
	_, ok := TestCallees[name]
	return ok
}
