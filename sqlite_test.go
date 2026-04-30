package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"modernc.org/sqlite"
)

func TestDialector(t *testing.T) {
	// This is the DSN of the in-memory SQLite database for these tests.
	const InMemoryDSN = "file:testdatabase?mode=memory&cache=shared"
	// This is the custom SQLite driver name.
	const CustomDriverName = "my_custom_driver"

	// Register a custom scalar function on the default driver.
	sqlite.MustRegisterFunction("my_custom_function", &sqlite.FunctionImpl{
		NArgs:         0,
		Deterministic: true,
		Scalar: func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return "my-result", nil
		},
	})

	// Obtain the global singleton driver (which carries registered functions)
	// and register it under a custom name.
	tmpDB, err := sql.Open(DriverName, "")
	if err != nil {
		t.Fatalf("failed to open default driver: %v", err)
	}
	sql.Register(CustomDriverName, tmpDB.Driver())
	_ = tmpDB.Close()

	rows := []struct {
		description  string
		dialector    *Dialector
		openSuccess  bool
		query        string
		querySuccess bool
	}{
		{
			description: "Default driver",
			dialector: &Dialector{
				DSN: InMemoryDSN,
			},
			openSuccess:  true,
			query:        "SELECT 1",
			querySuccess: true,
		},
		{
			description: "Explicit default driver",
			dialector: &Dialector{
				DriverName: DriverName,
				DSN:        InMemoryDSN,
			},
			openSuccess:  true,
			query:        "SELECT 1",
			querySuccess: true,
		},
		{
			description: "Bad driver",
			dialector: &Dialector{
				DriverName: "not-a-real-driver",
				DSN:        InMemoryDSN,
			},
			openSuccess: false,
		},
		{
			description: "Explicit default driver, custom function",
			dialector: &Dialector{
				DriverName: DriverName,
				DSN:        InMemoryDSN,
			},
			openSuccess:  true,
			query:        "SELECT my_custom_function()",
			querySuccess: true,
		},
		{
			description: "Custom driver",
			dialector: &Dialector{
				DriverName: CustomDriverName,
				DSN:        InMemoryDSN,
			},
			openSuccess:  true,
			query:        "SELECT 1",
			querySuccess: true,
		},
		{
			description: "Custom driver, custom function",
			dialector: &Dialector{
				DriverName: CustomDriverName,
				DSN:        InMemoryDSN,
			},
			openSuccess:  true,
			query:        "SELECT my_custom_function()",
			querySuccess: true,
		},
	}
	for rowIndex, row := range rows {
		t.Run(fmt.Sprintf("%d/%s", rowIndex, row.description), func(t *testing.T) {
			db, err := gorm.Open(row.dialector, &gorm.Config{})
			if !row.openSuccess {
				if err == nil {
					t.Errorf("Expected Open to fail.")
				}
				return
			}

			if err != nil {
				t.Errorf("Expected Open to succeed; got error: %v", err)
			}
			if db == nil {
				t.Errorf("Expected db to be non-nil.")
			}
			if row.query != "" {
				err = db.Exec(row.query).Error
				if !row.querySuccess {
					if err == nil {
						t.Errorf("Expected query to fail.")
					}
					return
				}

				if err != nil {
					t.Errorf("Expected query to succeed; got error: %v", err)
				}
			}
		})
	}
}

