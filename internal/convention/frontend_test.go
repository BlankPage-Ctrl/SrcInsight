package convention

import "testing"

func TestReactHelpers(t *testing.T) {
	for _, p := range []string{"src/Feed.tsx", "src/a.jsx", "src/b.js", "src/c.mjs", "src/d.cjs"} {
		if !IsReactFile(p) {
			t.Errorf("IsReactFile(%q) = false, want true", p)
		}
	}
	if IsReactFile("src/x.ts") || IsReactFile("src/x.vue") {
		t.Errorf("plain .ts/.vue must not count as React files")
	}
	for _, n := range []string{"UserCard", "Feed", "Foo.Bar"} {
		if !IsComponentName(n) {
			t.Errorf("IsComponentName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"div", "span", "helper", "user-card"} {
		if IsComponentName(n) {
			t.Errorf("IsComponentName(%q) = true, want false", n)
		}
	}
	for _, n := range []string{"useState", "useEffect", "useFetch"} {
		if !IsHookName(n) {
			t.Errorf("IsHookName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"use", "user", "helper"} {
		if IsHookName(n) {
			t.Errorf("IsHookName(%q) = true, want false", n)
		}
	}
	if !IsNativeJSXTag("div") || IsNativeJSXTag("UserCard") {
		t.Errorf("IsNativeJSXTag misclassifies div/UserCard")
	}
	if _, ok := Registered("react"); !ok {
		t.Errorf("react framework not registered")
	}
}

func TestVueHelpers(t *testing.T) {
	if !IsVueFile("src/Card.vue") || IsVueFile("src/x.ts") {
		t.Errorf("IsVueFile misclassifies Card.vue/x.ts")
	}
	if got := VueComponentName("src/UserCard.vue"); got != "UserCard" {
		t.Errorf("VueComponentName = %q, want UserCard", got)
	}
	if got := VueComponentName("src/x.ts"); got != "" {
		t.Errorf("VueComponentName(x.ts) = %q, want empty", got)
	}
	cases := map[string]string{
		"user-card": "UserCard", "UserCard": "UserCard", "my-el": "MyEl",
	}
	for in, want := range cases {
		if got := VueKebabToPascal(in); got != want {
			t.Errorf("VueKebabToPascal(%q) = %q, want %q", in, got, want)
		}
	}
	if _, ok := Registered("vue"); !ok {
		t.Errorf("vue framework not registered")
	}
}
