package dream_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/dream"
	"github.com/suvish/autowiki/internal/vault"
)

var ist = time.FixedZone("IST", 5*3600+30*60)

// ── consolidation tests ───────────────────────────────────────────────────────

// stubLLM records calls and returns preset SSE bodies in sequence.
type stubLLM struct {
	callCount int
	bodies    []string // return bodies[callCount-1]; repeats last when exhausted
}

func (s *stubLLM) StreamWithSystem(_ context.Context, _ string, _ []dream.ConsolidationMessage) (io.ReadCloser, error) {
	s.callCount++
	body := s.bodies[len(s.bodies)-1]
	if s.callCount-1 < len(s.bodies) {
		body = s.bodies[s.callCount-1]
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// minimalSSE is an Anthropic SSE response with one text delta and a stop (no tool call).
const minimalSSE = `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}

event: message_stop
data: {"type":"message_stop"}

`

func writePages(t *testing.T, vm *vault.Manager, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("page%02d.md", i)
		_ = vm.WriteFile(path, fmt.Sprintf("# Page %d\n\nContent for page %d.", i, i))
	}
}

func TestPostRunSchedulingTime_IsAfterThe1To5amWindow(t *testing.T) {
	// PostRunSchedulingTime must return a time past the 1–5am IST window so that
	// NextFireTime(PostRunSchedulingTime(now)) always picks tomorrow.
	now := time.Date(2026, 1, 15, 1, 30, 0, 0, ist) // 1:30am IST — inside window
	postRun := dream.PostRunSchedulingTime(now)
	postRunIST := postRun.In(ist)

	if postRunIST.Hour() < 5 {
		t.Errorf("expected PostRunSchedulingTime to be at or after 5am IST, got %02d:%02d IST", postRunIST.Hour(), postRunIST.Minute())
	}
}

func TestNextFireTime_AfterPostRunSchedulingTime_AlwaysReturnsTomorrow(t *testing.T) {
	// The combination of PostRunSchedulingTime + NextFireTime must always
	// produce a fire time in tomorrow's window — never the same calendar day.
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
	// Run many samples to ensure randomness always lands in [1:00, 5:00) IST.
	for i := 0; i < 200; i++ {
		fire := dream.NextFireTime(time.Now())

		fireIST := fire.In(ist)
		h := fireIST.Hour()
		m := fireIST.Minute()
		s := fireIST.Second()
		totalSec := h*3600 + m*60 + s

		// Window: 01:00:00–04:59:59 IST (inclusive).
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
	// Arrange — vault log already has today's dated entry.
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

	// Fire immediately (past time) so the loop runs without sleeping.
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

func TestConsolidator_SmallVault_MakesSingleLLMCall(t *testing.T) {
	// Arrange — 5 pages (≤ 25).
	dir := t.TempDir()
	vm := vault.NewManager(dir)
	writePages(t, vm, 5)

	llm := &stubLLM{bodies: []string{minimalSSE}}
	c := dream.NewConsolidator(vm, llm)

	// Act
	err := c.Consolidate(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llm.callCount != 1 {
		t.Errorf("expected 1 LLM call for small vault, got %d", llm.callCount)
	}
}

func TestConsolidator_LargeVault_MakesTopologyThenBatchCalls(t *testing.T) {
	// Arrange — 30 pages (> 25), expect topology call + ceil(30/10) = 3 batch calls.
	dir := t.TempDir()
	vm := vault.NewManager(dir)
	writePages(t, vm, 30)

	// topology returns a JSON plan; batch calls return minimal SSE.
	topologySSE := `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"batches\":[[\"page00.md\",\"page01.md\",\"page02.md\",\"page03.md\",\"page04.md\",\"page05.md\",\"page06.md\",\"page07.md\",\"page08.md\",\"page09.md\"],[\"page10.md\",\"page11.md\",\"page12.md\",\"page13.md\",\"page14.md\",\"page15.md\",\"page16.md\",\"page17.md\",\"page18.md\",\"page19.md\"],[\"page20.md\",\"page21.md\",\"page22.md\",\"page23.md\",\"page24.md\",\"page25.md\",\"page26.md\",\"page27.md\",\"page28.md\",\"page29.md\"]]}"}}

event: message_stop
data: {"type":"message_stop"}

`
	// First call is topology, remaining calls are batch workers.
	llm := &stubLLM{bodies: []string{topologySSE, minimalSSE, minimalSSE, minimalSSE}}
	c := dream.NewConsolidator(vm, llm)

	// Act
	err := c.Consolidate(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 topology + 3 batch = 4 total
	if llm.callCount != 4 {
		t.Errorf("expected 4 LLM calls (1 topology + 3 batch) for 30 pages, got %d", llm.callCount)
	}
}

func TestConsolidator_AppendsDatedLogEntry(t *testing.T) {
	// Arrange — small vault so we get one call.
	dir := t.TempDir()
	vm := vault.NewManager(dir)
	writePages(t, vm, 3)

	llm := &stubLLM{bodies: []string{minimalSSE}}
	c := dream.NewConsolidator(vm, llm)

	// Act
	err := c.Consolidate(context.Background())

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
