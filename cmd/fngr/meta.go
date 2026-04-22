package main

import (
	"context"
	"fmt"

	"github.com/monolithiclab/fngr/internal/parse"
)

type MetaCmd struct {
	List   MetaListCmd   `cmd:"" default:"withargs" help:"List all metadata keys and values."`
	Update MetaUpdateCmd `cmd:"" help:"Rename a metadata key=value pair."`
	Delete MetaDeleteCmd `cmd:"" help:"Delete a metadata key=value pair."`
}

type MetaListCmd struct{}

func (c *MetaListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	counts, err := s.ListMeta(ctx)
	if err != nil {
		return err
	}

	if len(counts) == 0 {
		fmt.Fprintln(io.Out, "No metadata found.")
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
		fmt.Fprintf(io.Out, "%-*s=%-*s  (%d)\n", maxKey, mc.Key, maxVal, mc.Value, mc.Count)
	}

	return nil
}

type MetaUpdateCmd struct {
	Old   string `arg:"" help:"Old key=value pair."`
	New   string `arg:"" help:"New key=value pair."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaUpdateCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	oldKey, oldValue, err := parse.KeyValue(c.Old)
	if err != nil {
		return err
	}
	newKey, newValue, err := parse.KeyValue(c.New)
	if err != nil {
		return err
	}

	count, err := s.CountMeta(ctx, oldKey, oldValue)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no metadata matching %s=%s", oldKey, oldValue)
	}

	if !c.Force {
		prompt := fmt.Sprintf("Update %d occurrence(s) of %s=%s to %s=%s? [Y/n] ", count, oldKey, oldValue, newKey, newValue)
		ok, err := confirm(io.In, io.Out, prompt)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	affected, err := s.UpdateMeta(ctx, oldKey, oldValue, newKey, newValue)
	if err != nil {
		return err
	}

	fmt.Fprintf(io.Out, "Updated %d occurrence(s)\n", affected)
	return nil
}

type MetaDeleteCmd struct {
	Meta  string `arg:"" help:"Metadata key=value to delete."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaDeleteCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	key, value, err := parse.KeyValue(c.Meta)
	if err != nil {
		return err
	}

	count, err := s.CountMeta(ctx, key, value)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no metadata matching %s=%s", key, value)
	}

	if !c.Force {
		prompt := fmt.Sprintf("Delete %d occurrence(s) of %s=%s? [Y/n] ", count, key, value)
		ok, err := confirm(io.In, io.Out, prompt)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	n, err := s.DeleteMeta(ctx, key, value)
	if err != nil {
		return err
	}

	fmt.Fprintf(io.Out, "Deleted %d occurrence(s)\n", n)
	return nil
}
