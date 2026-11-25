package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
)

type options struct {
	interval  float64
	keyframes bool
	workers   int
	decode    string
	match     string
	output    string
	reference string
	player    string
}

type hit struct {
	seconds        float64
	added, visible int
}

type fileReport struct {
	path string
	hits []hit
	err  error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	opt := options{}
	flag.Float64Var(&opt.interval, "interval", 3, "seconds between samples in interval mode")
	flag.BoolVar(&opt.keyframes, "keyframes", false, "sample only video keyframes")
	flag.IntVar(&opt.workers, "workers", 1, "number of recordings to scan concurrently")
	flag.StringVar(&opt.decode, "decode", "auto", "auto (CUDA then CPU), cuda, or cpu")
	flag.StringVar(&opt.match, "match", "auto", "auto (template with OCR fallback), ocr, or template")
	flag.StringVar(&opt.output, "out", "juno-kills.csv", "destination CSV file")
	flag.StringVar(&opt.reference, "reference", "juno-reference.png", "calibration PNG path")
	flag.StringVar(&opt.player, "player", "", "kill-feed username for OCR")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: juno-scanner [options] <video or Replay directory> [...]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		return errors.New("provide at least one video or directory")
	}
	if opt.interval < .1 || math.IsNaN(opt.interval) || math.IsInf(opt.interval, 0) || opt.workers < 1 {
		return errors.New("interval must be finite and at least 0.1 seconds; workers must be positive")
	}
	if opt.decode != "auto" && opt.decode != "cuda" && opt.decode != "cpu" {
		return errors.New("decode must be auto, cuda, or cpu")
	}
	if opt.match != "auto" && opt.match != "ocr" && opt.match != "template" {
		return errors.New("match must be auto, ocr, or template")
	}
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("add %s to PATH: %w", binary, err)
		}
	}
	paths, err := collectPaths(flag.Args())
	if err != nil {
		return err
	}
	if err := checkOutput(opt.output, paths); err != nil {
		return err
	}
	if opt.reference == "juno-reference.png" {
		if _, err := os.Stat(opt.reference); os.IsNotExist(err) {
			if exe, err := os.Executable(); err == nil {
				opt.reference = filepath.Join(filepath.Dir(exe), opt.reference)
			}
		}
	}
	d, err := newDetector(opt.reference)
	if err != nil {
		return fmt.Errorf("load calibration PNG (-reference): %w", err)
	}
	if opt.match != "template" {
		if opt.player == "" {
			player, err := os.ReadFile(filepath.Join(filepath.Dir(opt.reference), "juno-player.txt"))
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("read juno-player.txt: %w", err)
			}
			opt.player = string(player)
		}
		opt.player = strings.ToUpper(strings.TrimSpace(opt.player))
		if opt.player == "" {
			if opt.match == "ocr" {
				return errors.New("OCR mode needs -player or juno-player.txt beside the calibration PNG")
			}
			fmt.Fprintln(os.Stderr, "Set -player or juno-player.txt to enable OCR fallback. Using template matching.")
			opt.match = "template"
		} else if strings.ContainsFunc(opt.player, func(r rune) bool {
			return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
		}) {
			return errors.New("OCR expects a player name with letters A–Z and digits 0–9; use -match template for other names")
		}
	}
	if opt.keyframes {
		fmt.Fprintf(os.Stderr, "Scanning %d video(s) at keyframes only\n", len(paths))
	} else {
		fmt.Fprintf(os.Stderr, "Scanning %d video(s) every %g seconds\n", len(paths), opt.interval)
	}
	jobs := make(chan int, len(paths))
	reports := make([]fileReport, len(paths))
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	var cache wazero.CompilationCache
	if opt.match != "template" {
		cache = wazero.NewCompilationCache()
		defer cache.Close(context.Background())
	}
	var workers sync.WaitGroup
	for range min(opt.workers, len(paths)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			var reader *nameReader
			var ocrErr error
			if cache != nil {
				reader, ocrErr = newNameReader(ctx, cache, opt.player)
				if reader != nil {
					defer reader.engine.Close(context.Background())
				}
			}
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				if ocrErr != nil {
					reports[i] = fileReport{path: paths[i], err: ocrErr}
				} else {
					reports[i] = scan(ctx, paths[i], d, reader, opt)
				}
			}
		}()
	}
	workers.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var hits, failures int
	for _, report := range reports {
		hits += len(report.hits)
		if report.err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "Skipped %s: %v\n", report.path, report.err)
		}
	}
	if failures == len(reports) {
		return errors.New("every video failed")
	}
	if err := writeCSV(opt.output, reports); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %d highlight timestamps to %s\n", hits, opt.output)
	if failures > 0 {
		return fmt.Errorf("%d recording(s) failed; the CSV contains results from successful scans", failures)
	}
	return nil
}

