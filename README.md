# SQLite driver for GORM

Pure-Go (no CGO) SQLite driver for [GORM](https://gorm.io/), powered by [modernc.org/sqlite](https://gitlab.com/cznic/sqlite).

Drop-in replacement for [go-gorm/sqlite](https://github.com/go-gorm/sqlite) (the official CGO-based driver).

## Features

- Pure Go — no C compiler or external libraries required, cross-compiles to any Go-supported platform
- Compatible with the [GORM test suite](https://github.com/go-gorm/gorm/tree/master/tests) (tested on Linux/macOS/Windows)
- [JSON1](https://www.sqlite.org/json1.html), [Math functions](https://www.sqlite.org/lang_mathfunc.html), [FTS5](https://www.sqlite.org/fts5.html), [R-Tree](https://www.sqlite.org/rtree.html) and [Geopoly](https://www.sqlite.org/geopoly.html) enabled by default

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

Parameters are appended to the DSN as a query string:

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
| `journal_mode(WAL)` | Yes | [WAL mode](https://www.sqlite.org/wal.html) — improves concurrent read performance. |
| `busy_timeout(10000)` | Yes | Wait up to N milliseconds when the database is locked, instead of returning `SQLITE_BUSY` immediately. |
| `foreign_keys(1)` | If using FKs | Enable foreign key constraint enforcement (off by default in SQLite). |
| `cache_size(-64000)` | Optional | Set page cache size in KiB (negative value) or pages (positive value). Default is `-2000` (2 MiB). |
| `synchronous(NORMAL)` | With WAL | Reduces fsync calls in WAL mode with minimal durability risk. See [synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous). |

> [!WARNING]
> SQLite only allows one writer at a time — concurrent writes will inevitably encounter `SQLITE_BUSY` ([details](https://github.com/mattn/go-sqlite3/issues/274)). This cannot be fully avoided, but can be mitigated:
>
> 1. Set `busy_timeout` to allow writers to wait instead of failing immediately
> 2. Limit the connection pool to a single connection to reduce lock contention
> 3. Enable WAL mode to allow concurrent reads while writing

```go
dsn := "sqlite.db?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})

sqlDB, _ := db.DB()
sqlDB.SetMaxOpenConns(1)
sqlDB.SetMaxIdleConns(1)
```

> [!NOTE]
> This serializes all database access (reads and writes). If you need concurrent reads, consider using WAL mode with a separate read-only connection pool instead. This approach does not work with `:memory:` databases ([details](https://github.com/mattn/go-sqlite3/issues/204)).

## Testing

Tests run on Linux, macOS, and Windows with the latest two Go releases. The full [GORM test suite](https://github.com/go-gorm/gorm/tree/master/tests) (12k+ cases) is included.

## Credits

- [glebarez/sqlite](https://github.com/glebarez/sqlite)
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite)
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3)
- [gorm.io/gorm](https://github.com/go-gorm/gorm)
