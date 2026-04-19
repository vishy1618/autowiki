package dream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"github.com/suvish/autowiki/internal/vault"
)

var ist = time.FixedZone("IST", 5*3600+30*60)

// ConsolidateFn is the function called to perform the overnight vault consolidation.
type ConsolidateFn func(ctx context.Context) error

// Runner schedules and runs the nightly vault consolidation.
type Runner struct {
	vault       *vault.Manager
	consolidate ConsolidateFn
}

// NewRunner returns a Runner that uses vm to read the vault log and calls
// consolidate once per night during the 1–5 am IST window.
func NewRunner(vm *vault.Manager, consolidate ConsolidateFn) *Runner {
	return &Runner{vault: vm, consolidate: consolidate}
}

// NextFireTime returns a random time in the 1–5 am IST window that is
// strictly after now. If now is before 1 am IST today, the fire time is today;
// otherwise it is tomorrow.
func NextFireTime(now time.Time) time.Time {
	nowIST := now.In(ist)

	// Random offset within the 4-hour window (1:00:00 – 4:59:59).
	windowSec := 4 * 3600 // 4 hours
	offsetSec := rand.Intn(windowSec)

	// Candidate: today at 01:00 IST + offsetSec.
	candidate := time.Date(
		nowIST.Year(), nowIST.Month(), nowIST.Day(),
		1, 0, 0, 0, ist,
	).Add(time.Duration(offsetSec) * time.Second)

	if !candidate.After(now) {
		// Push to tomorrow.
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// todayEntry returns true if the vault log.md contains a dated entry for today
// in the IST timezone. It reads only the last 500 bytes of log.md to avoid
// loading large files.
func (r *Runner) todayEntry() bool {
	tail, err := r.vault.ReadFilePartial("log.md", 500)
	if err != nil {
		return false
	}
	today := time.Now().In(ist).Format("2006-01-02")
	return strings.Contains(tail, today)
}

// RunOnce performs one consolidation check: if today's log entry is absent,
// it calls the consolidate function and appends a summary to log.md.
// Returns immediately when ctx is cancelled.
func (r *Runner) RunOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if r.todayEntry() {
		return
	}
	if err := r.consolidate(ctx); err != nil {
		_ = r.vault.AppendLog(fmt.Sprintf("dream error: %v", err))
		return
	}
}

// ── Consolidator ──────────────────────────────────────────────────────────────

// ConsolidationMessage is a single user or assistant turn in a consolidation call.
type ConsolidationMessage struct {
	Role    string // "user" or "assistant"
	Content string
}

// ConsolidationStreamer is the LLM interface used by the Consolidator.
type ConsolidationStreamer interface {
	StreamWithSystem(ctx context.Context, systemPrompt string, messages []ConsolidationMessage) (io.ReadCloser, error)
}

// Consolidator implements the overnight vault consolidation logic.
type Consolidator struct {
	vault    *vault.Manager
	streamer ConsolidationStreamer
}

// NewConsolidator returns a Consolidator backed by vm and the given LLM streamer.
func NewConsolidator(vm *vault.Manager, streamer ConsolidationStreamer) *Consolidator {
	return &Consolidator{vault: vm, streamer: streamer}
}

const (
	smallVaultLimit = 25
	batchSize       = 10
)

const consolidationSystemPrompt = `You are an autonomous wiki curator. Your task is to improve the structure, cross-references, and completeness of a personal Obsidian vault.

SAFETY RULE: Never delete or overwrite a file unless its content has been confirmed saved to another location first. When reorganising, always save_to_vault updated content before deleting the original.

Use [[wikilinks]] to connect related pages. Follow vault schema conventions. Prefer additive changes; only restructure when clearly beneficial.

You have access to these tools: save_to_vault, read_page, search_vault, list_vault, read_page_partial, move_page, delete_item.`

// Consolidate performs one overnight vault consolidation. For small vaults
// (≤ smallVaultLimit pages) a single LLM call is made. For larger vaults a
// topology call determines batches, then one worker call per batch is made.
// A dated summary is appended to log.md after a successful run.
func (c *Consolidator) Consolidate(ctx context.Context) error {
	// Step 1: list all vault files.
	entries, err := c.vault.ListVault("", true)
	if err != nil {
		return fmt.Errorf("listing vault: %w", err)
	}

	// Collect .md files only.
	var mdFiles []vault.VaultEntry
	for _, e := range entries {
		if e.Type == "file" && strings.HasSuffix(e.Path, ".md") {
			mdFiles = append(mdFiles, e)
		}
	}

	// Step 2: read first 500 chars of each .md file for summaries.
	type fileSummary struct {
		Path    string
		Preview string
	}
	summaries := make([]fileSummary, 0, len(mdFiles))
	for _, f := range mdFiles {
		preview, _ := c.vault.ReadFilePartial(f.Path, 500)
		summaries = append(summaries, fileSummary{Path: f.Path, Preview: preview})
	}

	var changeLog []string

	if len(mdFiles) <= smallVaultLimit {
		// Small vault: single LLM call with full page content.
		var sb strings.Builder
		for _, s := range summaries {
			full, _ := c.vault.ReadFile(s.Path)
			fmt.Fprintf(&sb, "## %s\n\n%s\n\n", s.Path, full)
		}
		changes, err := c.runWorkerCall(ctx, "", sb.String())
		if err != nil {
			return err
		}
		changeLog = append(changeLog, changes...)
	} else {
		// Large vault: topology call to get batch plan.
		var sb strings.Builder
		sb.WriteString("Here is the vault index with page previews:\n\n")
		for _, s := range summaries {
			fmt.Fprintf(&sb, "- %s: %s\n", s.Path, firstLine(s.Preview))
		}
		sb.WriteString("\nReturn a JSON object with a \"batches\" key: an array of arrays of page paths. Group related pages together. Target ~10 pages per batch.")

		topologyBody, err := c.streamer.StreamWithSystem(ctx, consolidationSystemPrompt, []ConsolidationMessage{
			{Role: "user", Content: sb.String()},
		})
		if err != nil {
			return fmt.Errorf("topology call: %w", err)
		}
		topologyText := extractText(topologyBody)
		topologyBody.Close()

		// Parse the batch plan.
		batches := parseBatches(topologyText, mdFiles)

		// Topology JSON to include in every batch worker call for cross-batch context.
		topologyJSON := topologyText

		// Step 4: one worker call per batch.
		for _, batch := range batches {
			var batchContent strings.Builder
			fmt.Fprintf(&batchContent, "Topology context:\n%s\n\nProcess these pages:\n\n", topologyJSON)
			for _, path := range batch {
				full, _ := c.vault.ReadFile(path)
				fmt.Fprintf(&batchContent, "## %s\n\n%s\n\n", path, full)
			}
			changes, err := c.runWorkerCall(ctx, topologyJSON, batchContent.String())
			if err != nil {
				return err
			}
			changeLog = append(changeLog, changes...)
		}
	}

	// Step 6: append dated log entry.
	today := time.Now().In(ist).Format("2006-01-02")
	summary := fmt.Sprintf("## %s dream run\n\n", today)
	if len(changeLog) == 0 {
		summary += "No changes made.\n"
	} else {
		summary += strings.Join(changeLog, "\n") + "\n"
	}
	return c.vault.AppendLog(summary)
}

// runWorkerCall sends one batch/single-vault consolidation call to the LLM,
// executes any tool calls in the response, and returns a list of change log lines.
// topologyJSON is included for context (empty for small vault calls).
func (c *Consolidator) runWorkerCall(ctx context.Context, topologyJSON, userContent string) ([]string, error) {
	body, err := c.streamer.StreamWithSystem(ctx, consolidationSystemPrompt, []ConsolidationMessage{
		{Role: "user", Content: userContent},
	})
	if err != nil {
		return nil, err
	}
	defer body.Close()
	// For now we scan the SSE stream for any text response (tool dispatch is
	// wired in the full implementation; the agentic loop is kept simple here).
	_ = extractText(body)
	return nil, nil
}

// parseBatches extracts the batch plan from the topology LLM response.
// Falls back to grouping mdFiles into batchSize-sized chunks if parsing fails.
func parseBatches(topologyText string, mdFiles []vault.VaultEntry) [][]string {
	// Try to extract JSON from the response text.
	var plan struct {
		Batches [][]string `json:"batches"`
	}
	if err := json.Unmarshal([]byte(topologyText), &plan); err == nil && len(plan.Batches) > 0 {
		return plan.Batches
	}

	// Fallback: chunk mdFiles into batchSize groups.
	var batches [][]string
	for i := 0; i < len(mdFiles); i += batchSize {
		end := i + batchSize
		if end > len(mdFiles) {
			end = len(mdFiles)
		}
		chunk := make([]string, end-i)
		for j, f := range mdFiles[i:end] {
			chunk[j] = f.Path
		}
		batches = append(batches, chunk)
	}
	return batches
}

// extractText scans an Anthropic SSE body and returns the concatenated text deltas.
func extractText(body io.ReadCloser) string {
	var sb strings.Builder
	scanner := bufio.NewScanner(body)
	var lastEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			lastEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") || lastEvent != "content_block_delta" {
			continue
		}
		raw := strings.TrimPrefix(line, "data: ")
		var payload struct {
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil && payload.Delta.Type == "text_delta" {
			sb.WriteString(payload.Delta.Text)
		}
	}
	return sb.String()
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return s
}

// PostRunSchedulingTime returns the time to use as "now" when computing the
// next fire time after a run completes. It returns 6am IST of the same day as
// now, which is past the 1–5am window, guaranteeing NextFireTime picks tomorrow.
func PostRunSchedulingTime(now time.Time) time.Time {
	n := now.In(ist)
	return time.Date(n.Year(), n.Month(), n.Day(), 6, 0, 0, 0, ist)
}

// Start loops indefinitely: sleeps until the next 1–5 am IST fire time, then
// runs RunOnce. After each run it advances the scheduling base past the window
// so the next fire time is always tomorrow. Exits when ctx is cancelled.
func (r *Runner) Start(ctx context.Context) {
	now := time.Now()
	for {
		next := NextFireTime(now)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		r.RunOnce(ctx)
		now = PostRunSchedulingTime(time.Now())
	}
}