func collectPaths(inputs []string) ([]string, error) {
	var paths []string
	seen := make(map[string]bool)
	add := func(path string) {
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			paths = append(paths, path)
			seen[key] = true
		}
	}
	for _, input := range inputs {
		path, err := filepath.Abs(input)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("not a regular video file: %s", path)
			}
			add(path)
			continue
		}
		files, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		found := false
		for _, file := range files {
			name := file.Name()
			if file.IsDir() || !strings.HasPrefix(strings.ToLower(name), "replay") {
				continue
			}
			switch strings.ToLower(filepath.Ext(name)) {
			case ".mkv", ".mp4", ".mov", ".webm", ".avi", ".m4v", ".ts":
				add(filepath.Join(path, name))
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no Replay videos in %s", path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func checkOutput(output string, paths []string) error {
	if !strings.EqualFold(filepath.Ext(output), ".csv") {
		return errors.New("CSV destination must end in .csv")
	}
	dir, err := os.Stat(filepath.Dir(output))
	if err != nil {
		return err
	}
	if !dir.IsDir() {
		return fmt.Errorf("CSV parent path is not a directory: %s", filepath.Dir(output))
	}
	info, err := os.Stat(output)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("CSV destination is not a regular file: %s", output)
	}
	for _, path := range paths {
		if input, err := os.Stat(path); err == nil && os.SameFile(info, input) {
			return fmt.Errorf("CSV destination is also a source video: %s", path)
		}
	}
	return nil
}

func scan(ctx context.Context, path string, d *detector, reader *nameReader, opt options) fileReport {
	video, err := probe(ctx, path)
	if err != nil {
		return fileReport{path: path, err: err}
	}
	name := filepath.Base(path)
	report := fileReport{path: path}
	gray := make([]byte, frameWidth*frameHeight)
	var recognitionErr error
	maxGap := max(4, opt.interval+.15)
	if opt.keyframes {
		// Keyframes arrive at variable intervals. Keep nearby detections
		// comparable without carrying prior victims across long gaps.
		maxGap = 6
	}
	attempt := func(cuda bool) error {
		tracker := killTracker{maxGap: maxGap}
		nextProgress := 60.0
		mode := "CPU"
		if cuda {
			mode = "CUDA"
		}
		fmt.Fprintf(os.Stderr, "%s: scanning with %s\n", name, mode)
		result, err := sampleVideo(ctx, path, video, opt.interval, opt.keyframes, cuda, func(stamp float64, rgb []byte) error {
			current, err := matchWithOCR(ctx, reader, d, rgb, gray, opt.match)
			if err != nil {
				recognitionErr = err
				return err
			}
			if added := tracker.update(stamp, current); added > 0 {
				report.hits = append(report.hits, hit{stamp, added, len(current)})
			}
			if stamp >= nextProgress {
				fmt.Fprintf(os.Stderr, "%s: %s scanned, %d highlight timestamps\n",
					name, clockTime(stamp), len(report.hits))
				nextProgress = stamp + 60
			}
			return nil
		})
		if err == nil {
			fmt.Fprintf(os.Stderr, "%s: %d frames in %.1fs, %d highlight timestamps\n",
				name, result.frames, result.elapsed.Seconds(), len(report.hits))
		}
		return err
	}
	report.err = attempt(opt.decode != "cpu")
	if report.err != nil && opt.decode == "auto" && ctx.Err() == nil && recognitionErr == nil {
		fmt.Fprintf(os.Stderr, "%s: CUDA failed (%v), retrying on CPU\n", name, report.err)
		report.hits = nil
		report.err = attempt(false)
	}
	if report.err != nil {
		report.hits = nil
	}
	return report
}

type killTracker struct {
	maxGap   float64
	stamp    float64
	previous []detection
}

func (t *killTracker) update(stamp float64, current []detection) int {
	if stamp-t.stamp > t.maxGap {
		t.previous = nil
	}
	if len(current) == 0 {
		return 0
	}
	added := newKills(t.previous, current)
	t.stamp, t.previous = stamp, current
	return added
}

func newKills(previous, current []detection) int {
	// Match one row to at most one previous row. A second visible kill against
	// the same player therefore still adds a kill.
	type pair struct {
		before, after int
		similarity    float64
	}
	var matches []pair
	for i, old := range previous {
		for j, now := range current {
			if value := similarity(old.victim, now.victim); value >= .57 {
				matches = append(matches, pair{i, j, value})
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].similarity > matches[j].similarity })
	usedBefore := make([]bool, len(previous))
	usedAfter := make([]bool, len(current))
	added := len(current)
	for _, match := range matches {
		if !usedBefore[match.before] && !usedAfter[match.after] {
			usedBefore[match.before], usedAfter[match.after] = true, true
			added--
		}
	}
	return added
}

func clockTime(stamp float64) string {
	ms := int64(math.Round(max(0, stamp) * 1000))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func writeCSV(output string, reports []fileReport) error {
	file, err := os.CreateTemp(filepath.Dir(output), ".juno-kills-*.csv")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	writer := csv.NewWriter(file)
	_ = writer.Write([]string{"video_file", "timestamp", "seconds", "new_kills", "visible_juno_kills", "multi_kill", "video_path"})
	for _, report := range reports {
		for _, h := range report.hits {
			_ = writer.Write([]string{filepath.Base(report.path), clockTime(h.seconds),
				strconv.FormatFloat(h.seconds, 'f', 3, 64), strconv.Itoa(h.added), strconv.Itoa(h.visible),
				strconv.FormatBool(h.visible > 1), report.path})
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), output)
}
