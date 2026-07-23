package tui

// Package tui renders a zero-dependency, in-process ANSI dashboard for
// observing the live HDM runtime: the active cells, their descriptors and tape
// counts, user targets, co-mutation tracking, and a scrolling event log. It uses
// only the standard library (raw ANSI escapes) per the zero-dependency mandate.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mdcfrancis/flow/axiom"
	"github.com/mdcfrancis/flow/codependency"
	"github.com/mdcfrancis/flow/engine"
	"github.com/mdcfrancis/flow/evolution"
	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// StateSource provides read access to the live system for rendering.
type StateSource struct {
	Ledger  *storage.LedgerEngine
	Repo    *manifest.Repository
	Tapes   *evolution.TapeStore
	Gravity *codependency.Tracker
	Axiom   *axiom.Compiler
	Cells   func() []string
}

// Dashboard renders the system state. When disabled it degrades to plain
// line-oriented output (tick lines to stdout, events via the standard logger).
type Dashboard struct {
	enabled bool
	src     StateSource
	out     io.Writer
	start   time.Time

	mu        sync.Mutex
	tick      int
	phenotype string
	result    uint32
	fuel      uint64
	tokens    uint64
	latMS     float64
	h         float64
	events    []string
}

const maxEvents = 12

// New builds a dashboard. enabled selects full-screen ANSI mode.
func New(enabled bool, src StateSource) *Dashboard {
	return &Dashboard{enabled: enabled, src: src, out: os.Stdout, start: time.Now()}
}

// Tick records the latest production heartbeat.
func (d *Dashboard) Tick(tick int, phenotype string, result uint32, fuel, tokens uint64, latMS, h float64) {
	d.mu.Lock()
	d.tick, d.phenotype, d.result = tick, phenotype, result
	d.fuel, d.tokens, d.latMS, d.h = fuel, tokens, latMS, h
	d.mu.Unlock()
	if !d.enabled {
		fmt.Fprintf(d.out, "[%s] Tick %d (%s…) result=%d fuel=%d tokens=%d lat=%.1fms H=%.4f\n",
			time.Now().Format("15:04:05"), tick, phenotype, result, fuel, tokens, latMS, h)
	}
}

// Event records a log line (evolution, janitor, axiom, warnings).
func (d *Dashboard) Event(msg string) {
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return
	}
	d.mu.Lock()
	d.events = append(d.events, time.Now().Format("15:04:05")+" "+msg)
	if len(d.events) > maxEvents {
		d.events = d.events[len(d.events)-maxEvents:]
	}
	d.mu.Unlock()
}

// Write lets the dashboard serve as the log destination; each line becomes an
// event (TUI mode) or passes through to stderr (plain mode).
func (d *Dashboard) Write(p []byte) (int, error) {
	if d.enabled {
		d.Event(string(p))
		return len(p), nil
	}
	return os.Stderr.Write(p)
}

// Start launches the refresh loop (no-op when disabled). It restores the
// terminal when ctx is cancelled.
func (d *Dashboard) Start(ctx context.Context) {
	if !d.enabled {
		return
	}
	fmt.Fprint(d.out, "\033[2J\033[?25l") // clear + hide cursor
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				fmt.Fprint(d.out, "\033[?25h\033[2J\033[H") // show cursor + clear
				return
			case <-t.C:
				d.render()
			}
		}
	}()
}

func (d *Dashboard) render() {
	d.mu.Lock()
	tick, pheno, result := d.tick, d.phenotype, d.result
	fuel, tokens, latMS, h := d.fuel, d.tokens, d.latMS, d.h
	events := append([]string(nil), d.events...)
	d.mu.Unlock()

	var b strings.Builder
	b.WriteString("\033[H") // cursor home (overwrite, avoids flicker)
	line := func(s string) { b.WriteString(s + "\033[K\n") }

	line("\033[1;36m╔══ HOMEOSTATIC DATAFLOW MACHINE ─ live observer ══════════════════════════╗\033[0m")
	root := d.ref(engine.ManifestRootURN)
	line(fmt.Sprintf(" uptime %-10s  tick %-6d  manifest %s",
		d.start.Format("15:04:05")+"+"+dur(time.Since(d.start)), tick, short(root)))
	line(fmt.Sprintf(" live: phenotype %s…  result=%d  fuel=%d  tokens=%d  lat=%.1fms  \033[1mH=%.4f\033[0m",
		pheno, result, fuel, tokens, latMS, h))
	line("")

	line("\033[1m CELLS\033[0m")
	line(fmt.Sprintf("  %-26s %-14s %-8s %-8s %s", "urn", "phenotype", "saliency", "tapes", "domains"))
	var cells []string
	if d.src.Cells != nil {
		cells = d.src.Cells()
	}
	for _, urn := range cells {
		desc, err := d.src.Repo.Load(urn)
		if err != nil {
			line(fmt.Sprintf("  %-26s \033[2m(unresolved)\033[0m", urn))
			continue
		}
		n := 0
		if d.src.Tapes != nil {
			n, _ = d.src.Tapes.Count(urn)
		}
		line(fmt.Sprintf("  %-26s %-14s %-8.2f %-8d %s",
			trunc(urn, 26), short(desc.PhenotypeHash), desc.Saliency, n,
			strings.Join(desc.Semantics.DomainTags, ",")))
	}
	line("")

	d.renderTargets(line)
	d.renderGravity(line)

	line("\033[1m EVENTS\033[0m")
	start := 0
	if len(events) > maxEvents {
		start = len(events) - maxEvents
	}
	for _, e := range events[start:] {
		line("  " + trunc(e, 90))
	}
	// Pad remaining event rows so stale lines are cleared.
	for i := len(events); i < maxEvents; i++ {
		line("")
	}
	b.WriteString("\033[J") // clear below
	fmt.Fprint(d.out, b.String())
}

func (d *Dashboard) renderTargets(line func(string)) {
	if d.src.Axiom == nil {
		return
	}
	set, err := d.src.Axiom.Load()
	if err != nil || len(set.Targets) == 0 {
		return
	}
	line(fmt.Sprintf("\033[1m TARGETS\033[0m (%d)", len(set.Targets)))
	for _, t := range set.Targets {
		line(fmt.Sprintf("  %-6.2f %s %s", t.Saliency, trunc(t.Intent, 48),
			"\033[2m"+strings.Join(t.DomainTags, ",")+"\033[0m"))
	}
	line("")
}

func (d *Dashboard) renderGravity(line func(string)) {
	if d.src.Gravity == nil {
		return
	}
	m, err := d.src.Gravity.Load()
	if err != nil || len(m.Joint) == 0 {
		return
	}
	line("\033[1m GRAVITY\033[0m")
	for x, ys := range m.Joint {
		for y := range ys {
			line(fmt.Sprintf("  %.2f  %s → %s", m.Gravity(x, y), trunc(x, 30), trunc(y, 30)))
		}
	}
	line("")
}

func (d *Dashboard) ref(urn string) string {
	if d.src.Ledger == nil {
		return ""
	}
	h, err := d.src.Ledger.GetRef(urn)
	if err != nil {
		return ""
	}
	return h
}

func short(h string) string {
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "-"
	}
	return h
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func dur(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
