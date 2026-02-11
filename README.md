# Pure-Go SQLite driver for GORM

Pure-go (without cgo) implementation of SQLite driver for [GORM](https://gorm.io/)<br><br>
This driver has SQLite embedded, you don't need to install one separately.

This is a fork of [glebarez/sqlite](https://github.com/glebarez/sqlite). The original repository has not been updated for a long time.

# Usage

```go
import (
	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
)

db, err := gorm.Open(sqlite.Open("sqlite.db"), &gorm.Config{})
```

### In-memory DB example

```go
db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
```

### Foreign-key constraint activation

Foreign-key constraint is disabled by default in SQLite. To activate it, use connection URL parameter:

```go
db, err := gorm.Open(sqlite.Open(":memory:?_pragma=foreign_keys(1)"), &gorm.Config{})
```

More info: [https://www.sqlite.org/foreignkeys.html](https://www.sqlite.org/foreignkeys.html)

# FAQ

## How is this better than standard GORM SQLite driver?

The [standard GORM driver for SQLite](https://github.com/go-gorm/sqlite) has one major drawback: it is based on
a [Go-bindings of SQLite C-source](https://github.com/mattn/go-sqlite3) (this is called [cgo](https://go.dev/blog/cgo)).
This fact imposes following restrictions on Go developers:

- to build and run your code, you will need a C compiler installed on a machine
- SQLite has many features that need to be enabled at compile time (
  e.g. [json support](https://www.sqlite.org/json1.html)). If you plan to use those, you will have to include proper
  build tags for every ```go``` command to work properly (```go run```, ```go test```, etc.).
- Because of C-compiler requirement, you can't build your Go code inside tiny stripped containers like (golang-alpine)
- Building on GCP is not possible because Google Cloud Platform does not allow gcc to be executed.

**Instead**, this driver is based on pure-Go implementation of SQLite (https://gitlab.com/cznic/sqlite), which is
basically an original SQLite C-source AST, translated into Go! So, you may be sure you're using the original SQLite
implementation under the hood.

## Is this tested good ?

Yes, The CI pipeline of this driver employs [whole test base](https://github.com/go-gorm/gorm/tree/master/tests) of
GORM, which includes more than **12k** tests (see badge on the page-top). Testing is run against latest major releases
of Go:

- 1.25
- 1.24

In following environments:

- Linux
- Windows
- MacOS

## Included features

- JSON1 (https://www.sqlite.org/json1.html)
- Math functions (https://www.sqlite.org/lang_mathfunc.html)
