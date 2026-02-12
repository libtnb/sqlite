package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

// Compile-time interface assertions.
var (
	_ driver.Connector                      = (*wrappedConnector)(nil)
	_ driver.Conn                           = (*wrappedConn)(nil)
	_ driver.ConnPrepareContext             = (*wrappedConn)(nil)
	_ driver.QueryerContext                 = (*wrappedConn)(nil)
	_ driver.ExecerContext                  = (*wrappedConn)(nil)
	_ driver.ConnBeginTx                    = (*wrappedConn)(nil)
	_ driver.Pinger                         = (*wrappedConn)(nil)
	_ driver.SessionResetter                = (*wrappedConn)(nil)
	_ driver.Validator                      = (*wrappedConn)(nil)
	_ driver.Stmt                           = (*wrappedStmt)(nil)
	_ driver.StmtExecContext                = (*wrappedStmt)(nil)
	_ driver.StmtQueryContext               = (*wrappedStmt)(nil)
	_ driver.Rows                           = (*wrappedRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*wrappedRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*wrappedRows)(nil)
	_ driver.RowsColumnTypeLength           = (*wrappedRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*wrappedRows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*wrappedRows)(nil)
)

// wrappedConnector wraps a driver.Driver to intercept Rows and fix
// ColumnTypeScanType for datetime TEXT columns.
type wrappedConnector struct {
	dsn string
	drv driver.Driver
}

func newWrappedConnector(driverName, dsn string) (*wrappedConnector, error) {
	db, err := sql.Open(driverName, "")
	if err != nil {
		return nil, err
	}
	drv := db.Driver()
	_ = db.Close()
	return &wrappedConnector{dsn: injectDSNParams(dsn), drv: drv}, nil
}

// injectDSNParams appends _texttotime=1 and _inttotime=1 to DSN if not already set,
// enabling modernc.org/sqlite to return time.Time for datetime columns.
func injectDSNParams(dsn string) string {
	for _, param := range []string{"_texttotime", "_inttotime"} {
		if strings.Contains(dsn, param) {
			continue
		}
		if strings.Contains(dsn, "?") {
			dsn += "&" + param + "=1"
		} else {
			dsn += "?" + param + "=1"
		}
	}
	return dsn
}

func (c *wrappedConnector) Connect(_ context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &wrappedConn{conn: conn}, nil
}

func (c *wrappedConnector) Driver() driver.Driver {
	return c.drv
}

// wrappedConn wraps a driver.Conn, delegating all optional interfaces and
// ensuring Query paths return wrappedRows.
type wrappedConn struct {
	conn driver.Conn
}

func (c *wrappedConn) Prepare(query string) (driver.Stmt, error) {
	s, err := c.conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &wrappedStmt{stmt: s}, nil
}

func (c *wrappedConn) Close() error {
	return c.conn.Close()
}

func (c *wrappedConn) Begin() (driver.Tx, error) {
	return c.conn.Begin() //nolint:staticcheck
}

func (c *wrappedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if cc, ok := c.conn.(driver.ConnPrepareContext); ok {
		s, err := cc.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &wrappedStmt{stmt: s}, nil
	}
	return c.Prepare(query)
}

func (c *wrappedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.conn.(driver.QueryerContext); ok {
		rows, err := qc.QueryContext(ctx, query, args)
		if err != nil {
			return nil, err
		}
		return &wrappedRows{rows: rows}, nil
	}
	return nil, driver.ErrSkip
}

func (c *wrappedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.conn.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *wrappedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.conn.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.conn.Begin() //nolint:staticcheck
}

func (c *wrappedConn) Ping(ctx context.Context) error {
	if p, ok := c.conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *wrappedConn) ResetSession(ctx context.Context) error {
	if rs, ok := c.conn.(driver.SessionResetter); ok {
		return rs.ResetSession(ctx)
	}
	return nil
}

func (c *wrappedConn) IsValid() bool {
	if v, ok := c.conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// wrappedStmt wraps a driver.Stmt, ensuring Query paths return wrappedRows.
type wrappedStmt struct {
	stmt driver.Stmt
}

func (s *wrappedStmt) Close() error {
	return s.stmt.Close()
}

func (s *wrappedStmt) NumInput() int {
	return s.stmt.NumInput()
}

func (s *wrappedStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.stmt.Exec(args) //nolint:staticcheck
}

func (s *wrappedStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.stmt.Query(args) //nolint:staticcheck
	if err != nil {
		return nil, err
	}
	return &wrappedRows{rows: rows}, nil
}

func (s *wrappedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if sc, ok := s.stmt.(driver.StmtExecContext); ok {
		return sc.ExecContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return s.stmt.Exec(values) //nolint:staticcheck
}

func (s *wrappedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	var rows driver.Rows
	var err error
	if sc, ok := s.stmt.(driver.StmtQueryContext); ok {
		rows, err = sc.QueryContext(ctx, args)
	} else {
		values := make([]driver.Value, len(args))
		for i, arg := range args {
			values[i] = arg.Value
		}
		rows, err = s.stmt.Query(values) //nolint:staticcheck
	}
	if err != nil {
		return nil, err
	}
	return &wrappedRows{rows: rows}, nil
}

// wrappedRows wraps driver.Rows to fix ColumnTypeScanType for datetime columns.
//
// The underlying modernc.org/sqlite driver's ColumnTypeScanType returns string
// for all TEXT columns, but its Next() method parses DATE/DATETIME/TIMESTAMP
// TEXT values into time.Time. This mismatch causes database/sql to format the
// time.Time back into a string via convertAssign.
type wrappedRows struct {
	rows driver.Rows
}

func (r *wrappedRows) Columns() []string {
	return r.rows.Columns()
}

func (r *wrappedRows) Close() error {
	return r.rows.Close()
}

func (r *wrappedRows) Next(dest []driver.Value) error {
	return r.rows.Next(dest)
}

func (r *wrappedRows) ColumnTypeScanType(index int) reflect.Type {
	if ct, ok := r.rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		switch ct.ColumnTypeDatabaseTypeName(index) {
		case "DATE", "DATETIME", "TIMESTAMP", "TIME":
			return timeType
		}
	}
	if ct, ok := r.rows.(driver.RowsColumnTypeScanType); ok {
		return ct.ColumnTypeScanType(index)
	}
	return reflect.TypeOf("")
}

func (r *wrappedRows) ColumnTypeDatabaseTypeName(index int) string {
	if ct, ok := r.rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return ct.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

func (r *wrappedRows) ColumnTypeLength(index int) (int64, bool) {
	if ct, ok := r.rows.(driver.RowsColumnTypeLength); ok {
		return ct.ColumnTypeLength(index)
	}
	return 0, false
}

func (r *wrappedRows) ColumnTypeNullable(index int) (bool, bool) {
	if ct, ok := r.rows.(driver.RowsColumnTypeNullable); ok {
		return ct.ColumnTypeNullable(index)
	}
	return false, false
}

func (r *wrappedRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if ct, ok := r.rows.(driver.RowsColumnTypePrecisionScale); ok {
		return ct.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}
