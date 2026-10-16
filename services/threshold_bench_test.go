package services

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"srcinsight/internal/filesystem"
	"srcinsight/internal/storage/cold"
)

func testPipelineBench(b *testing.B, dir string) (*Pipeline, string) {
	b.Helper()
	root, err := filesystem.New(dir)
	if err != nil {
		b.Fatal(err)
	}
	vault, err := cold.Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = vault.Close() })
	return newForTest(root, vault), dir
}

func writeChainFiles(b *testing.B, dir string, n int) {
	b.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := range n {
		next := (i + 1) % n
		body := ""
		for c := range 25 {
			body += fmt.Sprintf("  total += f%d(x + %d);\n", next, c)
		}
		src := fmt.Sprintf(`import { f%d } from './a%d';

export function f%d(x: number): number {
  let total = 0;
%s  return total;
}
`, next, next, i, body)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("a%d.ts", i)), []byte(src), 0o644); err != nil {
			b.Fatal(err)
		}
	}
}

func touchAll(b *testing.B, dir string, n, round int) {
	b.Helper()
	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("a%d.ts", i))
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			b.Fatal(err)
		}
		fmt.Fprintf(f, "\n// touch %d\n", round)
		f.Close()
	}
}

// copySubset copies up to limit source files preserving relative paths.
func copySubset(b *testing.B, src, dst string, limit int) int {
	b.Helper()
	n := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || n >= limit {
			return err
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".ts") {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	return n
}

// BenchmarkCalibrate maps the small-vs-bulk crossover across DB and batch sizes.
func BenchmarkCalibrate(b *testing.B) {
	background := os.Getenv("SRCINSIGHT_BENCH_BACKGROUND")
	if background == "" {
		b.Skip("set SRCINSIGHT_BENCH_BACKGROUND to a large source tree")
	}
	only := os.Getenv("SRCINSIGHT_BENCH_ONLY")
	dbSizes := []struct {
		name  string
		files int
	}{
		{"db100", 100},
		{"db250", 250},
		{"dbfull", 100000},
	}
	for _, db := range dbSizes {
		if only != "" && only != db.name {
			continue
		}
		b.Run(db.name, func(b *testing.B) {
			for _, n := range []int{5, 20, 80, 200} {
				b.Run(fmt.Sprintf("batch_%d", n), func(b *testing.B) {
					for _, threshold := range []string{"1", "1000000"} {
						b.Run("threshold_"+threshold, func(b *testing.B) {
							b.Setenv("SRCINSIGHT_BULK_THRESHOLD", threshold)
							b.Setenv("SRCINSIGHT_COMMIT_CHUNKED", "0")
							dir := b.TempDir()
							bg := dir + "/bg"
							seeded := copySubset(b, background, bg, db.files)
							b.ReportMetric(float64(seeded), "seed_files")
							fixDir := bg + "/fixture"
							writeChainFiles(b, fixDir, n)
							p, _ := testPipelineBench(b, dir)
							defer p.Close()
							if _, err := p.FullScan(b.Context(), true); err != nil {
								b.Fatal(err)
							}
							b.ResetTimer()
							for r := range b.N {
								touchAll(b, fixDir, n, r)
								b.StartTimer()
								if _, err := p.Sync(b.Context()); err != nil {
									b.Fatal(err)
								}
								b.StopTimer()
							}
						})
					}
				})
			}
		})
	}
}

// BenchmarkSyncRecipes times the small vs bulk commit recipes for one dirty
// batch sitting in a large pre-indexed tree, which is the threshold tradeoff.
func BenchmarkSyncRecipes(b *testing.B) {
	background := os.Getenv("SRCINSIGHT_BENCH_BACKGROUND")
	if background == "" {
		b.Skip("set SRCINSIGHT_BENCH_BACKGROUND to a large source tree")
	}
	for _, n := range []int{5, 10, 20, 40, 80, 160} {
		b.Run(fmt.Sprintf("files_%d", n), func(b *testing.B) {
			for _, threshold := range []string{"1", "1000000"} {
				b.Run("threshold_"+threshold, func(b *testing.B) {
					b.Setenv("SRCINSIGHT_BULK_THRESHOLD", threshold)
					b.Setenv("SRCINSIGHT_COMMIT_CHUNKED", "0")
					dir := b.TempDir()
					if out, err := exec.Command("cp", "-r", background, dir+"/bg").CombinedOutput(); err != nil {
						b.Fatalf("copy background: %v %s", err, out)
					}
					fixDir := dir + "/bg/fixture"
					writeChainFiles(b, fixDir, n)
					p, _ := testPipelineBench(b, dir)
					defer p.Close()
					if _, err := p.FullScan(b.Context(), true); err != nil {
						b.Fatal(err)
					}
					b.ResetTimer()
					for r := range b.N {
						touchAll(b, fixDir, n, r)
						b.StartTimer()
						if _, err := p.Sync(b.Context()); err != nil {
							b.Fatal(err)
						}
						b.StopTimer()
					}
				})
			}
		})
	}
}
