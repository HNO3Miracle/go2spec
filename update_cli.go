package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/sandrolain/httpcache"
	"golang.org/x/mod/semver"
)

type specInfo struct {
	FilePath      string
	GoImportPath  string
	Version       string
	Name          string
	Source0       string
	LatestVersion string
	HasUpdate     bool
	Error         string
}

func parseSpecFile(path string) (*specInfo, error) {
	if err := rejectAggregateUpdate(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info := &specInfo{FilePath: path}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "%define go_import_path") || strings.HasPrefix(line, "%global go_import_path") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				info.GoImportPath = parts[2]
			}
		}
		if strings.HasPrefix(line, "%define _name") || strings.HasPrefix(line, "%global _name") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				info.Name = parts[2]
			}
		}
		if strings.HasPrefix(line, "Version:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				info.Version = parts[1]
			}
		}
		if strings.HasPrefix(line, "Source0:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				info.Source0 = parts[1]
			}
		}
	}
	return info, scanner.Err()
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if semver.IsValid(v) {
		return v
	}
	return ""
}

func checkLatestVersion(ctx context.Context, spec *specInfo) {
	if err := rejectAggregateUpdate(spec.FilePath); err != nil {
		spec.Error = err.Error()
		spec.HasUpdate = false
		return
	}
	if spec.GoImportPath == "" {
		spec.Error = "no go_import_path"
		return
	}
	info, err := getPkgsiteInfo(ctx, spec.GoImportPath)
	if err != nil {
		spec.Error = fmt.Sprintf("pkgsite error: %v", err)
		return
	}
	latestPkgVer := info.Package.Version
	latestModVer := info.Module.Version
	latestVer := latestPkgVer
	if latestModVer != "" && (latestVer == "" || semver.Compare(normalizeVersion(latestModVer), normalizeVersion(latestVer)) > 0) {
		latestVer = latestModVer
	}
	spec.LatestVersion = latestVer
	currentNorm := normalizeVersion(spec.Version)
	latestNorm := normalizeVersion(latestVer)
	if currentNorm == "" || latestNorm == "" {
		spec.HasUpdate = false
		return
	}
	spec.HasUpdate = semver.Compare(latestNorm, currentNorm) > 0
}

func scanSpecDir(specDir string) ([]*specInfo, error) {
	entries, err := os.ReadDir(specDir)
	if err != nil {
		return nil, err
	}
	var specs []*specInfo
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "go-") {
			continue
		}
		specFiles, err := filepath.Glob(filepath.Join(specDir, entry.Name(), "*.spec"))
		if err != nil {
			continue
		}
		for _, sf := range specFiles {
			spec, err := parseSpecFile(sf)
			if errors.Is(err, errAggregateUpdateUnsupported) {
				return nil, err
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", sf, err)
				continue
			}
			if spec.GoImportPath != "" {
				specs = append(specs, spec)
			}
		}
	}
	return specs, nil
}

func printBox(title string, lines []string) {
	width := 56
	contentWidth := width - 4
	fmt.Println("+" + strings.Repeat("-", width) + "+")
	if title != "" {
		padding := width - len(title)
		left := padding / 2
		right := padding - left
		fmt.Printf("|%s%s%s|\n", strings.Repeat(" ", left), title, strings.Repeat(" ", right))
		fmt.Println("+" + strings.Repeat("-", width) + "+")
	}
	for _, line := range lines {
		if len(line) > contentWidth {
			line = line[:contentWidth-3] + "..."
		}
		fmt.Printf("|  %-*s  |\n", contentWidth, line)
	}
	fmt.Println("+" + strings.Repeat("-", width) + "+")
}

type outputMsg struct {
	text string
}

type counters struct {
	scanned    int32
	withUpdate int32
	scanErrors int32
	modified   int32
	processed  int32
	dlErrors   int32
	upErrors   int32
	total      int
}

type outputLoop struct {
	ch             chan outputMsg
	counts         *counters
	done           chan struct{}
	exited         chan struct{}
	lastProgress   []string
	interactiveTTY bool
	progressShown  int
}

func newOutputLoop(c *counters) *outputLoop {
	return &outputLoop{
		ch:             make(chan outputMsg, 64),
		counts:         c,
		done:           make(chan struct{}),
		exited:         make(chan struct{}),
		interactiveTTY: isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()),
	}
}

func (o *outputLoop) start() {
	go o.loop()
}

func (o *outputLoop) loop() {
	defer close(o.exited)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case msg := <-o.ch:
			o.printMessage(msg.text)
		case <-ticker.C:
			o.maybePrintProgress()
		case <-o.done:
			for {
				select {
				case msg := <-o.ch:
					o.printMessage(msg.text)
				default:
					o.clearProgress()
					return
				}
			}
		}
	}
}

