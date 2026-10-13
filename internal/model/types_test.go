package model

import "testing"

func TestEntryIDParts(t *testing.T) {
	id := EntryID("typescript:src/auth/service.ts:method:AuthService.login:2")
	if got := id.Language(); got != LangTypeScript {
		t.Fatalf("Language() = %q, want %q", got, LangTypeScript)
	}
	if got := id.FilePath(); got != "src/auth/service.ts" {
		t.Fatalf("FilePath() = %q, want %q", got, "src/auth/service.ts")
	}
	if got := id.Kind(); got != KindMethod {
		t.Fatalf("Kind() = %q, want %q", got, KindMethod)
	}
	if got := id.QualifiedName(); got != "AuthService.login" {
		t.Fatalf("QualifiedName() = %q, want %q", got, "AuthService.login")
	}
	if got := id.Ordinal(); got != 2 {
		t.Fatalf("Ordinal() = %d, want 2", got)
	}
}

func TestEntryIDPartsRejectInvalidFormat(t *testing.T) {
	for _, raw := range []EntryID{
		"",
		"typescript:src/a.ts:fn",
		"typescript:src/a.ts:function:fn",
		"typescript:src/a.ts:function:fn:0",
		"typescript:src/a.ts:function:fn:not-an-ordinal",
		"typescript:src/a.ts::fn:1",
		"typescript:src/a.ts:function::1",
		// Old four-part format (with start line) is no longer valid.
		"typescript:src/a.ts:fn:42",
		"typescript:src:a:b:fn:1",
	} {
		if got := raw.Language(); got != "" {
			t.Errorf("%q Language() = %q, want empty", raw, got)
		}
		if got := raw.FilePath(); got != "" {
			t.Errorf("%q FilePath() = %q, want empty", raw, got)
		}
		if got := raw.Kind(); got != "" {
			t.Errorf("%q Kind() = %q, want empty", raw, got)
		}
		if got := raw.QualifiedName(); got != "" {
			t.Errorf("%q QualifiedName() = %q, want empty", raw, got)
		}
		if got := raw.Ordinal(); got != 0 {
			t.Errorf("%q Ordinal() = %d, want zero", raw, got)
		}
	}
}
