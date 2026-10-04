// Package catalog holds the in-process table registry: schemas, data and
// statistics. Every structural or statistical change bumps a monotonically
// increasing version so that cached plans can be invalidated atomically.
package catalog

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/guoyyit/query-optimizer-go/rows"
	"github.com/guoyyit/query-optimizer-go/stats"
)

// Table is a registered in-process table.
type Table struct {
	Name     string
	Schema   rows.Schema
	Rows     []rows.Row
	Stats    *stats.TableStats // nil when statistics have never been collected
	HasStats bool
}

// Catalog is the process-wide table registry.
type Catalog struct {
	mu      sync.RWMutex
	tables  map[string]*Table
	version int64
}

// New creates an empty catalog.
func New() *Catalog {
	return &Catalog{tables: map[string]*Table{}}
}

// Version returns the current catalog version. It is bumped on every schema
// or statistics change.
func (c *Catalog) Version() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// AddTable registers a table and bumps the version.
func (c *Catalog) AddTable(t *Table) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(t.Name)
	if _, exists := c.tables[key]; exists {
		// replacing a table is a structural change
	}
	c.tables[key] = t
	c.version++
}

// Get returns a table by name (case-insensitive).
func (c *Catalog) Get(name string) (*Table, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tables[strings.ToLower(name)]
	return t, ok
}

// Names returns all table names in sorted order.
func (c *Catalog) Names() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, 0, len(c.tables))
	for n := range c.tables {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SetStats attaches statistics to a table and bumps the version.
func (c *Catalog) SetStats(name string, s *stats.TableStats) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tables[strings.ToLower(name)]
	if !ok {
		return fmt.Errorf("catalog: unknown table %q", name)
	}
	t.Stats = s
	t.HasStats = true
	c.version++
	return nil
}

// ---------- data loading ----------

// LoadCSV reads a CSV file with a header row, infers column types from the
// data, and registers it as a table. Empty fields become NULL.
func LoadCSV(c *Catalog, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return loadCSVReader(c, name, f)
}

func loadCSVReader(c *Catalog, name string, r io.Reader) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	records, err := cr.ReadAll()
	if err != nil {
		return fmt.Errorf("catalog: reading csv: %w", err)
	}
	if len(records) == 0 {
		return fmt.Errorf("catalog: csv file %q is empty", name)
	}
	header := records[0]
	ncols := len(header)
	schema := rows.Schema{Columns: make([]rows.Column, ncols)}
	for i, h := range header {
		schema.Columns[i] = rows.Column{Name: strings.ToLower(strings.TrimSpace(h)), Type: rows.TypeString}
	}

	// infer types from data rows
	colTypes := make([]rows.Type, ncols)
	for i := range colTypes {
		colTypes[i] = rows.TypeInt
	}
	for ri := 1; ri < len(records); ri++ {
		rec := records[ri]
		for ci := 0; ci < ncols; ci++ {
			var raw string
			if ci < len(rec) {
				raw = strings.TrimSpace(rec[ci])
			}
			if raw == "" {
				continue // NULL
			}
			t := inferType(raw)
			if t > colTypes[ci] { // type rank: null < int < float < string
				colTypes[ci] = t
			}
		}
	}
	for i := range schema.Columns {
		schema.Columns[i].Type = colTypes[i]
	}

	tableRows := make([]rows.Row, 0, len(records)-1)
	for ri := 1; ri < len(records); ri++ {
		rec := records[ri]
		vals := make([]rows.Value, ncols)
		for ci := 0; ci < ncols; ci++ {
			raw := ""
			if ci < len(rec) {
				raw = strings.TrimSpace(rec[ci])
			}
			vals[ci] = parseValue(raw, colTypes[ci])
		}
		tableRows = append(tableRows, rows.Row{Vals: vals})
	}

	c.AddTable(&Table{Name: name, Schema: schema, Rows: tableRows})
	return nil
}

func inferType(raw string) rows.Type {
	if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return rows.TypeInt
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return rows.TypeFloat
	}
	if raw == "true" || raw == "false" {
		return rows.TypeBool
	}
	return rows.TypeString
}

func parseValue(raw string, t rows.Type) rows.Value {
	if raw == "" {
		return rows.NullValue()
	}
	switch t {
	case rows.TypeInt:
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return rows.IntValue(v)
		}
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return rows.FloatValue(v)
		}
	case rows.TypeFloat:
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return rows.FloatValue(v)
		}
	case rows.TypeBool:
		if v, err := strconv.ParseBool(raw); err == nil {
			return rows.BoolValue(v)
		}
	}
	return rows.StringValue(raw)
}

// NewTable creates a table with an explicit schema and generated rows.
func NewTable(name string, schema rows.Schema, tableRows []rows.Row) *Table {
	return &Table{Name: name, Schema: schema, Rows: tableRows}
}

// Index is an in-process hash index on one column, mapping the string form
// of a key value to the row indices containing it.
type Index struct {
	Table   string
	Column  string
	Keys    map[string][]int
}

// EnsureIndex returns a hash index for (table, column), building it on first
// use. Indexes are derived data and do not bump the catalog version.
func (c *Catalog) EnsureIndex(table, column string) *Index {
	c.mu.RLock()
	t, ok := c.tables[strings.ToLower(table)]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	colIdx, found := t.Schema.Index(column)
	if !found {
		return nil
	}
	idx := &Index{Table: table, Column: column, Keys: map[string][]int{}}
	for ri, r := range t.Rows {
		if colIdx < len(r.Vals) && !r.Vals[colIdx].Null {
			key := r.Vals[colIdx].String()
			idx.Keys[key] = append(idx.Keys[key], ri)
		}
	}
	return idx
}
