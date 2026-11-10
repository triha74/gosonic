package lib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Braille "dots" frames, one cell wide. Same cycle as the common CLI dots spinner.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

const (
	cursorUp   = "\033[%dA"
	clearLine  = "\r\033[K"
	clearToEnd = "\033[K"

	// statusColumnWidth fits "cancelled". durationColumnWidth fits "999.9s".
	statusColumnWidth   = 9
	durationColumnWidth = 6

	// spinnerInterval matches the usual pace for a 10-frame braille cycle.
	spinnerInterval = 80 * time.Millisecond
)

// stdoutIsTTY reports whether progress may animate. Tests replace it.
var stdoutIsTTY = func() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// StageOutputError carries container stdout/stderr with a stage failure so
// quiet mode can print the log after the progress line.
type StageOutputError struct {
	Err    error
	Stdout string
	Stderr string
}

func (e *StageOutputError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *StageOutputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ProgressSet coordinates quiet status lines for one `gosonic run`.
// Parallel stages share a set so each running job has its own spinner line.
type ProgressSet struct {
	verbose   bool
	prefix    bool
	tty       bool
	nameWidth int

	started time.Time

	mu     sync.Mutex
	live   []*Progress
	drawn  int
	inline bool // the spinner is on the current line and has not printed a newline
	frame  int
	stop   chan struct{}
	done   chan struct{}
	closed bool
}

// active is the set currently drawing, so a warning can leave the spinner
// line before it prints.
var (
	activeMu sync.Mutex
	active   *ProgressSet
)

// note prints a line of its own. When a spinner is on the current line, it
// clears that line first and draws the spinner again underneath.
func note(line string) {
	activeMu.Lock()
	s := active
	activeMu.Unlock()
	if s == nil {
		fmt.Fprintln(os.Stderr, line)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inline {
		fmt.Fprint(os.Stdout, clearLine)
		s.inline = false
	} else {
		s.eraseLiveLocked()
	}
	fmt.Fprintln(os.Stderr, line)
	s.drawLiveLocked()
}

// Progress is one stage's quiet status line.
type Progress struct {
	name    string
	start   time.Time
	set     *ProgressSet
	owned   bool
	enabled bool
}

// NewProgressSet builds a reporter for a run. prefix is true for --jobs > 1.
// names are the stage labels for this run; parallel status lines pad to the
// longest one so the separator, status, and duration form columns.
func NewProgressSet(verbose, prefix bool, names ...string) *ProgressSet {
	s := &ProgressSet{
		verbose:   verbose,
		prefix:    prefix,
		tty:       !verbose && stdoutIsTTY(),
		nameWidth: nameColumnWidth(names),
		started:   time.Now(),
	}
	if s.tty {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.loop()
	}
	activeMu.Lock()
	active = s
	activeMu.Unlock()
	return s
}

func (s *ProgressSet) loop() {
	defer close(s.done)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			s.frame++
			s.drawLiveLocked()
			s.mu.Unlock()
		}
	}
}

// Close stops the spinner ticker.
func (s *ProgressSet) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	already := s.closed
	s.closed = true
	stop := s.stop
	s.mu.Unlock()
	if !already && stop != nil {
		close(stop)
		<-s.done
	}
	activeMu.Lock()
	if active == s {
		active = nil
	}
	activeMu.Unlock()
}

// Start adds a spinner line as soon as the stage begins.
func (s *ProgressSet) Start(name string) *Progress {
	p := &Progress{name: name, start: time.Now(), set: s, enabled: s != nil && !s.verbose}
	if s == nil || !p.enabled {
		return p
	}
	s.mu.Lock()
	s.live = append(s.live, p)
	s.drawLiveLocked()
	s.mu.Unlock()
	return p
}

