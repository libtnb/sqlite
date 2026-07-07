package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openTestDB opens a file-backed database in a per-test temp dir so that
// connection-pool behavior matches real usage (":memory:" would give every
// pooled connection its own database).
func openTestDB(t *testing.T, dsnParams string) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if dsnParams != "" {
		dsn += "?" + dsnParams
	}
	db, err := gorm.Open(Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	closeTestDB(t, db)
	return db
}

// closeTestDB closes the pool on test cleanup; Windows cannot remove the
// t.TempDir() database file while connections still hold it open.
func closeTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
}

func tableDDL(t *testing.T, db *gorm.DB, table string) string {
	t.Helper()
	var ddl string
	db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl)
	return ddl
}

// DropColumn and AlterColumn must accept a table-name string like the other
// GORM dialects do (stmt.Schema is nil then) instead of panicking.
func TestMigratorStringTableName(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.Exec("CREATE TABLE `s1` (`id` integer, `b` text)").Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Migrator().DropColumn("s1", "b"); err != nil {
		t.Errorf("DropColumn with string value: %v", err)
	}
	if db.Migrator().HasColumn("s1", "b") {
		t.Error("column b still present after DropColumn")
	}

	// AlterColumn needs the model schema to build the new column type; a
	// string value must produce a regular error, not a panic.
	if err := db.Migrator().AlterColumn("s1", "id"); err == nil {
		t.Error("AlterColumn with string value: expected an error, got nil")
	}
}

type FKParent struct {
	ID   int
	Name string
	Note string
}

type FKChild struct {
	ID       int
	ParentID int
	Parent   FKParent `gorm:"foreignKey:ParentID"`
}

