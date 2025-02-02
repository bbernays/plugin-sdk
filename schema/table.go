package schema

import (
	"context"
	"fmt"
	"regexp"

	"github.com/apache/arrow/go/v13/arrow"
	"github.com/cloudquery/plugin-sdk/v4/glob"
	"golang.org/x/exp/slices"
)

// TableResolver is the main entry point when a table is sync is called.
//
// Table resolver has 3 main arguments:
// - meta(ClientMeta): is the client returned by the plugin.Provider Configure call
// - parent(Resource): resource is the parent resource in case this table is called via parent table (i.e. relation)
// - res(chan any): is a channel to pass results fetched by the TableResolver
type TableResolver func(ctx context.Context, meta ClientMeta, parent *Resource, res chan<- any) error

type RowResolver func(ctx context.Context, meta ClientMeta, resource *Resource) error

type Multiplexer func(meta ClientMeta) []ClientMeta

type Transform func(table *Table) error

type Tables []*Table

// This is deprecated
type SyncSummary struct {
	Resources uint64
	Errors    uint64
	Panics    uint64
}

type TableColumnChangeType int

const (
	TableColumnChangeTypeUnknown TableColumnChangeType = iota
	TableColumnChangeTypeAdd
	TableColumnChangeTypeUpdate
	TableColumnChangeTypeRemove
)

type TableColumnChange struct {
	Type       TableColumnChangeType
	ColumnName string
	Current    Column
	Previous   Column
}

type Table struct {
	// Name of table
	Name string
	// Title to be used in documentation (optional: will be generated from name if not set)
	Title string
	// table description
	Description string
	// Columns are the set of fields that are part of this table
	Columns ColumnList
	// Relations are a set of related tables defines
	Relations Tables
	// Transform
	Transform Transform
	// Resolver is the main entry point to fetching table data and
	Resolver TableResolver
	// Multiplex returns re-purposed meta clients. The sdk will execute the table with each of them
	Multiplex Multiplexer
	// PostResourceResolver is called after all columns have been resolved, but before the Resource is sent to be inserted. The ordering of resolvers is:
	//  (Table) Resolver → PreResourceResolver → ColumnResolvers → PostResourceResolver
	PostResourceResolver RowResolver
	// PreResourceResolver is called before all columns are resolved but after Resource is created. The ordering of resolvers is:
	//  (Table) Resolver → PreResourceResolver → ColumnResolvers → PostResourceResolver
	PreResourceResolver RowResolver
	// IsIncremental is a flag that indicates if the table is incremental or not. This flag mainly affects how the table is
	// documented.
	IsIncremental bool

	// IgnoreInTests is used to exclude a table from integration tests.
	// By default, integration tests fetch all resources from cloudquery's test account, and verify all tables
	// have at least one row.
	// When IgnoreInTests is true, integration tests won't fetch from this table.
	// Used when it is hard to create a reproducible environment with a row in this table.
	IgnoreInTests bool

	// Parent is the parent table in case this table is called via parent table (i.e. relation)
	Parent *Table

	PkConstraintName string
}

var (
	reValidTableName  = regexp.MustCompile(`^[a-z_][a-z\d_]*$`)
	reValidColumnName = regexp.MustCompile(`^[a-z_][a-z\d_]*$`)
)

// AddCqIds adds the cq_id and cq_parent_id columns to the table and all its relations
// set cq_id as primary key if no other primary keys
func AddCqIDs(table *Table) {
	havePks := len(table.PrimaryKeys()) > 0
	cqIDColumn := CqIDColumn
	if !havePks {
		cqIDColumn.PrimaryKey = true
	}
	table.Columns = append(
		ColumnList{
			cqIDColumn,
			CqParentIDColumn,
		},
		table.Columns...,
	)
	for _, rel := range table.Relations {
		AddCqIDs(rel)
	}
}

