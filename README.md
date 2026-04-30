# SQLite driver for GORM

Pure-Go (no CGO) SQLite driver for [GORM](https://gorm.io/), powered by [modernc.org/sqlite](https://gitlab.com/cznic/sqlite).

Drop-in replacement for [go-gorm/sqlite](https://github.com/go-gorm/sqlite) (the official CGO-based driver).

## Features

- Pure Go (no C compiler or external libraries required); cross-compiles to any Go-supported platform
- Compatible with the [GORM test suite](https://github.com/go-gorm/gorm/tree/master/tests) (tested on Linux, macOS, and Windows)
- [JSON1](https://www.sqlite.org/json1.html), [Math functions](https://www.sqlite.org/lang_mathfunc.html), [FTS5](https://www.sqlite.org/fts5.html), [R-Tree](https://www.sqlite.org/rtree.html), and [Geopoly](https://www.sqlite.org/geopoly.html) enabled by default

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
dsn := "sqlite.db?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
```

#### Driver parameters

| Parameter | Example | Description |
|-----------|---------|-------------|
| `_pragma` | `_pragma=journal_mode(WAL)` | Execute a `PRAGMA` statement on each new connection; can be specified multiple times. |
| `_txlock` | `_txlock=immediate` | Transaction locking mode: `deferred` (default), `immediate`, or `exclusive`. |
| `_time_format` | `_time_format=sqlite` | How `time.Time` is serialized to TEXT: `sqlite` (default, `YYYY-MM-DD HH:MM:SS.SSS[+-]HH:MM`) or `datetime` (`YYYY-MM-DD HH:MM:SS`). |
| `_time_integer_format` | `_time_integer_format=unix` | Store `time.Time` as INTEGER instead of TEXT: `unix`, `unix_milli`, `unix_micro`, or `unix_nano`. Overrides `_time_format`. |
| `_timezone` | `_timezone=UTC` | Timezone applied when reading and writing time values (parsed by `time.LoadLocation`). |

#### SQLite URI parameters

These are interpreted by SQLite itself, not the driver. Available when the DSN starts with `file:` (the driver opens connections with [`SQLITE_OPEN_URI`](https://www.sqlite.org/c3ref/open.html)).

| Parameter | Example | Description |
|-----------|---------|-------------|
| `mode` | `mode=ro` | Open mode: `ro`, `rw`, `rwc` (default), or `memory`. |
| `cache` | `cache=shared` | Cache mode: `shared` or `private` (default). See [shared cache](https://www.sqlite.org/sharedcache.html). |
| `immutable` | `immutable=1` | Treat the database as read-only and unchanging; enables optimizations for read-only media. |
| `vfs` | `vfs=unix-excl` | Use a specific [VFS](https://www.sqlite.org/vfs.html). |

#### Common pragmas

| Pragma | Recommended | Description |
|--------|-------------|-------------|
| `journal_mode(WAL)` | Yes | Enable [WAL mode](https://www.sqlite.org/wal.html) to improve concurrent read performance. |
| `busy_timeout(N)` | Optional | Wait up to N milliseconds on `SQLITE_BUSY`. Defaults to 5s; override to change. |
| `synchronous(NORMAL)` | With WAL | Reduce fsync calls in WAL mode with minimal durability risk. See [synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous). |
| `cache_size(-64000)` | Optional | Set page cache size in KiB (negative value) or pages (positive value). Defaults to `-2000` (2 MiB). |
| `foreign_keys(1)` | If using FKs | Enable foreign key constraint enforcement (disabled by default in SQLite). |
| `secure_delete(1)` | If sensitive data | Overwrite deleted content with zeros instead of leaving fragments in unused pages. Small I/O overhead. |
| `auto_vacuum(FULL)` | Long-lived DB | Reclaim space automatically: `NONE` (default), `FULL`, or `INCREMENTAL`. Must be set before any tables exist. |
| `case_sensitive_like(1)` | Optional | Make the `LIKE` operator case-sensitive (default is case-insensitive for ASCII). |
| `recursive_triggers(1)` | If using triggers | Allow triggers to fire recursively. |

> [!WARNING]
> SQLite only allows one writer at a time, so concurrent writes will inevitably encounter `SQLITE_BUSY` ([details](https://github.com/mattn/go-sqlite3/issues/274)). This cannot be fully avoided, but can be mitigated:
>
> 1. Tune `busy_timeout` to control how long writers wait before failing (5s by default)
> 2. Limit the connection pool to a single connection to reduce lock contention
> 3. Enable WAL mode to allow concurrent reads while writing

```go
dsn := "sqlite.db?_txlock=immediate&_pragma=journal_mode(WAL)"
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
