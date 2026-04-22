package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/monolithiclab/fngr/internal"
)

type MetaCmd struct {
	List   MetaListCmd   `cmd:"" default:"withargs" help:"List all metadata keys and values."`
	Update MetaUpdateCmd `cmd:"" help:"Rename a metadata key=value pair."`
	Delete MetaDeleteCmd `cmd:"" help:"Delete a metadata key=value pair."`
}

type MetaListCmd struct{}

func (c *MetaListCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	counts, err := internal.ListMeta(ctx, db)
	if err != nil {
		return err
	}

	if len(counts) == 0 {
		fmt.Println("No metadata found.")
		return nil
	}

	maxKey, maxVal := 0, 0
	for _, mc := range counts {
		if len(mc.Key) > maxKey {
			maxKey = len(mc.Key)
		}
		if len(mc.Value) > maxVal {
			maxVal = len(mc.Value)
		}
	}

	for _, mc := range counts {
		fmt.Fprintf(os.Stdout, "%-*s=%-*s  (%d)\n", maxKey, mc.Key, maxVal, mc.Value, mc.Count)
	}

	return nil
}

type MetaUpdateCmd struct {
	Old   string `arg:"" help:"Old key=value pair."`
	New   string `arg:"" help:"New key=value pair."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaUpdateCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	oldKey, oldValue, ok := strings.Cut(c.Old, "=")
	if !ok {
		return fmt.Errorf("invalid old meta %q: expected key=value", c.Old)
	}
	newKey, newValue, ok := strings.Cut(c.New, "=")
	if !ok {
		return fmt.Errorf("invalid new meta %q: expected key=value", c.New)
	}

	affected, err := internal.UpdateMeta(ctx, db, oldKey, oldValue, newKey, newValue)
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("no metadata matching %s=%s", oldKey, oldValue)
	}

	if !c.Force {
		fmt.Printf("Update %d occurrence(s) of %s=%s to %s=%s? [Y/n] ", affected, oldKey, oldValue, newKey, newValue)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	affected, err = internal.UpdateMeta(ctx, db, oldKey, oldValue, newKey, newValue)
	if err != nil {
		return err
	}

	fmt.Printf("Updated %d occurrence(s)\n", affected)
	return nil
}

type MetaDeleteCmd struct {
	Meta  string `arg:"" help:"Metadata key=value to delete."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaDeleteCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	key, value, ok := strings.Cut(c.Meta, "=")
	if !ok {
		return fmt.Errorf("invalid meta %q: expected key=value", c.Meta)
	}

	// Dry-run count first
	counts, err := internal.ListMeta(ctx, db)
	if err != nil {
		return err
	}
	var affected int
	for _, mc := range counts {
		if mc.Key == key && mc.Value == value {
			affected = mc.Count
			break
		}
	}
	if affected == 0 {
		return fmt.Errorf("no metadata matching %s=%s", key, value)
	}

	if !c.Force {
		fmt.Printf("Delete %d occurrence(s) of %s=%s? [Y/n] ", affected, key, value)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	n, err := internal.DeleteMeta(ctx, db, key, value)
	if err != nil {
		return err
	}

	fmt.Printf("Deleted %d occurrence(s)\n", n)
	return nil
}