func NewTablesFromArrowSchemas(schemas []*arrow.Schema) (Tables, error) {
	tables := make(Tables, len(schemas))
	for i, schema := range schemas {
		table, err := NewTableFromArrowSchema(schema)
		if err != nil {
			return nil, err
		}
		tables[i] = table
	}
	return tables, nil
}

// Create a CloudQuery Table abstraction from an arrow schema
// arrow schema is a low level representation of a table that can be sent
// over the wire in a cross-language way
func NewTableFromArrowSchema(sc *arrow.Schema) (*Table, error) {
	tableMD := sc.Metadata()
	name, found := tableMD.GetValue(MetadataTableName)
	if !found {
		return nil, fmt.Errorf("missing table name")
	}
	description, _ := tableMD.GetValue(MetadataTableDescription)
	constraintName, _ := tableMD.GetValue(MetadataConstraintName)
	fields := sc.Fields()
	columns := make(ColumnList, len(fields))
	for i, field := range fields {
		columns[i] = NewColumnFromArrowField(field)
	}
	table := &Table{
		Name:             name,
		Description:      description,
		PkConstraintName: constraintName,
		Columns:          columns,
	}
	if isIncremental, found := tableMD.GetValue(MetadataIncremental); found {
		table.IsIncremental = isIncremental == MetadataTrue
	}
	return table, nil
}

func (t TableColumnChangeType) String() string {
	switch t {
	case TableColumnChangeTypeAdd:
		return "add"
	case TableColumnChangeTypeUpdate:
		return "update"
	case TableColumnChangeTypeRemove:
		return "remove"
	default:
		return "unknown"
	}
}

func (t TableColumnChange) String() string {
	switch t.Type {
	case TableColumnChangeTypeAdd:
		return fmt.Sprintf("column: %s, type: %s, current: %s", t.ColumnName, t.Type, t.Current)
	case TableColumnChangeTypeUpdate:
		return fmt.Sprintf("column: %s, type: %s, current: %s, previous: %s", t.ColumnName, t.Type, t.Current, t.Previous)
	case TableColumnChangeTypeRemove:
		return fmt.Sprintf("column: %s, type: %s, previous: %s", t.ColumnName, t.Type, t.Previous)
	default:
		return fmt.Sprintf("column: %s, type: %s, current: %s, previous: %s", t.ColumnName, t.Type, t.Current, t.Previous)
	}
}

func (tt Tables) FilterDfsFunc(include, exclude func(*Table) bool, skipDependentTables bool) Tables {
	filteredTables := make(Tables, 0, len(tt))
	for _, t := range tt {
		filteredTable := t.Copy(nil)
		filteredTable = filteredTable.filterDfs(false, include, exclude, skipDependentTables)
		if filteredTable != nil {
			filteredTables = append(filteredTables, filteredTable)
		}
	}
	return filteredTables
}

func (tt Tables) ToArrowSchemas() Schemas {
	flattened := tt.FlattenTables()
	schemas := make(Schemas, len(flattened))
	for i, t := range flattened {
		schemas[i] = t.ToArrowSchema()
	}
	return schemas
}

func (tt Tables) FilterDfs(tables, skipTables []string, skipDependentTables bool) (Tables, error) {
	flattenedTables := tt.FlattenTables()
	for _, includePattern := range tables {
		matched := false
		for _, table := range flattenedTables {
			if glob.Glob(includePattern, table.Name) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("tables include a pattern %s with no matches", includePattern)
		}
	}
	for _, excludePattern := range skipTables {
		matched := false
		for _, table := range flattenedTables {
			if glob.Glob(excludePattern, table.Name) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("skip_tables include a pattern %s with no matches", excludePattern)
		}
	}
	include := func(t *Table) bool {
		for _, includePattern := range tables {
			if glob.Glob(includePattern, t.Name) {
				return true
			}
		}
		return false
	}
	exclude := func(t *Table) bool {
		for _, skipPattern := range skipTables {
			if glob.Glob(skipPattern, t.Name) {
				return true
			}
		}
		return false
	}
	return tt.FilterDfsFunc(include, exclude, skipDependentTables), nil
}

