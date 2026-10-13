package ts

import (
	"testing"
)

// BaseUrl layout mirrors the hoppscotch backend: one package tsconfig with
// baseUrl "./" and cross-module imports spelled `src/...`.
func baseURLFixture() map[string]string {
	return map[string]string{
		"pkg/tsconfig.json": `{"compilerOptions": {"baseUrl": "./"}}`,
		"pkg/src/user/user.service.ts": `export class UserService {
  getUser(): number { return 1; }
}
`,
		"pkg/src/profile/profile.service.ts": `import { UserService } from 'src/user/user.service';
export class ProfileService {
  constructor(private users: UserService) {}
  load(): number { return this.users.getUser(); }
}
`,
	}
}

func TestBaseURLCandidates(t *testing.T) {
	r := newResolver(t, baseURLFixture())
	tsr := TSResolver{Alias: r}
	got := tsr.CandidatePaths("pkg/src/profile/profile.service.ts", "src/user/user.service")
	found := false
	for _, c := range got {
		if c == "pkg/src/user/user.service.ts" {
			found = true
		}
	}
	if !found {
		t.Fatalf("baseUrl candidates = %+v, want pkg/src/user/user.service.ts", got)
	}
	// Bare packages still yield unverified candidates (downstream gates
	// drop misses); paths behavior is untouched.
	if got := tsr.CandidatePaths("pkg/src/profile/profile.service.ts", "fp-ts"); len(got) == 0 {
		t.Fatal("bare package should still yield unverified candidates")
	}
}

func TestBaseURLInternalSource(t *testing.T) {
	r := newResolver(t, baseURLFixture())
	if !r.IsInternalSource("pkg/src/profile/profile.service.ts", "src/user/user.service") {
		t.Fatal("existing baseUrl target must be internal")
	}
	if r.IsInternalSource("pkg/src/profile/profile.service.ts", "src/nope/missing") {
		t.Fatal("missing baseUrl target must not be internal")
	}
	if r.IsInternalSource("pkg/src/profile/profile.service.ts", "fp-ts") {
		t.Fatal("bare package must not be internal")
	}
	if r.IsInternalSource("pkg/src/profile/profile.service.ts", "fp-ts/Either") {
		t.Fatal("deep bare import must not be internal")
	}
}

func TestBaseURLBase(t *testing.T) {
	r := newResolver(t, map[string]string{
		"a/tsconfig.json": `{"compilerOptions": {"baseUrl": "./"}}`,
		"a/src/x.ts":      `export const x = 1;`,
		"b/tsconfig.json": `{"compilerOptions": {"baseUrl": "nested"}}`,
		"c/tsconfig.json": `{"compilerOptions": {"baseUrl": "/abs"}}`,
		"nope/src/x.ts":   `export const x = 1;`,
	})
	if got := r.BaseURLBase("a/src/app.ts"); got != "a" {
		t.Fatalf("BaseURLBase a = %q, want a", got)
	}
	if got := r.BaseURLBase("b/src/app.ts"); got != "b/nested" {
		t.Fatalf("BaseURLBase b = %q, want b/nested", got)
	}
	if got := r.BaseURLBase("c/src/app.ts"); got != "" {
		t.Fatalf("BaseURLBase absolute = %q, want empty", got)
	}
	if got := r.BaseURLBase("nope/src/app.ts"); got != "" {
		t.Fatalf("BaseURLBase without config = %q, want empty", got)
	}
	// Nil-root resolvers keep legacy behaviour (no panic, no base).
	var nilR *Resolver
	if got := nilR.BaseURLBase("a/src/app.ts"); got != "" {
		t.Fatalf("nil BaseURLBase = %q, want empty", got)
	}
	if nilR.BaseURLExists("a/src/app.ts", "src/x", nil) {
		t.Fatal("nil BaseURLExists must be false")
	}
}
