# SrcInsight

Source Insight is a Program Structure Weaver; it reads the codebase or software project that has been designated for indexing.

It is currently utilized by [Codryn](https://github.com/BlankPage-Ctrl/Codryn) as a component to assist the AI ​​in locating specific functions while prioritizing speed of comprehension.
> [!NOTE]
> SrcInsight is currently in the **Alpha stage**, and various aspects are being developed to ensure stability.

### Language Support

Currently indexed:

- Go
- TypeScript
- Python

Relationships captured include calls and called-by, extends and implements, parameter and return types, constant value usage, and per-file imports.

### Things you should know
- SrcInsight Pre-builds binary are only available for Windows and Linux (AMD64 architecture), **For now**.
- SrcInsight will attempt to keep itself up to date with changes to your codebase or software project.
- SrcInsight does not send the code in your codebase or software project to any network; it remains entirely on your device.
- SrcInsight supports frameworks such as NestJS, React, and Vue, with others coming later.


### Benchmark Snapshot

Summary below, measured on a Zod codebase (515 TypeScript files, around 100K lines).

Index output for that corpus: 21,215 entries and 61,207 resolved links, stored in an 81.3 MB database.

| Operation | Result |
|---|---|
| Fresh index (3 runs avg) | 18.93s, peak RSS around 266 MB |
| Second index, no changes | 0.37s |
| Sync, 1 file changed | 0.53s |
| Sync, no-op | 0.36s |
| Incremental scoped vs full | 0.70s vs 2.00s |
| Graph query | around 10 ms |
| Serve idle memory | around 24 MB |

Machine: Intel Core i7 M 620 (4 threads), 7.8 GiB RAM, Windows 10, Go 1.27.1, 3 parse workers.

> [!IMPORTANT]
> The data above represents the most recent benchmark results; the figures may be slower than those from the benchmark conducted on [October 4, 2026](scripts/result/benchmark-2026-10-04.md), on Windows (alongside Linux) on the same machine.

## Status and Scope

SrcInsight focuses on structure, not on type checking, linting, or execution. External and runtime calls are labeled rather than fully resolved, and ambiguous dynamic patterns are left unresolved rather than guessed.

## License

[MIT](./LICENSE).
