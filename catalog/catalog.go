// Package catalog holds in-memory tables, their statistics and a global
// epoch used for plan-cache invalidation. All methods are goroutine-safe.
//
// Every mutation (create table, insert, set stats, create index) bumps the
// catalog epoch; cached plans record the epoch they were built against and
// are discarded once it changes.
package catalog

import (
	"fmt"
	"sync"
	"time"

	"github.com/guoyyit/query-optimizer-go/value"
)

// Column describes one table column.
type Column struct {
	Name string
	Type value.Type
}

// Stats are table-level statistics. A nil *Stats means "unknown".
type Stats struct {
	RowCount  int64
	NDV       map[string]int64       // column -> distinct count
	Min, Max  map[string]value.Value // column -> bounds
	UpdatedAt time.Time
}

// Table is an in-memory relation.
type Table struct {
	Name    string
	Columns []Column

	mu       sync.RWMutex
	rows     [][]value.Value
	stats    *Stats
	statsVer uint64
	indexes  map[string]*HashIndex
	cat      *Catalog
}

// HashIndex maps an encoded value to row offsets for equality lookups.
type HashIndex struct {
	Column string
	M      map[uint64][]int
}

// Catalog is a collection of tables plus a mutation epoch.
type Catalog struct {
	mu     sync.RWMutex
	tables map[string]*Table
	epoch  uint64
}

func New() *Catalog {
	return &Catalog{tables: map[string]*Table{}}
}

// Epoch returns the current catalog version.
func (c *Catalog) Epoch() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.epoch
}

func (c *Catalog) bump() {
	c.mu.Lock()
	c.epoch++
	c.mu.Unlock()
}

// CreateTable registers a new empty table. It bumps the epoch.
func (c *Catalog) CreateTable(name string, cols []Column) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.tables[name]; dup {
		return fmt.Errorf("catalog: table %q already exists", name)
	}
	seen := map[string]bool{}
	for _, col := range cols {
		if seen[col.Name] {
			return fmt.Errorf("catalog: duplicate column %q in table %q", col.Name, name)
		}
		seen[col.Name] = true
	}
	c.tables[name] = &Table{Name: name, Columns: cols, indexes: map[string]*HashIndex{}, cat: c}
	c.epoch++
	return nil
}

// Table returns the named table or nil.
func (c *Catalog) Table(name string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tables[name]
}

// Insert appends a row, maintains indexes and bumps the epoch.
func (t *Table) Insert(row []value.Value) error {
	if len(row) != len(t.Columns) {
		return fmt.Errorf("catalog: insert into %s: got %d values, want %d", t.Name, len(row), len(t.Columns))
	}
	t.mu.Lock()
	pos := len(t.rows)
	t.rows = append(t.rows, row)
	for _, idx := range t.indexes {
		colPos := t.colPos(idx.Column)
		k := row[colPos].HashKey()
		idx.M[k] = append(idx.M[k], pos)
	}
	t.mu.Unlock()
	t.cat.bump()
	return nil
}

// Rows returns a snapshot of the table rows.
func (t *Table) Rows() [][]value.Value {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([][]value.Value, len(t.rows))
	copy(out, t.rows)
	return out
}

// SetStats replaces table statistics (nil clears them) and bumps the epoch.
// A zero UpdatedAt is stamped with the current time.
func (t *Table) SetStats(s *Stats) {
	if s != nil && s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	}
	t.mu.Lock()
	t.stats = s
	t.statsVer++
	t.mu.Unlock()
	t.cat.bump()
}

// Stats returns the current statistics (nil when unknown) and their version.
func (t *Table) Stats() (*Stats, uint64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.stats, t.statsVer
}

// CreateIndex builds a hash index on the given column.
func (t *Table) CreateIndex(column string) error {
	t.mu.Lock()
	pos := -1
	for i, c := range t.Columns {
		if c.Name == column {
			pos = i
			break
		}
	}
	if pos < 0 {
		t.mu.Unlock()
		return fmt.Errorf("catalog: no column %q in table %q", column, t.Name)
	}
	idx := &HashIndex{Column: column, M: map[uint64][]int{}}
	for i, row := range t.rows {
		k := row[pos].HashKey()
		idx.M[k] = append(idx.M[k], i)
	}
	t.indexes[column] = idx
	t.mu.Unlock()
	t.cat.bump()
	return nil
}

// Index returns the hash index on column, or nil.
func (t *Table) Index(column string) *HashIndex {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.indexes[column]
}

// ColumnPos returns the position of a column, or -1.
func (t *Table) ColumnPos(name string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.colPos(name)
}

func (t *Table) colPos(name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	return -1
}
