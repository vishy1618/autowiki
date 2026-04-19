package dream_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/dream"
	"github.com/suvish/autowiki/internal/llm"
	"github.com/suvish/autowiki/internal/store"
	"github.com/suvish/autowiki/internal/vault"
)

var ist = time.FixedZone("IST", 5*3600+30*60)

// stubLLM records calls and returns preset SSE bodies in sequence.
type stubLLM struct {
	callCount int
	bodies    []string
}

func (s *stubLLM) Stream(_ context.Context, _ string, _ []store.Message, _ []llm.Attachment) (io.ReadCloser, error) {
	s.callCount++
	idx := s.callCount - 1
	if idx >= len(s.bodies) {
		idx = len(s.bodies) - 1
	}
	return io.NopCloser(strings.NewReader(s.bodies[idx])), nil
}

// minimalSSE is an Anthropic SSE response with one text delta and a stop (no tool call).
const minimalSSE = `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: message_stop
data: {"type":"message_stop"}

`

// ── Runner scheduling tests ───────────────────────────────────────────────────

func TestPostRunSchedulingTime_IsAfterThe1To5amWindow(t *testing.T) {
	now := time.Date(2026, 1, 15, 1, 30, 0, 0, ist)
	postRun := dream.PostRunSchedulingTime(now)
	postRunIST := postRun.In(ist)

	if postRunIST.Hour() < 5 {
		t.Errorf("expected PostRunSchedulingTime to be at or after 5am IST, got %02d:%02d IST", postRunIST.Hour(), postRunIST.Minute())
	}
}

func TestNextFireTime_AfterPostRunSchedulingTime_AlwaysReturnsTomorrow(t *testing.T) {
	now := time.Date(2026, 1, 15, 1, 30, 0, 0, ist)
	postRun := dream.PostRunSchedulingTime(now)

	for i := 0; i < 200; i++ {
		fire := dream.NextFireTime(postRun)
		fireIST := fire.In(ist)
		nowIST := now.In(ist)
		if fireIST.Year() == nowIST.Year() && fireIST.YearDay() == nowIST.YearDay() {
			t.Fatalf("NextFireTime returned same-day fire time after PostRunSchedulingTime: %v", fireIST)
		}
	}
}

func TestNextFireTime_IsInOneTo5amISTWindow(t *testing.T) {
	for i := 0; i < 200; i++ {
		fire := dream.NextFireTime(time.Now())
		fireIST := fire.In(ist)
		h := fireIST.Hour()
		m := fireIST.Minute()
		s := fireIST.Second()
		totalSec := h*3600 + m*60 + s
		if totalSec < 1*3600 || totalSec >= 5*3600 {
			t.Fatalf("fire time %v (IST) is outside 1–5 am window", fireIST)
		}
	}
}

func TestNextFireTime_IsInTheFuture(t *testing.T) {
	now := time.Now()
	fire := dream.NextFireTime(now)
	if !fire.After(now) {
		t.Fatalf("expected fire time in the future, got %v (now=%v)", fire, now)
	}
}

func TestRunner_SkipsConsolidationWhenTodaysEntryExistsInLog(t *testing.T) {
	dir := t.TempDir()
	vm := vault.NewManager(dir)
	today := time.Now().In(ist).Format("2006-01-02")
	_ = vm.WriteFile("log.md", "## "+today+" dream run\n\nNo changes.\n")

	consolidated := false
	consolidateFn := func(ctx context.Context) error {
		consolidated = true
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := dream.NewRunner(vm, consolidateFn)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.RunOnce(ctx)
	}()
	cancel()
	<-done

	if consolidated {
		t.Error("expected consolidation to be skipped when today's log entry exists")
	}
}

// ── Consolidate tests ─────────────────────────────────────────────────────────

func TestConsolidate_AppendsDatedLogEntry(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	vm := vault.NewManager(dir)
	llm := &stubLLM{bodies: []string{minimalSSE}}

	// Act
	err := dream.Consolidate(context.Background(), vm, llm)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	logContent, _ := vm.ReadFile("log.md")
	today := time.Now().In(ist).Format("2006-01-02")
	if !strings.Contains(logContent, today) {
		t.Errorf("expected log.md to contain today's date %q, got: %q", today, logContent)
	}
}
