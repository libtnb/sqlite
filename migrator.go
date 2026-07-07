package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

type Migrator struct {
	migrator.Migrator
}

func (m *Migrator) RunWithoutForeignKey(fc func() error) error {
	return m.runWithoutForeignKey(func(*gorm.DB) error { return fc() })
}

// runWithoutForeignKey runs fc with foreign key enforcement disabled.
// PRAGMA foreign_keys is per-connection, so the PRAGMAs and fc must run on
// the same connection; with a connection pool they could otherwise be sent
// to different connections.
func (m *Migrator) runWithoutForeignKey(fc func(tx *gorm.DB) error) error {
	run := func(tx *gorm.DB) error {
		var enabled int
		if err := tx.Raw("PRAGMA foreign_keys").Scan(&enabled).Error; err != nil {
			return err
		}
		if enabled == 1 {
			if err := tx.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
				return err
			}
			defer tx.Exec("PRAGMA foreign_keys = ON")
		}
		return fc(tx)
	}

	if sqlDB, err := m.DB.DB(); err != nil || sqlDB == nil {
		// no dedicated connection available (custom ConnPool or an ongoing
		// transaction, which is single-connection already)
		return run(m.DB)
	}
	return m.DB.Connection(run)
}

func (m *Migrator) HasTable(value any) bool {
	var count int
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		return m.DB.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", stmt.Table).Row().Scan(&count)
	})
	return count > 0
}

func (m *Migrator) DropTable(values ...any) error {
	return m.runWithoutForeignKey(func(tx *gorm.DB) error {
		values = m.ReorderModels(values, false)
		session := tx.Session(&gorm.Session{})

		for i := len(values) - 1; i >= 0; i-- {
			if err := m.RunWithValue(values[i], func(stmt *gorm.Statement) error {
				return session.Exec("DROP TABLE IF EXISTS ?", clause.Table{Name: stmt.Table}).Error
			}); err != nil {
				return err
			}
		}

		return nil
	})
}

func (m *Migrator) GetTables() (tableList []string, err error) {
	return tableList, m.DB.Raw(
		"SELECT name FROM sqlite_master WHERE type = ? AND name NOT LIKE ?", "table", "sqlite_%",
	).Scan(&tableList).Error
}

func (m *Migrator) HasColumn(value any, name string) bool {
	var count int
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if field := stmt.Schema.LookUpField(name); field != nil {
				name = field.DBName
			}
		}

		if name != "" {
			_ = m.DB.Raw(
				"SELECT count(*) FROM pragma_table_info(?) WHERE name = ? COLLATE NOCASE",
				stmt.Table, name,
			).Row().Scan(&count)
		}
		return nil
	})
	return count > 0
}

func (m *Migrator) AlterColumn(value any, name string) error {
	return m.runWithoutForeignKey(func(tx *gorm.DB) error {
		return m.recreateTable(tx, value, nil, func(ddl *ddl, stmt *gorm.Statement) (*ddl, []any, error) {
			if stmt.Schema == nil {
				return nil, nil, fmt.Errorf("failed to alter field with name %v: model with schema is required", name)
			}
			if field := stmt.Schema.LookUpField(name); field != nil {
				var sqlArgs []any
				for i, f := range ddl.fields {
					if matches := columnRegexp.FindStringSubmatch(f); len(matches) > 1 && matches[1] == field.DBName {
						ddl.fields[i] = fmt.Sprintf("`%v` ?", field.DBName)
						sqlArgs = []any{m.FullDataTypeOf(field)}
						// table created by old version might look like `CREATE TABLE ? (? varchar(10) UNIQUE)`.
						// FullDataTypeOf doesn't contain UNIQUE, so we need to add unique constraint.
						if strings.Contains(strings.ToUpper(matches[3]), " UNIQUE") {
							uniName := m.DB.NamingStrategy.UniqueName(stmt.Table, field.DBName)
							uni, _ := m.GuessConstraintInterfaceAndTable(stmt, uniName)
							if uni != nil {
								uniSQL, uniArgs := uni.Build()
								ddl.addConstraint(uniName, uniSQL)
								sqlArgs = append(sqlArgs, uniArgs...)
							}
						}
						break
					}
				}
				return ddl, sqlArgs, nil
			}
			return nil, nil, fmt.Errorf("failed to alter field with name %v", name)
		})
	})
}

