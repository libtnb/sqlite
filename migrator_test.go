package sqlite

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
	if err := db.Raw("SELECT count(*) FROM fk_children").Scan(&childCount).Error; err != nil {
		t.Fatalf("querying fk_children: %v", err)
	}
	if childCount != 1 {
		t.Errorf("child rows after DropColumn = %d, want 1", childCount)
	}
	var fkEnabled int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&fkEnabled).Error; err != nil {
		t.Fatalf("querying foreign_keys pragma: %v", err)
	}
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
	if err := db.Raw("SELECT name FROM sqlite_master WHERE tbl_name = 'idx_table' AND type IN ('index','trigger')").Scan(&names).Error; err != nil {
		t.Fatalf("querying sqlite_master: %v", err)
	}
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
	if err := db.Raw("SELECT count(*) FROM audit").Scan(&auditCount).Error; err != nil {
		t.Fatalf("querying audit: %v", err)
	}
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
	if err := db.Raw("SELECT count(*) FROM pragma_table_info('composite_keys') WHERE pk > 0").Scan(&pkCount).Error; err != nil {
		t.Fatalf("querying pragma_table_info: %v", err)
	}
	if pkCount != 2 {
		t.Errorf("primary key column count = %d, want 2 (DDL: %s)", pkCount, tableDDL(t, db, "composite_keys"))
	}
}

func TestParseDDL_LowercaseUnique(t *testing.T) {
	d, err := parseDDL("CREATE TABLE `t` (`a` text, unique(`a`))")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range d.columns {
		if c.NameValue.String == "a" {
			found = true
			if uniq, _ := c.Unique(); !uniq {
				t.Error("lowercase table-level unique(...) not recognized")
			}
		}
	}
	if !found {
		t.Fatal("column a was not parsed at all")
	}
}

func TestConstraintNameQuoting(t *testing.T) {
	forms := []struct {
		desc, ddl, name string
	}{
		{"backquotes", "CREATE TABLE `t` (`a` integer, CONSTRAINT `chk_a` CHECK (`a` > 0))", "chk_a"},
		{"double quotes", `CREATE TABLE "t" ("a" integer, CONSTRAINT "chk_a" CHECK ("a" > 0))`, "chk_a"},
		{"single quotes", "CREATE TABLE `t` (`a` integer, CONSTRAINT 'chk_a' CHECK (`a` > 0))", "chk_a"},
		{"brackets", "CREATE TABLE t (a integer, CONSTRAINT [chk_a] CHECK (a > 0))", "chk_a"},
		{"unquoted", "CREATE TABLE t (a integer, CONSTRAINT chk_a CHECK (a > 0))", "chk_a"},
		{"hyphenated name", "CREATE TABLE `t` (`a` integer, CONSTRAINT `chk-a` CHECK (`a` > 0))", "chk-a"},
		{"unicode name", "CREATE TABLE `t` (`a` integer, CONSTRAINT `检查_a` CHECK (`a` > 0))", "检查_a"},
	}
	for _, f := range forms {
		form, ddlSQL := f.desc, f.ddl
		d, err := parseDDL(ddlSQL)
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		reg := compileConstraintRegexp(f.name)
		if !slices.ContainsFunc(d.fields, reg.MatchString) {
			t.Errorf("%s: constraint %s not matched", form, f.name)
		}
		regPrefix := compileConstraintRegexp("chk")
		if slices.ContainsFunc(d.fields, regPrefix.MatchString) {
			t.Errorf("%s: prefix chk must not match", form)
		}
		// the clause must not be mistaken for a column, or the rebuild's
		// data copy fails with "no column named CONSTRAINT"
		if cols := d.getColumns(); len(cols) != 1 || cols[0] != "`a`" {
			t.Errorf("%s: getColumns = %v, want [`a`]", form, cols)
		}
	}
}

// removeColumn matches the column name against the raw DDL field, so it has to
// cope with every identifier quoting form. A false return makes DropColumn
// fail, so a missed match is not a silent no-op.
func TestRemoveColumnQuotingForms(t *testing.T) {
	forms := map[string]string{
		"backquotes":    "CREATE TABLE `t` (`a` integer, `b` text)",
		"double quotes": `CREATE TABLE "t" ("a" integer, "b" text)`,
		"single quotes": "CREATE TABLE `t` (`a` integer, 'b' text)",
		"brackets":      "CREATE TABLE t ([a] integer, [b] text)",
		"unquoted":      "CREATE TABLE t (a integer, b text)",
	}
	for form, ddlSQL := range forms {
		d, err := parseDDL(ddlSQL)
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		if !d.removeColumn("b") {
			t.Errorf("%s: removeColumn(b) = false, want true", form)
		}
		if cols := d.getColumns(); len(cols) != 1 || cols[0] != "`a`" {
			t.Errorf("%s: getColumns after removal = %v, want [`a`]", form, cols)
		}
	}
}

