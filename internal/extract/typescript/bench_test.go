package typescript

import (
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// Extractor benches — TS. Focus Write-side? Extract is the CPU-bound half
// of fresh index (docs/benchmark-zod.md:80-89 extract = 3s vs parse 1.2s).
// Benches isolate Go alloc from CGO parse where possible.

// smallTS: single class + 2 functions, ~30 lines (like avg file in Zod)
const smallTS = `
import { sign } from './jwt';
/** Validates a token. */
export class AuthService {
  async validateToken(token: string): Promise<boolean> {
    return this.decode(token) && sign(token, "s");
  }
  decode(t: string): string { return t; }
}
export function standalone(x: number): number { return x + 1; }
const helper = () => 42;
`

// mediumTS: 8 functions + heritage + calls, ~80 lines
const mediumTS = `
import { Router } from 'express';
import { db } from './db';
import { sign, verify } from './jwt';

export enum Role { ADMIN='admin', USER='user' }
export type RoleName = Role;
export interface Audited { createdAt: string; }
export interface User extends Audited { role: Role; name: string; }

export class UserService implements Audited {
  createdAt: string = "";
  async findById(id: string): Promise<User> {
    const row = db.query("SELECT * FROM users WHERE id=?", id);
    return verify(row) ? row as User : null as any;
  }
  async create(u: User): Promise<string> {
    sign(u.name, "s");
    return db.insert(u);
  }
  getRole(u: User): Role { return u.role; }
}

export function authMiddleware(req: any, res: any, next: any) {
  const token = req.headers.authorization;
  if (!verify(token)) { res.status(401).send(); return; }
  next();
}
export const handler = (router: Router) => {
  router.get("/users/:id", authMiddleware, async (req, res) => {
    const svc = new UserService();
    const u = await svc.findById(req.params.id);
    res.json(u);
  });
};
`

// largeTS: ~200 lines, 30+ defs (stress file, like Zod's biggest)
var largeTS string

func init() {
	var sb strings.Builder
	sb.WriteString(mediumTS)
	for i := 0; i < 20; i++ {
		sb.WriteString(`
export function fn` + string(rune('A'+i%26)) + `(x: number): number {
  sign(String(x), "s");
  verify(String(x));
  return x + ` + string(rune('0'+i%10)) + `;
}
`)
	}
	largeTS = sb.String()
}

func parseTS(src string) *sitter.Tree {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(LanguageForPath("bench.ts")); err != nil {
		panic(err)
	}
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		panic("parse failed")
	}
	return tree
}

func BenchmarkExtract_TS_Small(b *testing.B) {
	src := []byte(smallTS)
	tree := parseTS(smallTS)
	defer tree.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Extract("bench.ts", src, tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtract_TS_Medium(b *testing.B) {
	src := []byte(mediumTS)
	tree := parseTS(mediumTS)
	defer tree.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Extract("bench.ts", src, tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtract_TS_Large(b *testing.B) {
	src := []byte(largeTS)
	tree := parseTS(largeTS)
	defer tree.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Extract("bench.ts", src, tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtract_TS_ParseAndExtract_Small(b *testing.B) {
	src := []byte(smallTS)
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(LanguageForPath("bench.ts")); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree := parser.Parse(src, nil)
		if tree == nil {
			b.Fatal("parse failed")
		}
		if _, err := Extract("bench.ts", src, tree); err != nil {
			b.Fatal(err)
		}
		tree.Close()
	}
}

func BenchmarkExtract_TS_ParseAndExtract_Medium(b *testing.B) {
	src := []byte(mediumTS)
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(LanguageForPath("bench.ts")); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree := parser.Parse(src, nil)
		if tree == nil {
			b.Fatal("parse failed")
		}
		if _, err := Extract("bench.ts", src, tree); err != nil {
			b.Fatal(err)
		}
		tree.Close()
	}
}