func (tt Tables) FlattenTables() Tables {
	tables := make(Tables, 0, len(tt))
	for _, t := range tt {
		table := *t
		table.Relations = nil
		tables = append(tables, &table)
		tables = append(tables, t.Relations.FlattenTables()...)
	}

	seen := make(map[string]struct{})
	deduped := make(Tables, 0, len(tables))
	for _, t := range tables {
		if _, found := seen[t.Name]; !found {
			deduped = append(deduped, t)
			seen[t.Name] = struct{}{}
		}
	}

	return slices.Clip(deduped)
}

func (tt Tables) TableNames() []string {
	ret := []string{}
	for _, t := range tt {
		ret = append(ret, t.TableNames()...)
	}
	return ret
}

// GetTopLevel returns a table by name. Only returns the table if it is in top-level list.
func (tt Tables) GetTopLevel(name string) *Table {
	for _, t := range tt {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// Get returns a table by name. Returns top-level tables and relations.
func (tt Tables) Get(name string) *Table {
	for _, t := range tt {
		if t.Name == name {
			return t
		}
		table := t.Relations.Get(name)
		if table != nil {
			return table
		}
	}
	return nil
}

func (tt Tables) ValidateDuplicateColumns() error {
	for _, t := range tt {
		if err := t.ValidateDuplicateColumns(); err != nil {
			return err
		}
	}
	return nil
}

func (tt Tables) ValidateDuplicateTables() error {
	tables := make(map[string]bool, len(tt))
	for _, t := range tt {
		if _, ok := tables[t.Name]; ok {
			return fmt.Errorf("duplicate table %s", t.Name)
		}
		tables[t.Name] = true
	}
	return nil
}

func (tt Tables) ValidateTableNames() error {
	for _, t := range tt {
		if err := t.ValidateName(); err != nil {
			return err
		}
		if err := t.Relations.ValidateTableNames(); err != nil {
			return err
		}
	}
	return nil
}

func (tt Tables) ValidateColumnNames() error {
	for _, t := range tt {
		if err := t.ValidateColumnNames(); err != nil {
			return err
		}
		if err := t.Relations.ValidateColumnNames(); err != nil {
			return err
		}
	}
	return nil
}

// this will filter the tree in-place
func (t *Table) filterDfs(parentMatched bool, include, exclude func(*Table) bool, skipDependentTables bool) *Table {
	if exclude(t) {
		return nil
	}
	matched := parentMatched && !skipDependentTables
	if include(t) {
		matched = true
	}
	filteredRelations := make([]*Table, 0, len(t.Relations))
	for _, r := range t.Relations {
		filteredChild := r.filterDfs(matched, include, exclude, skipDependentTables)
		if filteredChild != nil {
			matched = true
			filteredRelations = append(filteredRelations, r)
		}
	}
	t.Relations = filteredRelations
	if matched {
		return t
	}
	return nil
}

func (t *Table) ValidateName() error {
	ok := reValidTableName.MatchString(t.Name)
	if !ok {
		return fmt.Errorf("table name %q is not valid: table names must contain only lower-case letters, numbers and underscores, and must start with a lower-case letter or underscore", t.Name)
	}
	return nil
}

func (t *Table) PrimaryKeysIndexes() []int {
	var primaryKeys []int
	for i, c := range t.Columns {
		if c.PrimaryKey {
			primaryKeys = append(primaryKeys, i)
		}
	}

	return primaryKeys
}

func (t *Table) ToArrowSchema() *arrow.Schema {
	fields := make([]arrow.Field, len(t.Columns))
	md := map[string]string{
		MetadataTableName:        t.Name,
		MetadataTableDescription: t.Description,
		MetadataConstraintName:   t.PkConstraintName,
		MetadataIncremental:      MetadataFalse,
	}
	if t.IsIncremental {
		md[MetadataIncremental] = MetadataTrue
	}
	schemaMd := arrow.MetadataFrom(md)
	for i, c := range t.Columns {
		fields[i] = c.ToArrowField()
	}
	return arrow.NewSchema(fields, &schemaMd)
}

// Get Changes returns changes between two tables when t is the new one and old is the old one.
func (t *Table) GetChanges(old *Table) []TableColumnChange {
	var changes []TableColumnChange
	for _, c := range t.Columns {
		otherColumn := old.Columns.Get(c.Name)
		// A column was added to the table definition
		if otherColumn == nil {
			changes = append(changes, TableColumnChange{
				Type:       TableColumnChangeTypeAdd,
				ColumnName: c.Name,
				Current:    c,
			})
			continue
		}
		// Column type or options (e.g. PK, Not Null) changed in the new table definition
		if !arrow.TypeEqual(c.Type, otherColumn.Type) || c.NotNull != otherColumn.NotNull || c.PrimaryKey != otherColumn.PrimaryKey {
			changes = append(changes, TableColumnChange{
				Type:       TableColumnChangeTypeUpdate,
				ColumnName: c.Name,
				Current:    c,
				Previous:   *otherColumn,
			})
		}
	}
	// A column was removed from the table definition
	for _, c := range old.Columns {
		if t.Columns.Get(c.Name) == nil {
			changes = append(changes, TableColumnChange{
				Type:       TableColumnChangeTypeRemove,
				ColumnName: c.Name,
				Previous:   c,
			})
		}
	}
	return changes
}

func (t *Table) ValidateDuplicateColumns() error {
	columns := make(map[string]bool, len(t.Columns))
	for _, c := range t.Columns {
		if _, ok := columns[c.Name]; ok {
			return fmt.Errorf("duplicate column %s in table %s", c.Name, t.Name)
		}
		columns[c.Name] = true
	}
	for _, rel := range t.Relations {
		if err := rel.ValidateDuplicateColumns(); err != nil {
			return err
		}
	}
	return nil
}

func (t *Table) ValidateColumnNames() error {
	for _, c := range t.Columns {
		if !ValidColumnName(c.Name) {
			return fmt.Errorf("column name %q on table %q is not valid: column names must contain only lower-case letters, numbers and underscores, and must start with a lower-case letter or underscore", c.Name, t.Name)
		}
	}
	return nil
}

func (t *Table) Column(name string) *Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return &c
		}
	}
	return nil
}

