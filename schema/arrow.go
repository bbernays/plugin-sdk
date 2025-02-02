package schema

import (
	"github.com/apache/arrow/go/v13/arrow"
)

const (
	MetadataUnique         = "cq:extension:unique"
	MetadataPrimaryKey     = "cq:extension:primary_key"
	MetadataConstraintName = "cq:extension:constraint_name"
	MetadataIncremental    = "cq:extension:incremental"

	MetadataTrue             = "true"
	MetadataFalse            = "false"
	MetadataTableName        = "cq:table_name"
	MetadataTableDescription = "cq:table_description"
)

type Schemas []*arrow.Schema

func (s Schemas) Len() int {
	return len(s)
}

func (s Schemas) SchemaByName(name string) *arrow.Schema {
	for _, sc := range s {
		tableName, ok := sc.Metadata().GetValue(MetadataTableName)
		if !ok {
			continue
		}
		if tableName == name {
			return sc
		}
	}
	return nil
}