// ColumnTypes return columnTypes []gorm.ColumnType and execErr error
func (m *Migrator) ColumnTypes(value any) ([]gorm.ColumnType, error) {
	columnTypes := make([]gorm.ColumnType, 0)
	execErr := m.RunWithValue(value, func(stmt *gorm.Statement) (err error) {
		var (
			sqls   []string
			sqlDDL *ddl
		)

		if err := m.DB.Raw("SELECT sql FROM sqlite_master WHERE type IN ? AND tbl_name = ? AND sql IS NOT NULL order by type = ? desc", []string{"table", "index"}, stmt.Table, "table").Scan(&sqls).Error; err != nil {
			return err
		}

		if sqlDDL, err = parseDDL(sqls...); err != nil {
			return err
		}

		rows, err := m.DB.Session(&gorm.Session{}).Table(stmt.Table).Limit(1).Rows()
		if err != nil {
			return err
		}
		defer func() {
			if cerr := rows.Close(); err == nil {
				err = cerr
			}
		}()

		var rawColumnTypes []*sql.ColumnType
		rawColumnTypes, err = rows.ColumnTypes()
		if err != nil {
			return err
		}

		for _, c := range rawColumnTypes {
			columnType := migrator.ColumnType{SQLColumnType: c}
			for _, column := range sqlDDL.columns {
				if column.NameValue.String == c.Name() {
					column.SQLColumnType = c
					columnType = column
					break
				}
			}
			columnTypes = append(columnTypes, columnType)
		}

		return err
	})

	return columnTypes, execErr
}

func (m *Migrator) DropColumn(value any, name string) error {
	return m.runWithoutForeignKey(func(tx *gorm.DB) error {
		return m.recreateTable(tx, value, nil, func(ddl *ddl, stmt *gorm.Statement) (*ddl, []any, error) {
			if stmt.Schema != nil {
				if field := stmt.Schema.LookUpField(name); field != nil {
					name = field.DBName
				}
			}

			if !ddl.removeColumn(name) {
				return nil, nil, fmt.Errorf("failed to drop column %v: not found in the DDL of table %v", name, stmt.Table)
			}
			return ddl, nil, nil
		})
	})
}

func (m *Migrator) CreateConstraint(value any, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		constraint, table := m.GuessConstraintInterfaceAndTable(stmt, name)

		return m.runWithoutForeignKey(func(tx *gorm.DB) error {
			return m.recreateTable(tx, value, &table,
				func(ddl *ddl, stmt *gorm.Statement) (*ddl, []any, error) {
					var (
						constraintName   string
						constraintSql    string
						constraintValues []any
					)

					if constraint != nil {
						constraintName = constraint.GetName()
						constraintSql, constraintValues = constraint.Build()
					} else {
						return nil, nil, nil
					}

					ddl.addConstraint(constraintName, constraintSql)
					return ddl, constraintValues, nil
				})
		})
	})
}

func (m *Migrator) DropConstraint(value any, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		constraint, table := m.GuessConstraintInterfaceAndTable(stmt, name)
		if constraint != nil {
			name = constraint.GetName()
		}

		return m.runWithoutForeignKey(func(tx *gorm.DB) error {
			return m.recreateTable(tx, value, &table,
				func(ddl *ddl, stmt *gorm.Statement) (*ddl, []any, error) {
					ddl.removeConstraint(name)
					return ddl, nil, nil
				})
		})
	})
}

