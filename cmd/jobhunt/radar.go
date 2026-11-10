package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"jobhunt/internal/radar"
)

func radarCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jobhunt radar run [--fixtures <dir>] [--all] [--dry-run] | jobhunt radar import <job-radar folder>")
	}
	switch args[0] {
	case "run":
		return radarRun(args[1:])
	case "import":
		if len(args) != 2 {
			return errors.New("usage: jobhunt radar import <job-radar folder>")
		}
		return radarImport(args[1])
	}
	return fmt.Errorf("unknown radar command %q", args[0])
}

// radarRun works while serve runs: both go through the same database.
func radarRun(args []string) error {
	fl := flag.NewFlagSet("radar run", flag.ExitOnError)
	fixtures := fl.String("fixtures", "", "read each board from <dir>/<source>.json|xml instead of the network")
	all := fl.Bool("all", false, "re-score listings already seen")
	dry := fl.Bool("dry-run", false, "print the matches without saving anything")
	fl.Parse(args)

	s, _, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sum, err := (&radar.Radar{Store: s}).Run(ctx, radar.Options{Fixtures: *fixtures, All: *all, DryRun: *dry})
	for _, src := range sum.Sources {
		switch {
		case src.Error != "":
			fmt.Printf("  %-18s failed: %s\n", src.Name, src.Error)
		case src.Skipped != "":
			fmt.Printf("  %-18s not checked: %s\n", src.Name, src.Skipped)
		default:
			fmt.Printf("  %-18s %d listings\n", src.Name, src.Count)
		}
	}
	for _, p := range sum.Problems {
		fmt.Printf("  %-18s failed: %s\n", p.Name, p.Error)
	}
	if err != nil {
		return err
	}
	fmt.Printf("\n%d new lead(s) out of %d new listings", sum.Matches, sum.Scanned)
	if sum.Hidden > 0 {
		fmt.Printf("; %d more already on the board", sum.Hidden)
	}
	fmt.Println(".")
	for _, l := range sum.Leads {
		fmt.Printf("\n%3d  %s — %s\n     %s · %s · via %s", l.Score, l.Title, l.Company, l.Posted, l.Location, l.SourceName)
		if len(l.AlsoOn) > 0 {
			fmt.Printf(" (also on %s)", strings.Join(l.AlsoOn, ", "))
		}
		fmt.Printf("\n     %s\n", l.URL)
		if len(l.Flags) > 0 {
			fmt.Printf("     ⚠ %s\n", strings.Join(l.Flags, ", "))
		}
	}
	return nil
}

func radarImport(dir string) error {
	s, _, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	rep, err := (&radar.Radar{Store: s}).Import(context.Background(), dir)
	if err != nil {
		return err
	}
	fmt.Printf("settings  config.json %s, skills.json %s\n", rep.Config, rep.Skills)
	fmt.Printf("leads     %d, seen listings %d, skill observations %d\n", rep.Leads, rep.Seen, rep.Observations)
	fmt.Printf("board     %d skipped and %d applied added, %d already on the board\n", rep.Skipped, rep.Applied, rep.AlreadyOnBoard)
	for _, p := range rep.Problems {
		fmt.Println("not imported:", p)
	}
	return nil
}
