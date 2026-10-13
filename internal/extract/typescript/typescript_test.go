package typescript

import (
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"srcinsight/internal/extract"
	"srcinsight/internal/model"
)

func extractTS(t *testing.T, src string) extract.FileExtract {
	t.Helper()
	parser := sitter.NewParser()
	defer parser.Close()
	setPathLanguage(t, parser, "src/x.ts")
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		t.Fatal("parse failed")
	}
	defer tree.Close()
	fe, err := Extract("src/x.ts", []byte(src), tree)
	if err != nil {
		t.Fatal(err)
	}
	return fe
}

func TestExtractFunctions(t *testing.T) {
	src := `
import { sign } from './jwt';

/** Validates a token. */
export class AuthService {
  async validateToken(token: string): Promise<boolean> {
    return this.decode(token) && sign(token, "s");
  }
  decode(t: string): string { return t; }
}

export function standalone(x: number): number {
  return x + 1;
}

const helper = () => 42;
`
	fe := extractTS(t, src)

	byName := map[string]model.Function{}
	for _, f := range fe.Functions {
		byName[f.Name] = f
	}

	vt, ok := byName["validateToken"]
	if !ok {
		t.Fatalf("validateToken missing; got %v", funcNames(fe))
	}
	if vt.Kind != model.KindAsyncMethod {
		t.Errorf("kind = %s, want async-method", vt.Kind)
	}
	if vt.ContainerName() != "AuthService" {
		t.Errorf("container = %q, want AuthService", vt.ContainerName())
	}
	if vt.File.Kind != model.ContainerFile || vt.File.Name != "x.ts" {
		t.Errorf("file container = %+v", vt.File)
	}
	if vt.Container == nil || vt.Container.Kind != model.ContainerClass {
		t.Errorf("container kind = %+v", vt.Container)
	}
	if c := byName["standalone"].Container; c != nil {
		t.Errorf("standalone container = %+v, want nil", c)
	}
	if !strings.Contains(vt.Signature, "async validateToken(token: string)") {
		t.Errorf("signature = %q", vt.Signature)
	}
	if vt.Doc == "" {
		t.Errorf("expected doc from preceding comment block")
	}

	calls := map[string]bool{}
	for _, c := range vt.Calls {
		calls[c.Name] = true
	}
	if !calls["this.decode"] {
		t.Errorf("missing this.decode call; got %v", calls)
	}
	if !calls["sign"] {
		t.Errorf("missing sign call; got %v", calls)
	}

	if byName["AuthService"].Kind != model.KindClassDef {
		t.Errorf("class kind = %s", byName["AuthService"].Kind)
	}

	found := false
	for _, imp := range fe.Imports {
		if imp.LocalName == "sign" && imp.Source == "./jwt" {
			found = true
		}
	}
	if !found {
		t.Errorf("imports = %+v", fe.Imports)
	}
}

func funcNames(fe extract.FileExtract) []string {
	var out []string
	for _, f := range fe.Functions {
		out = append(out, f.Name)
	}
	return out
}

func TestExtractTypes(t *testing.T) {
	src := `
export enum Role {
  ADMIN = 'admin',
  USER = 'user',
}

export type RoleName = Role;

export interface Audited {
  createdAt: string;
}

export interface HasRole {
  role: Role;
}

export interface UserProfile extends Audited, HasRole {
  nickname: string;
}

export class AdminUser implements UserProfile {
  constructor(public nickname: string) {}
}
`
	fe := extractTS(t, src)

	byName := map[string]model.Function{}
	for _, f := range fe.Functions {
		byName[f.Name] = f
	}

	role, ok := byName["Role"]
	if !ok || role.Kind != model.KindEnum {
		t.Errorf("Role = %+v, want enum node", role)
	}

	roleName, ok := byName["RoleName"]
	if !ok || roleName.Kind != model.KindTypeAlias {
		t.Errorf("RoleName = %+v, want type-alias node", roleName)
	}
	if !strings.Contains(roleName.Signature, "type RoleName") {
		t.Errorf("RoleName signature = %q", roleName.Signature)
	}

	up, ok := byName["UserProfile"]
	if !ok || up.Kind != model.KindInterface {
		t.Fatalf("UserProfile = %+v, want interface node", up)
	}
	gotExtends := map[string]bool{}
	for _, e := range up.Extends {
		gotExtends[e.Name] = true
	}
	if !gotExtends["Audited"] || !gotExtends["HasRole"] {
		t.Errorf("UserProfile extends = %v, want Audited and HasRole", gotExtends)
	}
	if len(up.Implements) != 0 {
		t.Errorf("UserProfile implements = %+v, want none", up.Implements)
	}
	if !strings.Contains(up.Signature, "interface UserProfile extends Audited, HasRole") {
		t.Errorf("UserProfile signature = %q", up.Signature)
	}

	admin, ok := byName["AdminUser"]
	if !ok || admin.Kind != model.KindClassDef {
		t.Fatalf("AdminUser = %+v, want class node", admin)
	}
	if len(admin.Implements) != 1 || admin.Implements[0].Name != "UserProfile" {
		t.Errorf("AdminUser implements = %+v, want [UserProfile]", admin.Implements)
	}

	// Interface / enum bodies must not leak methods or members as functions.
	extra := funcNames(fe)
	want := []string{"Role", "RoleName", "Audited", "HasRole", "UserProfile", "AdminUser", "constructor"}
	for _, w := range want {
		if !contains(extra, w) {
			t.Errorf("missing node %q; got %v", w, extra)
		}
	}
	if len(extra) != len(want) {
		t.Errorf("unexpected extra nodes: %v", extra)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestExtractBranchCond(t *testing.T) {
	fe := extractTS(t, `
export function flow(x: number, err: Error | null): void {
  plain();
  if (x > 0) {
    positive();
  } else {
    nonpositive();
  }
  const v = err == null ? fresh() : stale();
  if (err == null) {
    const f = () => {
      insideArrow();
    };
    void f;
  }
}
`)
	var fn *model.Function
	for i := range fe.Functions {
		if fe.Functions[i].Name == "flow" {
			fn = &fe.Functions[i]
		}
	}
	if fn == nil {
		t.Fatal("flow missing")
	}
	condOf := map[string]string{}
	for _, c := range fn.Calls {
		condOf[c.Name] = c.Cond
	}
	if condOf["plain"] != "" {
		t.Errorf("plain cond = %q, want empty", condOf["plain"])
	}
	if condOf["positive"] != "x > 0" {
		t.Errorf("positive cond = %q", condOf["positive"])
	}
	if condOf["nonpositive"] != "!(x > 0)" {
		t.Errorf("nonpositive cond = %q", condOf["nonpositive"])
	}
	if condOf["fresh"] != "err == null" {
		t.Errorf("fresh cond = %q", condOf["fresh"])
	}
	if condOf["stale"] != "!(err == null)" {
		t.Errorf("stale cond = %q", condOf["stale"])
	}
	if condOf["insideArrow"] != "" {
		t.Errorf("insideArrow cond = %q, want empty (arrow boundary)", condOf["insideArrow"])
	}
}
