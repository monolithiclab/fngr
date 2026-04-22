package internal

import "database/sql"

type AddCmd struct {
	Text   string   `arg:"" help:"Event text."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID."`
	Meta   []string `help:"Metadata key=value pairs." short:"m"`
}

type ListCmd struct {
	Filter string `arg:"" optional:"" help:"Filter expression."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"Output format." enum:"table,json,csv" default:"table"`
	Tree   bool   `help:"Show events as a tree." default:"true" negatable:""`
}

type ShowCmd struct {
	ID   int64 `arg:"" help:"Event ID."`
	Tree bool  `help:"Show subtree." default:"false"`
}

type DeleteCmd struct {
	ID int64 `arg:"" help:"Event ID."`
}

type MetaCmd struct{}

func (c *AddCmd) Run(db *sql.DB) error    { return nil }
func (c *ListCmd) Run(db *sql.DB) error   { return nil }
func (c *ShowCmd) Run(db *sql.DB) error   { return nil }
func (c *DeleteCmd) Run(db *sql.DB) error { return nil }
func (c *MetaCmd) Run(db *sql.DB) error   { return nil }