func (m *Migrator) HasConstraint(value any, name string) bool {
	var has bool
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		constraint, table := m.GuessConstraintInterfaceAndTable(stmt, name)
		if constraint != nil {
			name = constraint.GetName()
		}

		rawDDL, err := m.getRawDDL(table)
		if err != nil {
			return err
		}
		parsed, err := parseDDL(rawDDL)
		if err != nil {
			return err
		}

		reg := compileConstraintRegexp(name)
		has = slices.ContainsFunc(parsed.fields, reg.MatchString)
		return nil
	})

	return has
}

func (m *Migrator) CurrentDatabase() (name string) {
	var null any
	_ = m.DB.Raw("PRAGMA database_list").Row().Scan(&null, &name, &null)
	return
}

func (m *Migrator) BuildIndexOptions(opts []schema.IndexOption, stmt *gorm.Statement) (results []any) {
	for _, opt := range opts {
		str := stmt.Quote(opt.DBName)
		if opt.Expression != "" {
			str = opt.Expression
		}

		if opt.Collate != "" {
			str += " COLLATE " + opt.Collate
		}

		if opt.Sort != "" {
			str += " " + opt.Sort
		}
		results = append(results, clause.Expr{SQL: str})
	}
	return
}

func (m *Migrator) CreateIndex(value any, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				opts := m.BuildIndexOptions(idx.Fields, stmt)
				values := []any{clause.Column{Name: idx.Name}, clause.Table{Name: stmt.Table}, opts}

				createIndexSQL := "CREATE "
				if idx.Class != "" {
					createIndexSQL += idx.Class + " "
				}
				createIndexSQL += "INDEX ?"

				if idx.Type != "" {
					createIndexSQL += " USING " + idx.Type
				}
				createIndexSQL += " ON ??"

				if idx.Where != "" {
					createIndexSQL += " WHERE " + idx.Where
				}

				return m.DB.Exec(createIndexSQL, values...).Error
			}
		}
		return fmt.Errorf("failed to create index with name %v", name)
	})
}

func (m *Migrator) HasIndex(value any, name string) bool {
	var count int
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				name = idx.Name
			}
		}

		if name != "" {
			_ = m.DB.Raw(
				"SELECT count(*) FROM sqlite_master WHERE type = ? AND tbl_name = ? AND name = ?", "index", stmt.Table, name,
			).Row().Scan(&count)
		}
		return nil
	})
	return count > 0
}

func (m *Migrator) RenameIndex(value any, oldName, newName string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		var sql string
		_ = m.DB.Raw("SELECT sql FROM sqlite_master WHERE type = ? AND tbl_name = ? AND name = ?", "index", stmt.Table, oldName).Row().Scan(&sql)
		if sql != "" {
			if err := m.DropIndex(value, oldName); err != nil {
				return err
			}
			return m.DB.Exec(strings.Replace(sql, oldName, newName, 1)).Error
		}
		return fmt.Errorf("failed to find index with name %v", oldName)
	})
}

func (m *Migrator) DropIndex(value any, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				name = idx.Name
			}
		}

		return m.DB.Exec("DROP INDEX ?", clause.Column{Name: name}).Error
	})
}

type Index struct {
	Seq     int
	Name    string
	Unique  bool
	Origin  string
	Partial bool
}

// GetIndexes return Indexes []gorm.Index and execErr error,
// See the [doc]
//
// [doc]: https://www.sqlite.org/pragma.html#pragma_index_list
func (m *Migrator) GetIndexes(value any) ([]gorm.Index, error) {
	indexes := make([]gorm.Index, 0)
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		rst := make([]*Index, 0)
		if err := m.DB.Raw("SELECT * FROM PRAGMA_index_list(?)", stmt.Table).Scan(&rst).Error; err != nil { // alias `PRAGMA index_list(?)`
			return err
		}
		for _, index := range rst {
			if index.Origin == "u" { // skip the index was created by a UNIQUE constraint
				continue
			}
			var columns []string
			if err := m.DB.Raw("SELECT name FROM PRAGMA_index_info(?)", index.Name).Scan(&columns).Error; err != nil { // alias `PRAGMA index_info(?)`
				return err
			}
			indexes = append(indexes, &migrator.Index{
				TableName:       stmt.Table,
				NameValue:       index.Name,
				ColumnList:      columns,
				PrimaryKeyValue: sql.NullBool{Bool: index.Origin == "pk", Valid: true}, // The exceptions are INTEGER PRIMARY KEY
				UniqueValue:     sql.NullBool{Bool: index.Unique, Valid: true},
			})
		}
		return nil
	})
	return indexes, err
}

