package python

import (
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"
	py "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

const smallPY = `
from .util import helper

class Store:
    def get_user(self, token):
        return self._load(token) or helper(token)
    def _load(self, key):
        return key

async def fetch():
    return await get()

def gen_values():
    yield 1
`

const mediumPY = `
from typing import Optional, List
from .models import User, Role
from .db import query, insert
from .auth import sign, verify

class UserService:
    def find_by_id(self, id: str) -> Optional[User]:
        row = query("SELECT * FROM users WHERE id=?", id)
        return verify(row) and row or None
    def create(self, u: User) -> str:
        sign(u.name, "s")
        return insert(u)
    def get_role(self, u: User) -> Role:
        return u.role

def auth_middleware(req, res, next):
    token = req.headers.get("authorization")
    if not verify(token):
        res.status(401).send()
        return
    next()

class Store:
    def get_user(self, token):
        return self._load(token) or helper(token)
    def _load(self, key):
        return key

async def fetch_many(ids: List[str]):
    for i in ids:
        yield await fetch_one(i)

def helper(x): return x
`

var largePY string

func init() {
	var sb strings.Builder
	sb.WriteString(mediumPY)
	for i := 0; i < 20; i++ {
		sb.WriteString(`
def fn` + string(rune('A'+i%26)) + `(x):
    sign(str(x), "s")
    verify(str(x))
    return x + ` + string(rune('0'+i%10)) + `
`)
	}
	largePY = sb.String()
}

func parsePY(src string) *sitter.Tree {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(py.Language())); err != nil {
		panic(err)
	}
	tree := parser.Parse([]byte(src), nil)
	if tree == nil {
		panic("parse failed")
	}
	return tree
}

func BenchmarkExtract_PY_Small(b *testing.B) {
	src := []byte(smallPY)
	tree := parsePY(smallPY)
	defer tree.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Extract("bench.py", src, tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtract_PY_Medium(b *testing.B) {
	src := []byte(mediumPY)
	tree := parsePY(mediumPY)
	defer tree.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Extract("bench.py", src, tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtract_PY_Large(b *testing.B) {
	src := []byte(largePY)
	tree := parsePY(largePY)
	defer tree.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Extract("bench.py", src, tree); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkExtract_PY_ParseAndExtract_Small(b *testing.B) {
	src := []byte(smallPY)
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(py.Language())); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree := parser.Parse(src, nil)
		if tree == nil {
			b.Fatal("parse failed")
		}
		if _, err := Extract("bench.py", src, tree); err != nil {
			b.Fatal(err)
		}
		tree.Close()
	}
}

func BenchmarkExtract_PY_ParseAndExtract_Medium(b *testing.B) {
	src := []byte(mediumPY)
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(py.Language())); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree := parser.Parse(src, nil)
		if tree == nil {
			b.Fatal("parse failed")
		}
		if _, err := Extract("bench.py", src, tree); err != nil {
			b.Fatal(err)
		}
		tree.Close()
	}
}