func TestInjectDSNParams(t *testing.T) {
	allDefaults := []string{
		"_texttotime=1",
		"_inttotime=1",
		"_time_format=sqlite",
		"_pragma=busy_timeout(5000)",
	}
	tests := []struct {
		name    string
		in      string
		exact   string   // when set, the result must equal this exactly
		want    []string // each must be a substring of the result
		notWant []string // each must NOT be a substring of the result
	}{
		// --- happy path ---
		{
			name:  ":memory: gets all defaults",
			in:    ":memory:",
			exact: ":memory:?_texttotime=1&_inttotime=1&_time_format=sqlite&_pragma=busy_timeout(5000)",
		},
		{
			name:  "file: URI gets all defaults",
			in:    "file::memory:",
			exact: "file::memory:?_texttotime=1&_inttotime=1&_time_format=sqlite&_pragma=busy_timeout(5000)",
		},
		{
			name:  "plain path gets all defaults",
			in:    "test.db",
			exact: "test.db?_texttotime=1&_inttotime=1&_time_format=sqlite&_pragma=busy_timeout(5000)",
		},
		{
			name:  "existing query is preserved verbatim",
			in:    "test.db?cache=shared",
			exact: "test.db?cache=shared&_texttotime=1&_inttotime=1&_time_format=sqlite&_pragma=busy_timeout(5000)",
		},

		// --- empty / no-path DSN ---
		{
			name:  "empty DSN is left untouched (modernc cannot parse '?' at index 0)",
			in:    "",
			exact: "",
		},

		// --- trailing punctuation ---
		{
			name:  "trailing ? with empty query",
			in:    "test.db?",
			exact: "test.db?_texttotime=1&_inttotime=1&_time_format=sqlite&_pragma=busy_timeout(5000)",
		},
		{
			name:  "trailing & is preserved (still parseable by url.ParseQuery)",
			in:    "test.db?cache=shared&",
			exact: "test.db?cache=shared&&_texttotime=1&_inttotime=1&_time_format=sqlite&_pragma=busy_timeout(5000)",
		},

		// --- substring-in-path safety: keys/values appearing in the path
		// must NOT trick the heuristic into skipping a default. ---
		{
			name: "path containing '_texttotime' substring still gets _texttotime injected",
			in:   "file:/tmp/_texttotime/x.db",
			want: allDefaults,
		},
		{
			name: "path containing 'busy_timeout' substring still gets busy_timeout injected",
			in:   "file:/tmp/busy_timeout/x.db",
			want: allDefaults,
		},
		{
			name: "path containing '_time_format' substring still gets _time_format injected",
			in:   "file:/var/_time_format.db",
			want: allDefaults,
		},

		// --- user override wins, key-by-key ---
		{
			name:    "user _time_format=datetime wins",
			in:      "test.db?_time_format=datetime",
			want:    []string{"_time_format=datetime", "_texttotime=1", "_inttotime=1", "_pragma=busy_timeout(5000)"},
			notWant: []string{"_time_format=sqlite"},
		},
		{
			name:    "user _texttotime=0 wins",
			in:      "test.db?_texttotime=0",
			want:    []string{"_texttotime=0", "_inttotime=1", "_time_format=sqlite", "_pragma=busy_timeout(5000)"},
			notWant: []string{"_texttotime=1"},
		},
		{
			name:    "user _inttotime=0 wins",
			in:      "test.db?_inttotime=0",
			want:    []string{"_inttotime=0", "_texttotime=1", "_time_format=sqlite", "_pragma=busy_timeout(5000)"},
			notWant: []string{"_inttotime=1"},
		},

		// --- busy_timeout: detected across pragma syntaxes / casings ---
		{
			name:    "user busy_timeout via function syntax wins",
			in:      "test.db?_pragma=busy_timeout(10000)",
			want:    []string{"_pragma=busy_timeout(10000)", "_texttotime=1", "_inttotime=1", "_time_format=sqlite"},
			notWant: []string{"busy_timeout(5000)"},
		},
		{
			name:    "user busy_timeout via URL-encoded '=' syntax wins",
			in:      "test.db?_pragma=busy_timeout%3D10000",
			want:    []string{"_pragma=busy_timeout%3D10000"},
			notWant: []string{"busy_timeout(5000)"},
		},
		{
			name:    "BUSY_TIMEOUT (uppercase) is detected case-insensitively",
			in:      "test.db?_pragma=BUSY_TIMEOUT(10000)",
			want:    []string{"_pragma=BUSY_TIMEOUT(10000)"},
			notWant: []string{"busy_timeout(5000)"},
		},
		{
			name:    "busy_timeout buried among multiple _pragma values is detected",
			in:      "test.db?_pragma=foreign_keys(on)&_pragma=busy_timeout(7500)",
			want:    []string{"_pragma=busy_timeout(7500)"},
			notWant: []string{"busy_timeout(5000)"},
		},
		{
			name: "user has unrelated _pragma — busy_timeout still injected",
			in:   "test.db?_pragma=foreign_keys(on)",
			want: []string{"_pragma=foreign_keys(on)", "_pragma=busy_timeout(5000)", "_texttotime=1", "_inttotime=1", "_time_format=sqlite"},
		},

		// --- key matching is exact, not substring ---
		{
			name: "param named '_texttotime_extra' does not satisfy '_texttotime' check",
			in:   "test.db?_texttotime_extra=1",
			want: allDefaults,
		},

		// --- malformed query: bail out, don't corrupt user input ---
		{
			name:  "malformed query is left untouched",
			in:    "test.db?key=%ZZ",
			exact: "test.db?key=%ZZ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectDSNParams(tt.in)
			if tt.exact != "" {
				if got != tt.exact {
					t.Errorf("injectDSNParams(%q)\n  got:  %q\n  want: %q", tt.in, got, tt.exact)
				}
				return
			}
			for _, s := range tt.want {
				if !strings.Contains(got, s) {
					t.Errorf("injectDSNParams(%q) = %q; want to contain %q", tt.in, got, s)
				}
			}
			for _, s := range tt.notWant {
				if strings.Contains(got, s) {
					t.Errorf("injectDSNParams(%q) = %q; must not contain %q", tt.in, got, s)
				}
			}
		})
	}
}

