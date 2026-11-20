package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Top-Ranger/whitegopher/internal/importer"
	"github.com/Top-Ranger/whitegopher/internal/store"
)

func importTree(st *store.Store, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "read and validate only; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: import [-dry-run] DIR")
	}
	res, err := importer.Import(fs.Arg(0), st, importer.Options{DryRun: *dry})
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	fmt.Printf("users %d, calendars %d, events %d, address books %d, cards %d (dry-run: %v)\n",
		res.Users, res.Calendars, res.Events, res.AddressBooks, res.Cards, *dry)
	return err
}