// tableRegexp used to reject a bracket-quoted table name outright, and
// renameTable has to consume the brackets as well or the rewritten head
// becomes [`t__temp`], which names a table with literal backquotes in it.
func TestTableNameQuotingForms(t *testing.T) {
	forms := map[string]string{
		"backquotes":    "CREATE TABLE `t` (`a` integer, `b` text)",
		"double quotes": `CREATE TABLE "t" ("a" integer, "b" text)`,
		"single quotes": "CREATE TABLE 't' (`a` integer, `b` text)",
		"brackets":      "CREATE TABLE [t] ([a] integer, [b] text)",
		"unquoted":      "CREATE TABLE t (a integer, b text)",
	}
	for form, ddlSQL := range forms {
		d, err := parseDDL(ddlSQL)
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		if cols := d.getColumns(); len(cols) != 2 {
			t.Errorf("%s: getColumns = %v, want [`a` `b`]", form, cols)
		}
		if err := d.renameTable("t__temp", "t"); err != nil {
			t.Errorf("%s: renameTable: %v", form, err)
			continue
		}
		if !strings.Contains(d.head, "`t__temp`") {
			t.Errorf("%s: renamed head = %q, want it to quote t__temp", form, d.head)
		}
		if strings.ContainsAny(d.head, "[]") {
			t.Errorf("%s: renamed head still carries the old brackets: %q", form, d.head)
		}
	}
}

type BracketTable struct {
	A int
	B string
}

func (BracketTable) TableName() string { return "bracket_tbl" }

// End to end: a table whose DDL uses bracket quoting throughout must rebuild
// like any other, keeping its rows, constraints and indexes. DropColumn used
// to fail with "invalid DDL" before parseDDL accepted the name.
func TestBracketQuotedTableRebuild(t *testing.T) {
	db := openTestDB(t, "")
	for _, s := range []string{
		"CREATE TABLE [bracket_tbl] ([a] integer, [b] text, CONSTRAINT [chk-a] CHECK ([a] > 0))",
		"CREATE INDEX [idx_bracket_a] ON [bracket_tbl]([a])",
		"INSERT INTO [bracket_tbl] ([a],[b]) VALUES (1,'keep')",
	} {
		if err := db.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}

	if !db.Migrator().HasConstraint(&BracketTable{}, "chk-a") {
		t.Error("HasConstraint(chk-a) = false, want true")
	}
	if err := db.Migrator().DropColumn(&BracketTable{}, "b"); err != nil {
		t.Fatalf("DropColumn on a bracket-quoted table: %v", err)
	}
	if db.Migrator().HasColumn(&BracketTable{}, "b") {
		t.Error("column b still present after DropColumn")
	}

	var a int
	if err := db.Raw("SELECT a FROM bracket_tbl").Scan(&a).Error; err != nil || a != 1 {
		t.Errorf("row lost in the rebuild: a=%d err=%v", a, err)
	}
	if !db.Migrator().HasConstraint(&BracketTable{}, "chk-a") {
		t.Error("constraint chk-a lost in the rebuild")
	}
	var idx []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE tbl_name='bracket_tbl' AND type='index'").Scan(&idx).Error; err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(idx, "idx_bracket_a") {
		t.Errorf("index idx_bracket_a lost in the rebuild, remaining: %v", idx)
	}
}

type NoSuchTableModel struct {
	ID int
}

func (NoSuchTableModel) TableName() string { return "no_such_table" }

// HasConstraint reports a plain bool, so looking it up on a table that does not
// exist must simply answer false and leave the caller's session usable.
func TestHasConstraintMissingTable(t *testing.T) {
	db := openTestDB(t, "")

	if db.Migrator().HasConstraint(&NoSuchTableModel{}, "chk_x") {
		t.Error("HasConstraint on a missing table = true, want false")
	}
	if db.Error != nil {
		t.Errorf("session left with an error: %v", db.Error)
	}
	if err := db.Exec("CREATE TABLE hc_probe (id integer)").Error; err != nil {
		t.Errorf("follow-up statement on the same session failed: %v", err)
	}
}