func (o *outputLoop) maybePrintProgress() {
	progress := o.currentProgressLines()
	if sameProgressLines(progress, o.lastProgress) {
		return
	}
	o.lastProgress = append(o.lastProgress[:0], progress...)

	if o.interactiveTTY {
		o.renderProgress(progress)
		return
	}

	for _, line := range progress {
		fmt.Println(line)
	}
}

func (o *outputLoop) currentProgressLines() []string {
	c := o.counts
	scanned := atomic.LoadInt32(&c.scanned)
	withUpdate := atomic.LoadInt32(&c.withUpdate)
	scanErrors := atomic.LoadInt32(&c.scanErrors)
	processed := atomic.LoadInt32(&c.processed)
	modified := atomic.LoadInt32(&c.modified)
	dlErrors := atomic.LoadInt32(&c.dlErrors)
	upErrors := atomic.LoadInt32(&c.upErrors)

	return []string{
		fmt.Sprintf("  [scan]   %d/%d  updates:%d  errors:%d",
			scanned, c.total, withUpdate, scanErrors),
		fmt.Sprintf("  [verify] %d/%d  modified:%d  errors:%d",
			processed, withUpdate, modified, dlErrors+upErrors),
	}
}

func (o *outputLoop) print(text string) {
	o.ch <- outputMsg{text: text}
}

func (o *outputLoop) stop() {
	close(o.done)
	<-o.exited
}

func (o *outputLoop) printMessage(text string) {
	o.clearProgress()
	fmt.Println(text)
	if o.interactiveTTY {
		progress := o.currentProgressLines()
		o.lastProgress = append(o.lastProgress[:0], progress...)
		o.renderProgress(progress)
	}
}

func (o *outputLoop) clearProgress() {
	if o.interactiveTTY && o.progressShown > 0 {
		fmt.Print("\r\033[2K")
		for i := 1; i < o.progressShown; i++ {
			fmt.Print("\033[1A\r\033[2K")
		}
		o.progressShown = 0
	}
}

func (o *outputLoop) renderProgress(lines []string) {
	o.clearProgress()
	for i, line := range lines {
		if i > 0 {
			fmt.Print("\n")
		}
		fmt.Print(line)
	}
	o.progressShown = len(lines)
}

func sameProgressLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mainUpdate(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	concurrency := fs.Int("j", 3, "number of concurrent checks")
	dryRun := fs.Bool("n", false, "dry run: detect updates only, do not modify files")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s update [options] <SPECS_DIR>\n\n", os.Args[0])
		fmt.Fprintf(fs.Output(), "Check Go module spec files for updates.\n\n")
		fmt.Fprintf(fs.Output(), "Arguments:\n")
		fmt.Fprintf(fs.Output(), "  SPECS_DIR    directory containing go-* subdirectories\n\n")
		fmt.Fprintf(fs.Output(), "Options:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *concurrency < 1 {
		*concurrency = 1
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 1
	}

	specDir := fs.Arg(0)
	specs, err := scanSpecDir(specDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning spec dir: %v\n", err)
		return 1
	}
	if len(specs) == 0 {
		fmt.Fprintf(os.Stderr, "No spec files with go_import_path found in %s\n", specDir)
		return 1
	}

	pkgsiteHTTPClient = &http.Client{
		Timeout:   30 * time.Second,
		Transport: httpcache.NewMemoryCacheTransport(),
	}

	ctx := context.Background()
	total := len(specs)
	c := &counters{total: total}

	out := newOutputLoop(c)
	out.start()
	pkgsiteRetryLog = out.print
	downloadRetryLog = out.print

	fmt.Printf("Checking %d spec files for updates...\n", total)
	fmt.Printf("Parameters: spec_dir=%s  concurrency=%d  dry_run=%t\n", specDir, *concurrency, *dryRun)

	updateCh := make(chan *specInfo, *concurrency)

	var wg2 sync.WaitGroup
	if !*dryRun {
		for i := 0; i < *concurrency; i++ {
			wg2.Add(1)
			go func() {
				defer wg2.Done()
				for sp := range updateCh {
					err := verifyDownload(ctx, sp)
					if err != nil {
						sp.Error = err.Error()
						out.print(fmt.Sprintf("  FAIL:   %s - %v", sp.GoImportPath, err))
						atomic.AddInt32(&c.dlErrors, 1)
					} else {
						newVer := specVersionForVersion(sp.LatestVersion)
						remoteSHA256, err := remoteAssetSHA256(ctx, sp, newVer)
						updateRemoteAsset := true
						if errors.Is(err, errRemoteAssetSkip) {
							out.print(fmt.Sprintf("  SKIP:   %s remote asset sha256 - %v", sp.GoImportPath, err))
							updateRemoteAsset = false
							err = nil
						}
						if err != nil {
							sp.Error = err.Error()
							out.print(fmt.Sprintf("  FAIL:   %s - %v", sp.GoImportPath, err))
							atomic.AddInt32(&c.upErrors, 1)
						} else if err := updateSpecVersion(sp, newVer, remoteSHA256, updateRemoteAsset); err != nil {
							sp.Error = err.Error()
							out.print(fmt.Sprintf("  FAIL:   %s - %v", sp.GoImportPath, err))
							atomic.AddInt32(&c.upErrors, 1)
						} else {
							atomic.AddInt32(&c.modified, 1)
							out.print(fmt.Sprintf("  OK:     %s updated to %s", sp.GoImportPath, newVer))
						}
					}
					atomic.AddInt32(&c.processed, 1)
				}
			}()
		}
	}

	var wg1 sync.WaitGroup
	sem1 := make(chan struct{}, *concurrency)

	for _, spec := range specs {
		wg1.Add(1)
		sem1 <- struct{}{}
		go func(s *specInfo) {
			defer wg1.Done()
			defer func() { <-sem1 }()

			checkLatestVersion(ctx, s)

			atomic.AddInt32(&c.scanned, 1)
			needSend := false

			if s.Error != "" {
				atomic.AddInt32(&c.scanErrors, 1)
				out.print(fmt.Sprintf("  ERROR:  %s - %s", s.GoImportPath, s.Error))
			} else if s.HasUpdate {
				atomic.AddInt32(&c.withUpdate, 1)
				out.print(fmt.Sprintf("  UPDATE: %-50s  %s -> %s", s.GoImportPath, s.Version, s.LatestVersion))
				needSend = true
			}

			if needSend && !*dryRun {
				updateCh <- s
			}
		}(spec)
	}

	wg1.Wait()
	close(updateCh)

	if !*dryRun {
		wg2.Wait()
	}

	pkgsiteRetryLog = nil
	downloadRetryLog = nil
	out.stop()

	var errSpecs []*specInfo
	for _, s := range specs {
		if s.Error != "" {
			errSpecs = append(errSpecs, s)
		}
	}

	summaryLines := []string{
		fmt.Sprintf("%-12s  %d/%d", "Scanned:", total, total),
		fmt.Sprintf("%-12s  %d", "Updates:", atomic.LoadInt32(&c.withUpdate)),
		fmt.Sprintf("%-12s  %d", "Modified:", atomic.LoadInt32(&c.modified)),
	}
	if atomic.LoadInt32(&c.scanErrors) > 0 || atomic.LoadInt32(&c.dlErrors) > 0 || atomic.LoadInt32(&c.upErrors) > 0 {
		totalErrors := atomic.LoadInt32(&c.scanErrors) + atomic.LoadInt32(&c.dlErrors) + atomic.LoadInt32(&c.upErrors)
		summaryLines = append(summaryLines, fmt.Sprintf("%-12s  %d", "Errors:", totalErrors))
	}
	if *dryRun {
		summaryLines = append(summaryLines, fmt.Sprintf("%-12s  %s", "Mode:", "dry run"))
	}
	fmt.Println()
	printBox("SUMMARY", summaryLines)

	if atomic.LoadInt32(&c.withUpdate) == 0 {
		fmt.Println("\nAll modules are up to date.")
	} else if *dryRun {
		fmt.Println("\nDry run complete. No files were modified.")
	} else {
		fmt.Println()
		printBox("UPDATE RESULT", []string{
			fmt.Sprintf("%-12s  %d", "Attempted:", atomic.LoadInt32(&c.withUpdate)),
			fmt.Sprintf("%-12s  %d", "Modified:", atomic.LoadInt32(&c.modified)),
			fmt.Sprintf("%-12s  %d", "Dl errors:", atomic.LoadInt32(&c.dlErrors)),
			fmt.Sprintf("%-12s  %d", "Up errors:", atomic.LoadInt32(&c.upErrors)),
		})
	}

	if len(errSpecs) > 0 {
		fmt.Printf("\n=== %d modules with errors ===\n", len(errSpecs))
		for _, s := range errSpecs {
			fmt.Printf("  %-50s  %s\n", s.GoImportPath, s.Error)
		}
	}

	return 0
}