// DropColumn rebuilds the table via DROP TABLE; with foreign_keys=ON and
// referencing rows in a child table that used to fail (go-gorm/sqlite #229).
func TestDropColumnWithForeignKeysEnabled(t *testing.T) {
	db := openTestDB(t, "_pragma=foreign_keys(1)")
	if err := db.AutoMigrate(&FKParent{}, &FKChild{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&FKParent{ID: 1, Name: "p", Note: "n"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&FKChild{ID: 1, ParentID: 1}).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Migrator().DropColumn(&FKParent{}, "note"); err != nil {
		t.Fatalf("DropColumn with foreign_keys=ON: %v", err)
	}

	var childCount int
	db.Raw("SELECT count(*) FROM fk_children").Scan(&childCount)
	if childCount != 1 {
		t.Errorf("child rows after DropColumn = %d, want 1", childCount)
	}
	var fkEnabled int
	db.Raw("PRAGMA foreign_keys").Scan(&fkEnabled)
	if fkEnabled != 1 {
		t.Errorf("foreign_keys not restored, got %d", fkEnabled)
	}
}

// HasColumn must not report a column as present just because its name appears
// as a substring elsewhere in the DDL.
func TestHasColumnNoFalsePositive(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.Exec("CREATE TABLE plaincols (id integer, first_name text)").Error; err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn("plaincols", "name") {
		t.Error("unquoted DDL: first_name matched HasColumn(name)")
	}
	if !db.Migrator().HasColumn("plaincols", "first_name") {
		t.Error("HasColumn(first_name) = false, want true")
	}

	if err := db.Exec("CREATE TABLE `defv` (`id` integer, `cfg` text DEFAULT 'name value')").Error; err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn("defv", "name") {
		t.Error("DEFAULT 'name value' matched HasColumn(name)")
	}
}

type PlainCol struct {
	ID   int
	Name string
}

func (PlainCol) TableName() string { return "plain_col" }

// DropColumn on a table whose DDL uses unquoted column names used to return
// nil without dropping anything.
func TestDropColumnUnquotedDDL(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.Exec("CREATE TABLE plain_col (id integer, name text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PlainCol{}, "name"); err != nil {
		t.Fatalf("DropColumn: %v", err)
	}
	if db.Migrator().HasColumn("plain_col", "name") {
		t.Error("column name still present after DropColumn on unquoted DDL")
	}

	// dropping a column that does not exist must report an error, not succeed silently
	if err := db.Migrator().DropColumn(&PlainCol{}, "missing"); err == nil {
		t.Error("DropColumn(missing) = nil, want error")
	}
}

type IdxTable struct {
	ID int
	A  string
	B  string
}

func (IdxTable) TableName() string { return "idx_table" }

// recreateTable drops the old table together with its indexes and triggers;
// they must be recreated on the rebuilt table. Indexes on a dropped column
// are the exception: they can no longer apply.
func TestRecreateTablePreservesIndexesAndTriggers(t *testing.T) {
	db := openTestDB(t, "")
	stmts := []string{
		"CREATE TABLE `idx_table` (`id` integer, `a` text, `b` text)",
		"CREATE INDEX `idx_keep` ON `idx_table`(`a`)",
		"CREATE INDEX `idx_gone` ON `idx_table`(`b`)",
		"CREATE TABLE `audit` (`msg` text)",
		"CREATE TRIGGER `trg_keep` AFTER INSERT ON `idx_table` BEGIN INSERT INTO `audit`(`msg`) VALUES ('x'); END",
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := db.Migrator().DropColumn(&IdxTable{}, "b"); err != nil {
		t.Fatalf("DropColumn: %v", err)
	}

	var names []string
	db.Raw("SELECT name FROM sqlite_master WHERE tbl_name = 'idx_table' AND type IN ('index','trigger')").Scan(&names)
	got := strings.Join(names, ",")
	if !strings.Contains(got, "idx_keep") {
		t.Errorf("index idx_keep lost after DropColumn, remaining: %v", names)
	}
	if !strings.Contains(got, "trg_keep") {
		t.Errorf("trigger trg_keep lost after DropColumn, remaining: %v", names)
	}
	if strings.Contains(got, "idx_gone") {
		t.Errorf("index idx_gone references the dropped column and must not survive, remaining: %v", names)
	}

	// trigger still works on the rebuilt table
	if err := db.Exec("INSERT INTO `idx_table`(`id`,`a`) VALUES (1,'v')").Error; err != nil {
		t.Fatal(err)
	}
	var auditCount int
	db.Raw("SELECT count(*) FROM audit").Scan(&auditCount)
	if auditCount != 1 {
		t.Errorf("trigger did not fire after rebuild, audit rows = %d", auditCount)
	}
}

type WithoutRowid struct {
	ID string `gorm:"primaryKey"`
	A  string
	B  string
}

func (WithoutRowid) TableName() string { return "wr_table" }

// Table options after the column list (WITHOUT ROWID, STRICT) must survive a
// table rebuild.
func TestRecreateTablePreservesTableOptions(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.Exec("CREATE TABLE `wr_table` (`id` text PRIMARY KEY, `a` text, `b` text) WITHOUT ROWID, STRICT").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&WithoutRowid{}, "b"); err != nil {
		t.Fatalf("DropColumn: %v", err)
	}

	ddl := tableDDL(t, db, "wr_table")
	if !strings.Contains(ddl, "WITHOUT ROWID") {
		t.Errorf("WITHOUT ROWID lost after rebuild: %s", ddl)
	}
	if !strings.Contains(ddl, "STRICT") {
		t.Errorf("STRICT lost after rebuild: %s", ddl)
	}
}

type ViewedTable struct {
	ID int
	A  string
	B  string
}

func (ViewedTable) TableName() string { return "viewed" }

// Rebuilding a table that a view references used to fail with "error in view
// ...: no such table" (go-gorm/sqlite #225).
func TestRecreateTableWithView(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.Exec("CREATE TABLE `viewed` (`id` integer, `a` text, `b` text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE VIEW `viewed_v` AS SELECT `a` FROM `viewed`").Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Migrator().DropColumn(&ViewedTable{}, "b"); err != nil {
		t.Fatalf("DropColumn with referencing view: %v", err)
	}

	var n int
	if err := db.Raw("SELECT count(*) FROM `viewed_v`").Scan(&n).Error; err != nil {
		t.Errorf("view is broken after rebuild: %v", err)
	}
}

func TestRecreateTableTableNotFound(t *testing.T) {
	db := openTestDB(t, "")
	err := db.Migrator().DropColumn("does_not_exist", "col")
	if err == nil {
		t.Fatal("DropColumn on missing table: expected error")
	}
	if !strings.Contains(err.Error(), "table not found") {
		t.Errorf("error should mention the missing table, got: %v", err)
	}
}

type SeqTable struct {
	ID int `gorm:"primaryKey;autoIncrement"`
}

func (SeqTable) TableName() string { return "seq_table" }

// GetTables must not report SQLite internal tables such as sqlite_sequence.
func TestGetTablesExcludesInternal(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.AutoMigrate(&SeqTable{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&SeqTable{}).Error; err != nil {
		t.Fatal(err)
	}

	tables, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		if strings.HasPrefix(tb, "sqlite_") {
			t.Errorf("GetTables returned internal table %q", tb)
		}
	}
	if len(tables) != 1 || tables[0] != "seq_table" {
		t.Errorf("GetTables = %v, want [seq_table]", tables)
	}
}

type CheckedModel struct {
	ID  int
	Age int `gorm:"check:age_positive,age > 0"`
}

func (CheckedModel) TableName() string { return "checked" }

func TestTranslateCheckConstraint(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := gorm.Open(Open(dsn), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	if err := db.AutoMigrate(&CheckedModel{}); err != nil {
		t.Fatal(err)
	}

	insErr := db.Create(&CheckedModel{ID: 1, Age: -1}).Error
	if insErr != gorm.ErrCheckConstraintViolated {
		t.Errorf("check violation = %v, want gorm.ErrCheckConstraintViolated", insErr)
	}
}

// HasConstraint must match the exact constraint name, and DropConstraint must
// actually remove it.
func TestHasAndDropConstraint(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.AutoMigrate(&CheckedModel{}); err != nil {
		t.Fatal(err)
	}

	if !db.Migrator().HasConstraint(&CheckedModel{}, "age_positive") {
		t.Error("HasConstraint(age_positive) = false, want true")
	}
	if db.Migrator().HasConstraint(&CheckedModel{}, "age_pos") {
		t.Error("HasConstraint(age_pos) matched by prefix, want false")
	}

	if err := db.Migrator().DropConstraint(&CheckedModel{}, "age_positive"); err != nil {
		t.Fatalf("DropConstraint: %v", err)
	}
	if db.Migrator().HasConstraint(&CheckedModel{}, "age_positive") {
		t.Error("constraint still present after DropConstraint")
	}
	if err := db.Create(&CheckedModel{ID: 1, Age: -1}).Error; err != nil {
		t.Errorf("insert after dropping the check constraint: %v", err)
	}
}

func TestExplainQuotesStrings(t *testing.T) {
	out := Dialector{}.Explain("SELECT * FROM t WHERE name = ?", "hello")
	if !strings.Contains(out, "'hello'") {
		t.Errorf("Explain must quote strings with single quotes, got: %s", out)
	}
}

type CompositeKey struct {
	A int    `gorm:"primaryKey;autoIncrement"`
	B string `gorm:"primaryKey"`
}

func (CompositeKey) TableName() string { return "composite_keys" }

// A composite primary key that includes an auto-increment field must keep all
// key columns; AUTOINCREMENT (single-column INTEGER PRIMARY KEY only) is
// dropped instead of silently reducing the key to one column.
func TestCompositePrimaryKeyAutoIncrement(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.AutoMigrate(&CompositeKey{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	var pkCount int
	db.Raw("SELECT count(*) FROM pragma_table_info('composite_keys') WHERE pk > 0").Scan(&pkCount)
	if pkCount != 2 {
		t.Errorf("primary key column count = %d, want 2 (DDL: %s)", pkCount, tableDDL(t, db, "composite_keys"))
	}
}

func TestParseDDL_LowercaseUnique(t *testing.T) {
	d, err := parseDDL("CREATE TABLE `t` (`a` text, unique(`a`))")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range d.columns {
		if c.NameValue.String == "a" {
			if uniq, _ := c.Unique(); !uniq {
				t.Error("lowercase table-level unique(...) not recognized")
			}
		}
	}
}

func TestParseDDL_DecimalPrecision(t *testing.T) {
	d, err := parseDDL("CREATE TABLE `t` (`p` decimal(10,2), `v` varchar(25))")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range d.columns {
		switch c.NameValue.String {
		case "p":
			if ct, _ := c.ColumnType(); ct != "decimal(10,2)" {
				t.Errorf("ColumnType = %q, want decimal(10,2)", ct)
			}
			if c.DataTypeValue.String != "decimal" {
				t.Errorf("DataType = %q, want decimal", c.DataTypeValue.String)
			}
			precision, scale, ok := c.DecimalSize()
			if !ok || precision != 10 || scale != 2 {
				t.Errorf("DecimalSize = (%d,%d,%v), want (10,2,true)", precision, scale, ok)
			}
		case "v":
			if length, ok := c.Length(); !ok || length != 25 {
				t.Errorf("Length = (%d,%v), want (25,true)", length, ok)
			}
		}
	}
}

type DefaultValueModel struct {
	ID   int
	Code string `gorm:"default:hello"`
}

func (DefaultValueModel) TableName() string { return "default_values" }

// GORM embeds string default values into the DDL via Dialector.Explain
// (single quotes); parseDDL must strip them (and double quotes from tables
// created by older versions) so migrations stay idempotent.
func TestDefaultValueRoundTrip(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.AutoMigrate(&DefaultValueModel{}); err != nil {
		t.Fatal(err)
	}

	cols, err := db.Migrator().ColumnTypes(&DefaultValueModel{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if c.Name() == "code" {
			if dv, ok := c.DefaultValue(); !ok || dv != "hello" {
				t.Errorf("DefaultValue = (%q,%v), want (hello,true)", dv, ok)
			}
		}
	}

	// double quotes from tables created by older driver versions still parse
	d, err := parseDDL("CREATE TABLE `legacy` (`code` text DEFAULT \"hi\")")
	if err != nil {
		t.Fatal(err)
	}
	if dv, ok := d.columns[0].DefaultValue(); !ok || dv != "hi" {
		t.Errorf("legacy DefaultValue = (%q,%v), want (hi,true)", dv, ok)
	}
}

func TestParseDDL_TableOptionsRoundTrip(t *testing.T) {
	d, err := parseDDL("CREATE TABLE `t` (`id` text PRIMARY KEY) WITHOUT ROWID, STRICT")
	if err != nil {
		t.Fatal(err)
	}
	compiled := d.compile()
	if !strings.Contains(compiled, "WITHOUT ROWID") || !strings.Contains(compiled, "STRICT") {
		t.Errorf("table options lost in compile round-trip: %s", compiled)
	}
}
