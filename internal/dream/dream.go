package dream

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"github.com/suvish/autowiki/internal/chat"
	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

var ist = time.FixedZone("IST", 5*3600+30*60)

// ConsolidateFn is the function called to perform the overnight vault consolidation.
type ConsolidateFn func(ctx context.Context) error

// Streamer is the LLM interface required by Consolidate.
type Streamer interface {
	Stream(ctx context.Context, systemPrompt string, messages []store.Message, attachments []llm.Attachment) (io.ReadCloser, error)
}

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
// strictly after now.
func NextFireTime(now time.Time) time.Time {
	nowIST := now.In(ist)
	offsetSec := rand.Intn(4 * 3600)
	candidate := time.Date(
		nowIST.Year(), nowIST.Month(), nowIST.Day(),
		1, 0, 0, 0, ist,
	).Add(time.Duration(offsetSec) * time.Second)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// todayEntry returns true if log.md contains a dated entry for today (IST).
func (r *Runner) todayEntry() bool {
	tail, err := r.vault.ReadFilePartial("log.md", 500)
	if err != nil {
		return false
	}
	today := time.Now().In(ist).Format("2006-01-02")
	return strings.Contains(tail, today)
}

// RunOnce performs one consolidation check: if today's log entry is absent,
// it calls the consolidate function.
func (r *Runner) RunOnce(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if r.todayEntry() {
		return
	}
	if err := r.consolidate(ctx); err != nil {
		_ = r.vault.AppendLog(fmt.Sprintf("dream error: %v", err))
	}
}

// PostRunSchedulingTime returns 6am IST of the same day as now, which is past
// the 1–5am window, guaranteeing NextFireTime picks tomorrow.
func PostRunSchedulingTime(now time.Time) time.Time {
	n := now.In(ist)
	return time.Date(n.Year(), n.Month(), n.Day(), 6, 0, 0, 0, ist)
}

// Start loops indefinitely: sleeps until the next 1–5 am IST fire time, then
// runs RunOnce. Exits when ctx is cancelled.
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

const dreamSystemPrompt = `You are an autonomous wiki curator performing an overnight consolidation of a personal Obsidian vault.

Your goal is to improve the structure, cross-references, and completeness of the vault. Work autonomously: list the vault, read pages, reorganise, add wikilinks, and save changes.

SAFETY RULE: Never delete or overwrite a file unless its content has been confirmed saved to another location first. When reorganising, always save_to_vault updated content before deleting the original.

Use [[wikilinks]] to connect related pages. Prefer additive changes; only restructure when clearly beneficial.

You have access to these tools: save_to_vault, read_page, search_vault, list_vault, read_page_partial, move_page, delete_item.

When you are done, call save_to_vault with any pages you have changed or created.`

// maxDreamToolCalls is the cap on tool dispatches per consolidation run.
const maxDreamToolCalls = 50

// Consolidate performs one overnight vault consolidation using the agentic
// loop and appends a dated entry to log.md on success.
func Consolidate(ctx context.Context, vm *vault.Manager, streamer Streamer) error {
	cs := store.NewMemChatStore()
	session, err := cs.ResolveSession()
	if err != nil {
		return fmt.Errorf("dream: resolve session: %w", err)
	}

	_ = cs.AppendMessage(store.Message{
		SessionID: session.ID,
		Role:      "user",
		Content:   "Please consolidate the vault: improve structure, add wikilinks, and fix any gaps.",
	})

	history, err := cs.ListMessages(session.ID)
	if err != nil {
		return fmt.Errorf("dream: list messages: %w", err)
	}

	firstBody, err := streamer.Stream(ctx, dreamSystemPrompt, history, nil)
	if err != nil {
		return fmt.Errorf("dream: initial stream: %w", err)
	}

	runner := chat.NewAgenticRunner(streamer, cs, vm)
	if err := runner.Run(ctx, session.ID, dreamSystemPrompt, firstBody, io.Discard, maxDreamToolCalls); err != nil {
		return fmt.Errorf("dream: agentic run: %w", err)
	}

	today := time.Now().In(ist).Format("2006-01-02")
	return vm.AppendLog(fmt.Sprintf("## %s dream run\n\nRun completed.\n", today))
}
