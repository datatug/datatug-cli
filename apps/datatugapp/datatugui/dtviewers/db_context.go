package dtviewers

import (
	"context"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/datatug-core/schemer"
)

type DbContext interface {
	Name() string // A db name. For SQLite would be a file name
	Driver() Driver
	Schema() schemer.SchemaProvider
	GetDB(ctx context.Context) (db dal.DB, err error)
}

type Driver struct {
	ID         string
	ShortTitle string
}

var _ DbContext = (*DbContextBase)(nil)

type DbContextBase struct {
	name   string
	driver Driver
	schema schemer.SchemaProvider
	getDB  func(ctx context.Context) (dal.DB, error)
}

func (c *DbContextBase) Name() string {
	return c.name
}

func (c *DbContextBase) Driver() Driver {
	return c.driver
}

func (c *DbContextBase) Schema() schemer.SchemaProvider {
	return c.schema
}

func (c *DbContextBase) GetDB(ctx context.Context) (db dal.DB, err error) {
	return c.getDB(ctx)
}
