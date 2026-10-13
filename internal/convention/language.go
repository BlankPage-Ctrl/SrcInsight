package convention

import "strings"

// AskFileExts are the only suffixes StripAskExt removes; scoping keeps Class.method intact.
var AskFileExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".py", ".pyi", ".go"}

// StripAskExt drops one real file extension from a token. Case-insensitive,
// single suffix only: "parse.ts" => "parse", "Class.method" untouched.
func StripAskExt(t string) string {
	lower := strings.ToLower(t)
	for _, ext := range AskFileExts {
		if strings.HasSuffix(lower, ext) && len(t) > len(ext)+1 {
			cut := t[:len(t)-len(ext)]
			// A qualifier tail (a.b, a::b, a/b) is a symbol, not a file.
			if strings.HasSuffix(cut, ".") || strings.HasSuffix(cut, ":") || strings.HasSuffix(cut, "/") {
				return t
			}
			return cut
		}
	}
	return t
}
