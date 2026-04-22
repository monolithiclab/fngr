package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

type MetaCmd struct {
	List   MetaListCmd   `cmd:"" default:"withargs" help:"List metadata, optionally filtered (default)."`
	Rename MetaRenameCmd `cmd:"" help:"Rename a metadata entry across all events."`
	Delete MetaDeleteCmd `cmd:"" help:"Delete a metadata entry across all events."`
}

type MetaListCmd struct {
	Search string `help:"Filter: bare key (e.g. 'tag'), key=value, @person, or #tag." short:"S"`
}

func (c *MetaListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	var opts event.ListMetaOpts
	if c.Search != "" {
		m, err := parseMetaFilter(c.Search)
		if err != nil {
			return err
		}
		opts.Key = m.Key
		opts.Value = m.Value
	}

	counts, err := s.ListMeta(ctx, opts)
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

type MetaRenameCmd struct {
	Old   string `arg:"" help:"Old entry: key=value, @person, or #tag."`
	New   string `arg:"" help:"New entry: same forms as <old>."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaRenameCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	oldM, err := parse.MetaArg(c.Old)
	if err != nil {
		return err
	}
	newM, err := parse.MetaArg(c.New)
	if err != nil {
		return err
	}

	count, err := s.CountMeta(ctx, oldM.Key, oldM.Value)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no metadata matching %s=%s", oldM.Key, oldM.Value)
	}

	if !c.Force {
		prompt := fmt.Sprintf("Rename %d occurrence(s) of %s=%s to %s=%s? [Y/n] ",
			count, oldM.Key, oldM.Value, newM.Key, newM.Value)
		ok, err := confirm(io.In, io.Out, prompt, true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	affected, err := s.UpdateMeta(ctx, oldM.Key, oldM.Value, newM.Key, newM.Value)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Renamed %d occurrence(s)\n", affected)
	return nil
}

type MetaDeleteCmd struct {
	Meta  string `arg:"" help:"Entry to delete: key=value, @person, or #tag."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaDeleteCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	m, err := parse.MetaArg(c.Meta)
	if err != nil {
		return err
	}

	count, err := s.CountMeta(ctx, m.Key, m.Value)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no metadata matching %s=%s", m.Key, m.Value)
	}

	if !c.Force {
		prompt := fmt.Sprintf("Delete %d occurrence(s) of %s=%s? [y/N] ", count, m.Key, m.Value)
		ok, err := confirm(io.In, io.Out, prompt, false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	n, err := s.DeleteMeta(ctx, m.Key, m.Value)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Deleted %d occurrence(s)\n", n)
	return nil
}

// parseMetaFilter accepts the same shorthand as parse.MetaArg
// (@person, #tag, key=value) plus a bare key (e.g. "tag") that filters
// by key only. Returned Meta carries an empty Value for the bare-key
// case. Used only by MetaListCmd's -S filter.
func parseMetaFilter(s string) (parse.Meta, error) {
	if s == "" {
		return parse.Meta{}, nil
	}
	if s[0] != '@' && s[0] != '#' && !strings.Contains(s, "=") {
		if !parse.MetaNameRe.MatchString(s) {
			return parse.Meta{}, fmt.Errorf("invalid filter %q: bare key must match [\\w][\\w/\\-]*", s)
		}
		return parse.Meta{Key: s}, nil
	}
	return parse.MetaArg(s)
}
