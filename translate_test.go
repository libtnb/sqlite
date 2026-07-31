package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
	moderncsqlite "modernc.org/sqlite"
	moderncsqlitelib "modernc.org/sqlite/lib"
)

type foreignCodeError struct {
	code int
}

func (e foreignCodeError) Error() string { return "unrelated error from another library" }
func (e foreignCodeError) Code() int     { return e.code }

func TestTranslate(t *testing.T) {
	rawSQLiteErr := duplicateKeyError(t)
	wrappedSQLiteErr := fmt.Errorf("inserting article: %w", rawSQLiteErr)
	foreignErr := foreignCodeError{code: moderncsqlitelib.SQLITE_CONSTRAINT_UNIQUE}
	plainErr := errors.New("unrelated error")
	var nilSQLiteErr *moderncsqlite.Error
	typedNilSQLiteErr := error(nilSQLiteErr)

	tests := []struct {
		name     string
		err      error
		want     error
		wantSame bool
	}{
		{
			name: "sqlite error",
			err:  rawSQLiteErr,
			want: gorm.ErrDuplicatedKey,
		},
		{
			name: "wrapped sqlite error",
			err:  wrappedSQLiteErr,
			want: gorm.ErrDuplicatedKey,
		},
		{
			name:     "foreign error with matching code",
			err:      foreignErr,
			wantSame: true,
		},
		{
			name:     "plain error",
			err:      plainErr,
			wantSame: true,
		},
		{
			name:     "typed nil sqlite error",
			err:      typedNilSQLiteErr,
			wantSame: true,
		},
	}

	dialector := Dialector{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dialector.Translate(tt.err)
			if tt.wantSame {
				if got != tt.err {
					t.Errorf("Translate() = %v, want original error %v", got, tt.err)
				}
				return
			}

			if !errors.Is(got, tt.want) {
				t.Errorf("Translate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func duplicateKeyError(t *testing.T) error {
	t.Helper()

	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	if _, err := db.Exec("CREATE TABLE articles (article_number TEXT UNIQUE)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO articles VALUES ('A00000XX')"); err != nil {
		t.Fatalf("insert first article: %v", err)
	}

	_, err = db.Exec("INSERT INTO articles VALUES ('A00000XX')")
	if err == nil {
		t.Fatal("insert duplicate article: expected an error")
	}

	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("insert duplicate article: got %T, want *sqlite.Error", err)
	}
	return err
}
