package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
)

func mainModuleSelection(args []string) int {
	flags := flag.NewFlagSet("module-select", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprintln(os.Stderr, moduleSelectionUsage()) }
	var packages stringListFlag
	var format string
	flags.Var(&packages, "package", "Analyze this exact package import path in the project module; may be repeated")
	flags.StringVar(&format, "format", "text", "Output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if format != "text" && format != "json" {
		log.Printf("module-select: unsupported format %q", format)
		return 2
	}
	if flags.NArg() != 1 {
		log.Print("module-select: provide exactly one project module@canonical-version or module@full-commit-sha")
		return 2
	}
	request, err := parseModuleSelectionRequest(flags.Arg(0))
	if err != nil {
		log.Printf("module-select: %v", err)
		return 2
	}
	if _, err := validateModuleSelectionPackages(request.Path, packages); err != nil {
		log.Printf("module-select: %v", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	work, err := os.MkdirTemp("", "go2spec-module-select-")
	if err != nil {
		log.Printf("module-select: create private work directory: %v", err)
		return 1
	}
	defer func() {
		if err := removeAggregateWork(work); err != nil {
			log.Printf("module-select: cannot clean private work directory: %v", err)
		}
	}()
	report, err := moduleSelectionReportFor(ctx, work, request, packages)
	if err != nil {
		log.Printf("module-select: %v", err)
		return 1
	}
	if format == "json" {
		data, err := moduleSelectionReportJSON(report)
		if err != nil {
			log.Printf("module-select: encode report: %v", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	fmt.Println(renderModuleSelectionReport(report))
	return 0
}

func moduleSelectionUsage() string {
	return strings.TrimSpace(`Usage: go2spec module-select [--package import/path]... [--format text|json] module@version-or-full-commit-sha

Analyze packages in one explicitly pinned Go module. This command reports advisory
module candidates only; it does not select a final package set or modify files.`)
}