// A table-level UNIQUE constraint marks its columns unique whichever way the
// constraint name is quoted.
func TestUniqueConstraintNameQuoting(t *testing.T) {
	forms := map[string]string{
		"backquotes":    "CREATE TABLE `t` (`a` integer, CONSTRAINT `u_a` UNIQUE (`a`))",
		"double quotes": `CREATE TABLE "t" ("a" integer, CONSTRAINT "u_a" UNIQUE ("a"))`,
		"single quotes": "CREATE TABLE `t` (`a` integer, CONSTRAINT 'u_a' UNIQUE (`a`))",
		"brackets":      "CREATE TABLE t (a integer, CONSTRAINT [u_a] UNIQUE (a))",
		"unquoted":      "CREATE TABLE t (a integer, CONSTRAINT u_a UNIQUE (a))",
	}
	for form, ddlSQL := range forms {
		d, err := parseDDL(ddlSQL)
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		found := false
		for _, c := range d.columns {
			if c.NameValue.String != "a" {
				continue
			}
			found = true
			if uniq, _ := c.Unique(); !uniq {
				t.Errorf("%s: column a not marked unique", form)
			}
		}
		if !found {
			t.Fatalf("%s: column a was not parsed at all", form)
		}
		if cols := d.getColumns(); len(cols) != 1 || cols[0] != "`a`" {
			t.Errorf("%s: getColumns = %v, want [`a`]", form, cols)
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

type DeadlockParent struct {
	ID int `gorm:"primaryKey"`
}

func (DeadlockParent) TableName() string { return "deadlock_parents" }

type DeadlockChild struct {
	ID       int
	ParentID int
	Parent   DeadlockParent `gorm:"foreignKey:ParentID"`
	Extra    string
}

func (DeadlockChild) TableName() string { return "deadlock_children" }

type DeadlockOrphan struct {
	ID       int
	ParentID int
	Parent   DeadlockParent `gorm:"foreignKey:ParentID"`
}

func (DeadlockOrphan) TableName() string { return "deadlock_orphans" }

// Issue #24: with SetMaxOpenConns(1), every migrator entry point that pins a
// connection must not issue queries through the pool from inside the pinned
// callback — that inner query waits forever for the connection the callback
// itself is holding.
func TestMigratorSingleConnectionNoDeadlock(t *testing.T) {
	db := openSingleConnDB(t)
	if err := db.AutoMigrate(&DeadlockParent{}, &DeadlockChild{}); err != nil {
		t.Fatal(err)
	}

	mustFinish(t, "DropColumn", func() error {
		return db.Migrator().DropColumn(&DeadlockChild{}, "extra")
	})
	mustFinish(t, "AlterColumn", func() error {
		return db.Migrator().AlterColumn(&DeadlockChild{}, "parent_id")
	})

	// a table missing a foreign key declared in the model makes AutoMigrate
	// go through CreateConstraint → recreateTable
	if err := db.Exec("CREATE TABLE `deadlock_orphans` (`id` integer, `parent_id` integer)").Error; err != nil {
		t.Fatal(err)
	}
	mustFinish(t, "AutoMigrate adding a foreign key", func() error {
		return db.AutoMigrate(&DeadlockOrphan{})
	})

	// the public RunWithoutForeignKey callback has no handle on a pinned
	// connection, so plain queries and nested migrator calls must both work
	m := db.Migrator().(*Migrator)
	mustFinish(t, "RunWithoutForeignKey with a plain query", func() error {
		return m.RunWithoutForeignKey(func() error {
			var n int
			return db.Raw("SELECT count(*) FROM deadlock_parents").Scan(&n).Error
		})
	})
	mustFinish(t, "RunWithoutForeignKey with a nested migrator call", func() error {
		return m.RunWithoutForeignKey(func() error {
			return db.Migrator().DropTable("deadlock_orphans")
		})
	})
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

	// only one outer quote pair is stripped; inner quotes survive
	d, err = parseDDL("CREATE TABLE `q` (`code` text DEFAULT '\"x\"')")
	if err != nil {
		t.Fatal(err)
	}
	if dv, ok := d.columns[0].DefaultValue(); !ok || dv != `"x"` {
		t.Errorf(`quoted DefaultValue = (%q,%v), want ("x",true)`, dv, ok)
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
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&ddl).Error; err != nil {
		t.Fatalf("querying sqlite_master: %v", err)
	}
	return ddl
}

// openSingleConnDB mimics the common SQLite setup that serializes writers:
// a pool limited to one connection.
func openSingleConnDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openTestDB(t, "_pragma=foreign_keys(1)")
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

// mustFinish fails the test if fn is still running after the timeout, which
// is how a pool deadlock manifests.
func mustFinish(t *testing.T, name string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: deadlocked (issue #24)", name)
	}
}