func (m *Migrator) getRawDDL(table string) (string, error) {
	var createSQL string
	err := m.DB.Raw("SELECT sql FROM sqlite_master WHERE type = ? AND tbl_name = ? AND name = ?", "table", table, table).Row().Scan(&createSQL)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if createSQL == "" {
		return "", fmt.Errorf("failed to get DDL of table %q: table not found", table)
	}
	return createSQL, nil
}

// recreateTable implements ALTER TABLE operations SQLite doesn't support by
// creating a modified copy of the table, moving the data over, dropping the
// original and renaming the copy back. execDB must be pinned to a single
// connection (see runWithoutForeignKey) so the PRAGMAs used along the way
// apply to the connection that runs the transaction.
func (m *Migrator) recreateTable(
	execDB *gorm.DB, value any, tablePtr *string,
	getCreateSQL func(ddl *ddl, stmt *gorm.Statement) (sql *ddl, sqlArgs []any, err error),
) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		table := stmt.Table
		if tablePtr != nil {
			table = *tablePtr
		}

		rawDDL, err := m.getRawDDL(table)
		if err != nil {
			return err
		}

		originDDL, err := parseDDL(rawDDL)
		if err != nil {
			return err
		}

		createDDL, sqlArgs, err := getCreateSQL(originDDL.clone(), stmt)
		if err != nil {
			return err
		}
		if createDDL == nil {
			return nil
		}

		newTableName := table + "__temp"
		if err := createDDL.renameTable(newTableName, table); err != nil {
			return err
		}

		columns := createDDL.getColumns()
		createSQL := createDDL.compile()

		// indexes and triggers are dropped together with the old table; save
		// their DDL so they can be recreated on the rebuilt table.
		var auxDDLs []string
		if err := execDB.Raw(
			"SELECT sql FROM sqlite_master WHERE tbl_name = ? AND type IN (?, ?) AND sql IS NOT NULL",
			table, "index", "trigger",
		).Scan(&auxDDLs).Error; err != nil {
			return err
		}

		return execDB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(createSQL, sqlArgs...).Error; err != nil {
				return err
			}

			queries := []string{
				fmt.Sprintf("INSERT INTO `%v`(%v) SELECT %v FROM `%v`", newTableName, strings.Join(columns, ","), strings.Join(columns, ","), table),
				fmt.Sprintf("DROP TABLE `%v`", table),
			}
			for _, query := range queries {
				if err := tx.Exec(query).Error; err != nil {
					return err
				}
			}

			// legacy_alter_table keeps RENAME from re-resolving views that
			// reference the table; they point at the original name and become
			// valid again right after the rename.
			if err := tx.Exec("PRAGMA legacy_alter_table = ON").Error; err != nil {
				return err
			}
			renameErr := tx.Exec(fmt.Sprintf("ALTER TABLE `%v` RENAME TO `%v`", newTableName, table)).Error
			if err := tx.Exec("PRAGMA legacy_alter_table = OFF").Error; renameErr == nil {
				renameErr = err
			}
			if renameErr != nil {
				return renameErr
			}

			// recreate the saved indexes and triggers; ones referencing a
			// column that no longer exists cannot apply anymore and are
			// skipped, any other failure aborts the migration
			for _, aux := range auxDDLs {
				if err := tx.Exec(aux).Error; err != nil {
					if strings.Contains(err.Error(), "no such column") {
						continue
					}
					return err
				}
			}
			return nil
		})
	})
}
