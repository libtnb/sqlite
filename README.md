# SQLite driver for GORM

A pure-Go SQLite driver for [GORM](https://gorm.io/) — **no CGO, no C compiler, no external dependencies**.

Powered by [modernc.org/sqlite](https://gitlab.com/cznic/sqlite), which transpiles the original SQLite C source into Go.

## Highlights

- **100% pure Go** — no CGO, builds anywhere Go builds (Alpine, scratch containers, GCP, cross-compilation)
- **Passes all GORM tests** — CI runs the [full GORM test suite](https://github.com/go-gorm/gorm/tree/master/tests) on Linux, macOS, and Windows
- **Built-in features** — [JSON1](https://www.sqlite.org/json1.html), [Math functions](https://www.sqlite.org/lang_mathfunc.html) enabled out of the box
- **Drop-in replacement** for [go-gorm/sqlite](https://github.com/go-gorm/sqlite) — just change the import path

## Install

```bash
go get github.com/libtnb/sqlite
```

## Usage

```go
import (
	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
)

db, err := gorm.Open(sqlite.Open("sqlite.db"), &gorm.Config{})
```

### In-memory database

```go
db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
```

### DSN Parameters

Parameters are appended to the DSN as query string:

```go
dsn := "sqlite.db?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
```

| Parameter | Example | Description |
|-----------|---------|-------------|
| `_pragma` | `_pragma=journal_mode(WAL)` | Execute a `PRAGMA` statement on each new connection. Can be specified multiple times. |
| `_txlock` | `_txlock=immediate` | Transaction locking mode. Values: `deferred` (default), `immediate`, `exclusive`. |

Common pragmas:

| Pragma | Recommended | Description |
|--------|-------------|-------------|
| `journal_mode(WAL)` | Yes | [WAL mode](https://www.sqlite.org/wal.html) — significantly improves concurrent read performance. |
| `busy_timeout(10000)` | Yes | Wait up to N milliseconds when the database is locked, instead of returning `SQLITE_BUSY` immediately. |
| `foreign_keys(1)` | If using FKs | Enable foreign key constraint enforcement (off by default in SQLite). |
| `cache_size(-64000)` | Optional | Set page cache size in KiB (negative value) or pages (positive value). Default is `-2000` (2 MiB). |
| `synchronous(NORMAL)` | With WAL | Reduces fsync calls in WAL mode with minimal durability risk. See [synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous). |

## Why not the standard GORM SQLite driver?

The [official GORM SQLite driver](https://github.com/go-gorm/sqlite) relies on [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3), which uses CGO. This means:

- A C compiler must be installed on the build machine
- Compile-time build tags are needed to enable SQLite features (e.g. JSON support)
- Cannot build in minimal containers (golang-alpine, scratch, distroless)
- Cannot build on platforms that disallow GCC execution (e.g. GCP)

This driver eliminates all of these issues by using a pure-Go SQLite implementation.

## Testing

CI runs on every push against the latest two Go releases:

| OS | Go versions |
|----|-------------|
| Linux | stable, oldstable |
| macOS | stable, oldstable |
| Windows | stable, oldstable |

The full [GORM test suite](https://github.com/go-gorm/gorm/tree/master/tests) (12k+ test cases) is executed to ensure complete compatibility.

## Credits

- [glebarez/sqlite](https://github.com/glebarez/sqlite)
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite)
- [gorm.io/gorm](https://github.com/go-gorm/gorm)