// If the column with the same name exists, overwrites it.
// Otherwise, adds the column to the beginning of the table.
func (t *Table) OverwriteOrAddColumn(column *Column) {
	for i, c := range t.Columns {
		if c.Name == column.Name {
			t.Columns[i] = *column
			return
		}
	}
	t.Columns = append([]Column{*column}, t.Columns...)
}

func (t *Table) PrimaryKeys() []string {
	var primaryKeys []string
	for _, c := range t.Columns {
		if c.PrimaryKey {
			primaryKeys = append(primaryKeys, c.Name)
		}
	}

	return primaryKeys
}

func (t *Table) IncrementalKeys() []string {
	var incrementalKeys []string
	for _, c := range t.Columns {
		if c.IncrementalKey {
			incrementalKeys = append(incrementalKeys, c.Name)
		}
	}

	return incrementalKeys
}

func (t *Table) TableNames() []string {
	ret := []string{t.Name}
	for _, rel := range t.Relations {
		ret = append(ret, rel.TableNames()...)
	}
	return ret
}

func (t *Table) Copy(parent *Table) *Table {
	c := *t
	c.Parent = parent
	c.Columns = make([]Column, len(t.Columns))
	copy(c.Columns, t.Columns)
	c.Relations = make([]*Table, len(t.Relations))
	for i := range t.Relations {
		c.Relations[i] = t.Relations[i].Copy(&c)
	}
	return &c
}

func ValidColumnName(name string) bool {
	return reValidColumnName.MatchString(name)
}
