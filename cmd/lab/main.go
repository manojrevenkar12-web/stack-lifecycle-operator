// Command lab runs the upgrade lab and writes its reports.
//
//	go run ./cmd/lab sim --split dev        # tune against these seeds as often as you like
//	go run ./cmd/lab sim --split heldout    # once; writes results/HELDOUT.lock
//	go run ./cmd/lab report                 # regenerate README block, reports/RESULTS.md, dashboard data
//	go run ./cmd/lab report --check         # CI: fail if any generated file is stale
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/lab"
	"github.com/manojrevenkar12-web/stack-lifecycle-operator/internal/release"
)

const (
	configPath = "config/lab.yaml"
	lockPath   = "results/HELDOUT.lock"
	startMark  = "<!-- results:start -->"
	endMark    = "<!-- results:end -->"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lab sim --split dev|heldout | lab report [--check]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "sim":
		fs := flag.NewFlagSet("sim", flag.ExitOnError)
		split := fs.String("split", "dev", "dev or heldout")
		_ = fs.Parse(os.Args[2:])
		err = sim(*split)
	case "report":
		fs := flag.NewFlagSet("report", flag.ExitOnError)
		check := fs.Bool("check", false, "fail if generated files are out of date")
		_ = fs.Parse(os.Args[2:])
		err = report(*check)
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lab:", err)
		os.Exit(1)
	}
}

func sim(split string) error {
	c, raw, err := lab.LoadConfig(configPath)
	if err != nil {
		return err
	}
	var seeds []uint64
	switch split {
	case "dev":
		seeds = c.Seeds.Dev
	case "heldout":
		if _, err := os.Stat(lockPath); err == nil {
			return fmt.Errorf("the held-out split already ran (%s); it runs once so it cannot be tuned against", lockPath)
		}
		seeds = c.Seeds.Heldout
	default:
		return fmt.Errorf("split must be dev or heldout")
	}
	for _, s := range seeds {
		for _, d := range c.Seeds.Dev {
			if split == "heldout" && s == d {
				return fmt.Errorf("seed %d is in both splits", s)
			}
		}
	}
	res := lab.Run(c, raw, release.Default(), split, seeds)
	if err := writeJSON(filepath.Join("results", split+".json"), res); err != nil {
		return err
	}
	if split == "heldout" {
		lock := fmt.Sprintf("held-out split ran on %s\nconfig sha256 %s\nseeds %v\n", time.Now().UTC().Format(time.RFC3339), res.ConfigSHA256, seeds)
		if err := os.WriteFile(lockPath, []byte(lock), 0o644); err != nil {
			return err
		}
	}
	op := find(res, lab.Operator)
	fmt.Printf("%s: %d seeds × %d sites; operator first-try %.1f, broken %.1f, data loss %.1f per 100\n",
		split, len(seeds), c.Fleet.Sites, op.Metrics["first_try"].Mean, op.Metrics["broken"].Mean, op.Metrics["data_loss"].Mean)
	return nil
}

func report(check bool) error {
	_, raw, err := lab.LoadConfig(configPath)
	if err != nil {
		return err
	}
	var dev lab.Result
	if err := readJSON("results/dev.json", &dev); err != nil {
		return fmt.Errorf("run `lab sim --split dev` first: %w", err)
	}
	if dev.ConfigSHA256 != lab.Hash(raw) {
		return errors.New("results/dev.json was made with different settings; rerun `lab sim --split dev`")
	}
	var held *lab.Result
	var h lab.Result
	if err := readJSON("results/heldout.json", &h); err == nil {
		if h.ConfigSHA256 != lab.Hash(raw) {
			return errors.New("config/lab.yaml changed after the held-out split ran; the held-out numbers no longer describe these settings")
		}
		if _, err := os.Stat(lockPath); err != nil {
			return errors.New("results/heldout.json exists without results/HELDOUT.lock")
		}
		held = &h
	}
	main := dev
	if held != nil {
		main = *held
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		return err
	}
	block := lab.Markdown(main, otherSplit(main, dev, held))
	newReadme, err := replaceBlock(string(readme), block)
	if err != nil {
		return err
	}
	dashboard, err := json.MarshalIndent(map[string]any{"metrics": lab.Metrics, "dev": dev, "heldout": held}, "", "  ")
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"README.md":                   []byte(newReadme),
		"reports/RESULTS.md":          []byte(lab.Report(dev, held)),
		"dashboard/data/results.json": append(dashboard, '\n'),
	}
	stale := []string{}
	for path, want := range files {
		got, _ := os.ReadFile(path)
		if bytes.Equal(got, want) {
			continue
		}
		if check {
			stale = append(stale, path)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	if len(stale) > 0 {
		return fmt.Errorf("generated files are stale: %s; run `go run ./cmd/lab report`", strings.Join(stale, ", "))
	}
	return nil
}

func otherSplit(main, dev lab.Result, held *lab.Result) *lab.Result {
	if held != nil && main.Split == "heldout" {
		return &dev
	}
	return nil
}

func replaceBlock(doc, block string) (string, error) {
	i, j := strings.Index(doc, startMark), strings.Index(doc, endMark)
	if i < 0 || j < i {
		return "", fmt.Errorf("README.md needs %s and %s", startMark, endMark)
	}
	return doc[:i+len(startMark)] + "\n" + block + doc[j:], nil
}

func find(r lab.Result, d lab.Design) lab.DesignResult {
	for _, x := range r.Designs {
		if x.Design == d {
			return x
		}
	}
	return lab.DesignResult{}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
