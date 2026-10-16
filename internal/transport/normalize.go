package transport

import "srcinsight/internal/convention"

// NormalizeMethod uppercases verbs; empty stays empty so callers can tell unknown apart.
func NormalizeMethod(m string) string { return convention.NormalizeMethod(m) }

// NormalizePath folds :id, {id}, [id], and * segments into {param}.
func NormalizePath(p string) string { return convention.NormalizePath(p) }

// StripURL reduces a fetch URL to its path.
func StripURL(raw string) string { return convention.StripURL(raw) }

// JoinPrefix combines a controller prefix with a method path.
func JoinPrefix(prefix, path string) string { return convention.JoinPrefix(prefix, path) }