// Finish replaces the spinner with a final status and dumps captured logs.
func (s *ProgressSet) Finish(p *Progress, status string, err error) {
	if s == nil || p == nil || !p.enabled {
		return
	}
	s.mu.Lock()
	s.removeLiveLocked(p)
	s.commitLineLocked(s.statusLine(p.name, status, time.Since(p.start)))
	if !errors.Is(err, context.Canceled) {
		writeCapturedOutputLocked(p.name, err, s.prefix)
	}
	s.drawLiveLocked()
	s.mu.Unlock()
}

// Summarize prints the run status and wall time. Call it after the stages
// have finished, including when the run failed. Wall time starts when the
// set was created, so parallel stages count once.
func (s *ProgressSet) Summarize(err error) {
	if s == nil {
		return
	}
	status := "ok"
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		status = "cancelled"
	default:
		status = "failed"
	}
	s.mu.Lock()
	if s.inline {
		fmt.Fprintln(os.Stdout)
		s.inline = false
	} else {
		s.eraseLiveLocked()
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, s.statusLine("run", status, time.Since(s.started)))
	s.mu.Unlock()
}

// Skip writes a skipped line without a spinner.
func (s *ProgressSet) Skip(name string) {
	if s == nil || s.verbose {
		return
	}
	s.mu.Lock()
	s.commitLineLocked(s.statusLine(name, "skipped", 0))
	s.drawLiveLocked()
	s.mu.Unlock()
}

func (s *ProgressSet) removeLiveLocked(p *Progress) {
	out := s.live[:0]
	for _, item := range s.live {
		if item != p {
			out = append(out, item)
		}
	}
	s.live = out
}

func (s *ProgressSet) eraseLiveLocked() {
	if !s.tty || s.drawn == 0 {
		s.drawn = 0
		return
	}
	fmt.Fprintf(os.Stdout, cursorUp, s.drawn)
	for i := 0; i < s.drawn; i++ {
		fmt.Fprint(os.Stdout, clearLine+"\n")
	}
	fmt.Fprintf(os.Stdout, cursorUp, s.drawn)
	s.drawn = 0
}

// commitLineLocked ends the current status row. A single-stage spinner has
// been redrawn with carriage returns, so the final text replaces that row.
func (s *ProgressSet) commitLineLocked(line string) {
	if s.inline {
		fmt.Fprintf(os.Stdout, "\r%s%s\n", line, clearToEnd)
		s.inline = false
		return
	}
	s.eraseLiveLocked()
	fmt.Fprintln(os.Stdout, line)
}

func (s *ProgressSet) drawLiveLocked() {
	if !s.tty || len(s.live) == 0 {
		return
	}
	frame := spinnerFrames[s.frame%len(spinnerFrames)]
	// One stage stays on the line it started on. Printing a newline and
	// moving the cursor back up leaves the earlier frames on screen in
	// terminals that do not honor that cursor move.
	if len(s.live) == 1 {
		if s.drawn > 0 {
			s.eraseLiveLocked()
		}
		fmt.Fprintf(os.Stdout, "\r%s%s", s.liveLine(s.live[0], frame), clearToEnd)
		s.inline = true
		return
	}
	if s.inline {
		fmt.Fprintln(os.Stdout)
		s.inline = false
		s.drawn = 1
	}
	if s.drawn > 0 {
		fmt.Fprintf(os.Stdout, cursorUp, s.drawn)
	}
	for _, p := range s.live {
		fmt.Fprintf(os.Stdout, "\r%s%s\n", s.liveLine(p, frame), clearToEnd)
	}
	s.drawn = len(s.live)
}

func (s *ProgressSet) liveLine(p *Progress, frame rune) string {
	if s.prefix {
		return formatPrefixLine(p.name, string(frame), "", s.nameWidth)
	}
	return fmt.Sprintf("%s  %c", p.name, frame)
}

func (s *ProgressSet) statusLine(name, status string, d time.Duration) string {
	if !s.prefix {
		return FormatStatusLine(name, status, d, false)
	}
	duration := ""
	if status != "skipped" {
		duration = FormatDuration(d)
	}
	return formatPrefixLine(name, status, duration, s.nameWidth)
}