func TestSplitDSN(t *testing.T) {
	tests := []struct {
		in              string
		wantPath, wantQ string
	}{
		{"", "", ""},
		{":memory:", ":memory:", ""},
		{"file::memory:", "file::memory:", ""},
		{"test.db?", "test.db", ""},
		{"test.db?cache=shared", "test.db", "cache=shared"},
		{"test.db?cache=shared&_pragma=foo", "test.db", "cache=shared&_pragma=foo"},
		{"file:/tmp/x.db?cache=shared", "file:/tmp/x.db", "cache=shared"},
		{"?onlyquery=1", "", "onlyquery=1"}, // pathological — modernc rejects it too
		{"a?b?c", "a", "b?c"},               // first '?' wins
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			gotPath, gotQ := splitDSN(tt.in)
			if gotPath != tt.wantPath || gotQ != tt.wantQ {
				t.Errorf("splitDSN(%q) = (%q, %q); want (%q, %q)", tt.in, gotPath, gotQ, tt.wantPath, tt.wantQ)
			}
		})
	}
}

// Issue #15
func TestTimeWriteFormatNoMonotonic(t *testing.T) {
	db, err := sql.Open(DriverName, injectDSNParams("file::memory:?cache=shared"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec("CREATE TABLE ts_test (ts TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// time.Now() carries a monotonic clock reading. Without _time_format=sqlite,
	// the driver would persist this via t.String(), embedding "m=+...".
	cases := []struct {
		name string
		t    time.Time
	}{
		{"local now", time.Now()},
		{"utc now", time.Now().UTC()},
		{"fixed +0200", time.Date(2026, 4, 29, 20, 36, 48, 97619000, time.FixedZone("CEST", 2*60*60))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec("INSERT INTO ts_test (ts) VALUES (?)", tc.t); err != nil {
				t.Fatalf("INSERT: %v", err)
			}
			var raw string
			if err := db.QueryRow("SELECT ts FROM ts_test ORDER BY rowid DESC LIMIT 1").Scan(&raw); err != nil {
				t.Fatalf("SELECT: %v", err)
			}

			for _, marker := range []string{"m=+", "m=-", " MST", " UTC", " CEST"} {
				if strings.Contains(raw, marker) {
					t.Errorf("stored time %q contains %q — driver fell back to t.String()", raw, marker)
				}
			}
			if _, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", raw); err != nil {
				t.Errorf("stored time %q does not match SQLiteTimestampFormats[0]: %v", raw, err)
			}
		})
	}
}

// TestDefaultBusyTimeout verifies the connection comes up with a 5s busy
// timeout by default (matching mattn/go-sqlite3), and that a user-supplied
// _pragma=busy_timeout overrides the default — across various DSN shapes.
func TestDefaultBusyTimeout(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want int
	}{
		{"default with file: URI", "file::memory:", 5000},
		{"default with :memory:", ":memory:", 5000},
		{"default with existing _pragma", "file::memory:?_pragma=foreign_keys(on)", 5000},
		{"user override via function syntax", "file::memory:?_pragma=busy_timeout(7500)", 7500},
		{"user override via equals syntax", "file::memory:?_pragma=busy_timeout%3D7500", 7500},
		{"user override case-insensitive", "file::memory:?_pragma=BUSY_TIMEOUT(7500)", 7500},
		{"user override buried among other pragmas", "file::memory:?_pragma=foreign_keys(on)&_pragma=busy_timeout(7500)", 7500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sql.Open(DriverName, injectDSNParams(tt.dsn))
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer func() { _ = db.Close() }()

			var got int
			if err := db.QueryRow("PRAGMA busy_timeout").Scan(&got); err != nil {
				t.Fatalf("PRAGMA busy_timeout: %v", err)
			}
			if got != tt.want {
				t.Errorf("busy_timeout = %d; want %d", got, tt.want)
			}
		})
	}
}
