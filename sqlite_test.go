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
	tests := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{
			name: "empty DSN gets all defaults",
			in:   "",
			want: []string{"_texttotime=1", "_inttotime=1", "_time_format=sqlite"},
		},
		{
			name: "DSN without query string gets ? separator",
			in:   "test.db",
			want: []string{"test.db?", "_texttotime=1", "_inttotime=1", "_time_format=sqlite"},
		},
		{
			name: "DSN with existing query is preserved",
			in:   "test.db?cache=shared",
			want: []string{"cache=shared", "_texttotime=1", "_inttotime=1", "_time_format=sqlite"},
		},
		{
			name:    "user-supplied _time_format wins over default",
			in:      "test.db?_time_format=datetime",
			want:    []string{"_time_format=datetime", "_texttotime=1", "_inttotime=1"},
			notWant: []string{"_time_format=sqlite"},
		},
		{
			name:    "user-supplied _texttotime wins over default",
			in:      "test.db?_texttotime=0",
			want:    []string{"_texttotime=0", "_inttotime=1", "_time_format=sqlite"},
			notWant: []string{"_texttotime=1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectDSNParams(tt.in)
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