// StartProgress begins a spinner for a single stage. Tests and sequential
// callers that do not share a set use this. Parallel runs should share a set.
func StartProgress(name string, verbose, prefix bool, _ *sync.Mutex) *Progress {
	s := NewProgressSet(verbose, prefix)
	p := s.Start(name)
	p.owned = true
	return p
}

// Finish writes the final status line. Sequential TTY overwrites the spinner.
func (p *Progress) Finish(status string) {
	if p == nil {
		return
	}
	if p.set != nil {
		p.set.Finish(p, status, nil)
		if p.owned {
			p.set.Close()
		}
		return
	}
}

// PrintSkipped writes a single skipped line. No spinner.
func PrintSkipped(name string, verbose, prefix bool, mu *sync.Mutex) {
	if verbose {
		return
	}
	line := FormatStatusLine(name, "skipped", 0, prefix)
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	fmt.Fprintln(os.Stdout, line)
}

// WriteCapturedOutput prints container logs after a quiet failure.
func WriteCapturedOutput(name string, err error, prefix bool, mu *sync.Mutex) {
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	writeCapturedOutputLocked(name, err, prefix)
}

func writeCapturedOutputLocked(name string, err error, prefix bool) {
	var captured *StageOutputError
	if !errors.As(err, &captured) {
		return
	}
	if captured.Stdout == "" && captured.Stderr == "" {
		return
	}
	fmt.Fprintln(os.Stdout)
	if prefix {
		writePrefixed(os.Stdout, name, captured.Stdout)
		writePrefixed(os.Stderr, name, captured.Stderr)
		return
	}
	writePlain(os.Stdout, captured.Stdout)
	writePlain(os.Stderr, captured.Stderr)
}

func writePlain(w *os.File, text string) {
	if text == "" {
		return
	}
	fmt.Fprint(w, text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(w)
	}
}

// nameColumnWidth is the rune width of the longest stage label.
// Zero means the caller did not supply names, so prefix lines stay unpadded.
func nameColumnWidth(names []string) int {
	width := 0
	for _, name := range names {
		if n := utf8.RuneCountInString(name); n > width {
			width = n
		}
	}
	return width
}

// formatPrefixLine is the parallel status row: name, separator, status, duration.
// nameWidth 0 keeps the compact form. A positive width pads every row so the
// separator, status, and duration line up. An empty duration (a spinner frame,
// or skipped) leaves the status column open.
func formatPrefixLine(name, status, duration string, nameWidth int) string {
	if nameWidth == 0 {
		if duration == "" {
			return name + " | " + status
		}
		return name + " | " + status + " " + duration
	}
	if gap := nameWidth - utf8.RuneCountInString(name); gap > 0 {
		name += strings.Repeat(" ", gap)
	}
	if duration == "" {
		return name + " | " + status
	}
	if gap := statusColumnWidth - utf8.RuneCountInString(status); gap > 0 {
		status += strings.Repeat(" ", gap)
	}
	if gap := durationColumnWidth - utf8.RuneCountInString(duration); gap > 0 {
		duration = strings.Repeat(" ", gap) + duration
	}
	return name + " | " + status + " " + duration
}

// FormatStatusLine is the quiet progress line.
func FormatStatusLine(name, status string, d time.Duration, prefix bool) string {
	if prefix {
		duration := ""
		if status != "skipped" {
			duration = FormatDuration(d)
		}
		return formatPrefixLine(name, status, duration, 0)
	}
	if status == "skipped" {
		return fmt.Sprintf("%-16s %s", name, status)
	}
	return fmt.Sprintf("%-16s %-7s %s", name, status, FormatDuration(d))
}

// FormatDuration renders seconds with one decimal, matching the quiet contract.
func FormatDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}
