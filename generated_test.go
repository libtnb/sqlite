package sqlite

import (
	"strings"
	"testing"

	"gorm.io/gorm/schema"
)

func TestDataTypeOfGeneratedColumn(t *testing.T) {
	dialector := Dialector{}
	tests := []struct {
		name  string
		field *schema.Field
		want  string
	}{
		{
			name:  "computed column renders a STORED generated column",
			field: &schema.Field{DataType: schema.Float, TagSettings: map[string]string{"GENERATED": "price * quantity"}},
			want:  "real GENERATED ALWAYS AS (price * quantity) STORED",
		},
		{
			name:  "computed expression keeps commas",
			field: &schema.Field{DataType: schema.String, TagSettings: map[string]string{"GENERATED": "coalesce(first_name, last_name)"}},
			want:  "text GENERATED ALWAYS AS (coalesce(first_name, last_name)) STORED",
		},
		{
			// `identity` is reserved for identity columns, which SQLite renders
			// through its native AUTOINCREMENT rather than a computed column.
			name:  "identity keyword is not treated as a computed column",
			field: &schema.Field{DataType: schema.Int, AutoIncrement: true, TagSettings: map[string]string{"GENERATED": "identity"}},
			want:  "integer PRIMARY KEY AUTOINCREMENT",
		},
		{
			name:  "identity with an explicit mode is also reserved",
			field: &schema.Field{DataType: schema.Int, AutoIncrement: true, TagSettings: map[string]string{"GENERATED": "identity always"}},
			want:  "integer PRIMARY KEY AUTOINCREMENT",
		},
		{
			name:  "a bare generated tag is ignored",
			field: &schema.Field{DataType: schema.Float, TagSettings: map[string]string{"GENERATED": "GENERATED"}},
			want:  "real",
		},
		{
			name:  "a lowercase generated expression is not mistaken for a bare tag",
			field: &schema.Field{DataType: schema.Float, TagSettings: map[string]string{"GENERATED": "generated"}},
			want:  "real GENERATED ALWAYS AS (generated) STORED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dialector.DataTypeOf(tt.field); got != tt.want {
				t.Errorf("DataTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

type GeneratedProduct struct {
	ID       uint `gorm:"primaryKey"`
	Name     string
	Price    float64
	Quantity int
	Total    float64 `gorm:"->;generated:price * quantity"`
}

func (GeneratedProduct) TableName() string { return "generated_products" }

func TestGeneratedColumnEndToEnd(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.AutoMigrate(&GeneratedProduct{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	ddl := tableDDL(t, db, "generated_products")
	if !strings.Contains(ddl, "GENERATED ALWAYS AS (price * quantity) STORED") {
		t.Fatalf("DDL missing generated clause: %s", ddl)
	}

	if err := db.Create(&GeneratedProduct{ID: 1, Name: "x", Price: 2.5, Quantity: 4}).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got GeneratedProduct
	if err := db.First(&got, 1).Error; err != nil {
		t.Fatal(err)
	}
	if got.Total != 10 {
		t.Errorf("Total = %v, want 10", got.Total)
	}

	// repeated AutoMigrate must be idempotent, not rebuild or re-alter
	if err := db.AutoMigrate(&GeneratedProduct{}); err != nil {
		t.Fatalf("second AutoMigrate: %v", err)
	}
	if again := tableDDL(t, db, "generated_products"); again != ddl {
		t.Errorf("DDL changed after second AutoMigrate:\n  before: %s\n  after:  %s", ddl, again)
	}
}

// A table rebuild copies data with INSERT INTO ... SELECT, which must exclude
// generated columns (SQLite forbids writing them); the rebuilt table keeps the
// generated column working.
func TestGeneratedColumnSurvivesRecreateTable(t *testing.T) {
	db := openTestDB(t, "")
	if err := db.AutoMigrate(&GeneratedProduct{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&GeneratedProduct{ID: 1, Name: "x", Price: 3, Quantity: 3}).Error; err != nil {
		t.Fatal(err)
	}

	if err := db.Migrator().DropColumn(&GeneratedProduct{}, "name"); err != nil {
		t.Fatalf("DropColumn on a table with a generated column: %v", err)
	}

	ddl := tableDDL(t, db, "generated_products")
	if !strings.Contains(ddl, "GENERATED ALWAYS AS (price * quantity) STORED") {
		t.Errorf("generated clause lost after rebuild: %s", ddl)
	}
	var total float64
	if err := db.Raw("SELECT `total` FROM `generated_products` WHERE `id` = 1").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 9 {
		t.Errorf("Total after rebuild = %v, want 9 (data copied, expression recomputed)", total)
	}
}
