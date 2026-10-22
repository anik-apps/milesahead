package health

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []mail.ModelAlert
	fail error
	gate chan struct{} // when set, SendModelAlert waits for it
}

func (f *fakeSender) SendModelAlert(_ string, a mail.ModelAlert) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, a)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func openDB(t *testing.T) *db.Database {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

var (
	genStep = Step{Name: StepGenerator, Label: "Generator", Models: []string{"gen"},
		Roles: []string{"generator"}, StopsPublishing: true}
	criticStep = Step{Name: StepCritic, Label: "Critic", Models: []string{"super", "kimi"},
		Roles: []string{"critic"}, StopsPublishing: true}
	gone = &ai.APIError{StatusCode: 410, Model: "x", Body: "end of life"}
)

func ok(role, model string) ai.PreflightResult {
	return ai.PreflightResult{Model: model, Role: role, OK: true}
}

func bad(role, model string) ai.PreflightResult {
	return ai.PreflightResult{Model: model, Role: role, Err: gone}
}

// results is a check that always answers rs.
func results(rs ...ai.PreflightResult) func() []ai.PreflightResult {
	return func() []ai.PreflightResult { return rs }
}

// criticGone: the generator answers, both critics are retired (2026-10-20).
var criticGone = results(ok("generator", "gen"), bad("critic", "super"), bad("critic", "kimi"))

func newWorker(t *testing.T, d *db.Database, steps []Step, check func() []ai.PreflightResult, s AlertSender) *Worker {
	w := New(Options{DB: d, Steps: steps, Check: check, AdminURL: "https://x/admin/models", Interval: time.Hour,
		Now: func() time.Time { return t0 }})
	w.SetAlerting(s, "ops@example.com", "")
	return w
}

func TestWorker_CheckEmailsOnceWhenTheCriticDies(t *testing.T) {
	d, s := openDB(t), &fakeSender{}
	w := newWorker(t, d, []Step{genStep, criticStep}, criticGone, s)
	w.RunCheck()
	w.RunCheck()
	if s.count() != 1 || !strings.Contains(s.sent[0].Subject, "Critic is down") || !s.sent[0].Urgent {
		t.Fatalf("sent %+v", s.sent)
	}
	if snap := w.Snapshot(); !snap.Stopped || snap.Publishing.Reason == "" {
		t.Errorf("snapshot publishing %+v", snap.Publishing)
	}
}

func TestWorker_DeadBackupWarns(t *testing.T) {
	d, s := openDB(t), &fakeSender{}
	w := newWorker(t, d, []Step{criticStep}, results(ok("critic", "super"), bad("critic", "kimi")), s)
	w.RunCheck()
	if s.count() != 1 || s.sent[0].Urgent || !strings.Contains(s.sent[0].Subject, "backup kimi") {
		t.Fatalf("sent %+v", s.sent)
	}
	if snap := w.Snapshot(); snap.NotOK != 1 || snap.Stopped {
		t.Errorf("badge %d stopped %v", snap.NotOK, snap.Stopped)
	}
}

func TestWorker_FailedSendIsRetried(t *testing.T) {
	d, s := openDB(t), &fakeSender{fail: errors.New("550 mailbox unavailable")}
	w := newWorker(t, d, []Step{genStep, criticStep}, criticGone, s)
	w.RunCheck()
	if snap := w.Snapshot(); snap.LastSendError == "" {
		t.Error("the failed send is not shown")
	}
	s.fail = nil
	w.RunCheck()
	if s.count() != 1 {
		t.Errorf("sent %d after the retry, want 1", s.count())
	}
}

func TestWorker_RestartDoesNotResend(t *testing.T) {
	d, s := openDB(t), &fakeSender{}
	newWorker(t, d, []Step{genStep, criticStep}, criticGone, s).RunCheck()
	newWorker(t, d, []Step{genStep, criticStep}, criticGone, s).RunCheck()
	if s.count() != 1 {
		t.Errorf("sent %d across a restart, want 1", s.count())
	}
}

func TestWorker_CallsAreDebounced(t *testing.T) {
	d, s := openDB(t), &fakeSender{}
	w := newWorker(t, d, []Step{criticStep}, nil, s)
	fail := ai.ModelCall{Step: StepCritic, Model: "super", Source: ai.SourceCall, Outcome: ai.OutcomeTimeout, At: t0}
	w.RecordModelCall(fail)
	w.RecordModelCall(fail)
	w.Drain()
	if s.count() != 0 {
		t.Fatalf("emailed after 2 failed calls: %+v", s.sent)
	}
	w.RecordModelCall(fail)
	w.Drain()
	if s.count() != 1 || !strings.Contains(s.sent[0].Subject, "running on a backup") {
		t.Errorf("sent %+v", s.sent)
	}
}

// The critic chain fails model after model within a second. One evaluation
// covers both, so the admin gets "down", not "running on a backup" then "down".
func TestWorker_FailuresArrivingTogetherSendOneEmail(t *testing.T) {
	d, s := openDB(t), &fakeSender{}
	w := newWorker(t, d, []Step{genStep, criticStep}, nil, s)
	for _, c := range []ai.ModelCall{
		{Step: StepGenerator, Model: "gen", Source: ai.SourceCall, Outcome: ai.OutcomeOK, At: t0},
		{Step: StepCritic, Model: "super", Source: ai.SourceCall, Outcome: ai.OutcomeRetired, At: t0},
		{Step: StepCritic, Model: "kimi", Source: ai.SourceCall, Outcome: ai.OutcomeRetired, At: t0},
	} {
		w.RecordModelCall(c)
	}
	w.Drain()
	if s.count() != 1 || !s.sent[0].Urgent || !strings.Contains(s.sent[0].Subject, "Critic is down") {
		t.Errorf("sent %+v, want one urgent 'Critic is down'", s.sent)
	}
}

func TestWorker_RecordNeverBlocks(t *testing.T) {
	w := New(Options{DB: openDB(t), Steps: []Step{criticStep}, Buffer: 1})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			w.RecordModelCall(ai.ModelCall{Step: StepCritic, Model: "super", Outcome: ai.OutcomeOK, At: t0})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RecordModelCall blocked with a full buffer")
	}
}

func TestWorker_SnapshotDoesNotWaitForASend(t *testing.T) {
	d, s := openDB(t), &fakeSender{gate: make(chan struct{})}
	defer close(s.gate)
	w := newWorker(t, d, []Step{genStep, criticStep}, criticGone, s)
	go w.RunCheck()
	time.Sleep(50 * time.Millisecond) // RunCheck is now waiting on the send
	got := make(chan Snapshot, 1)
	go func() { got <- w.Snapshot() }()
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("Snapshot waited for a hung email send")
	}
}

func TestWorker_SharedModelIsJudgedPerStep(t *testing.T) {
	d := openDB(t)
	steps := []Step{
		{Name: StepCritic, Label: "Critic", Models: []string{"vl"}, Roles: []string{"critic"}, StopsPublishing: true},
		{Name: StepEnrichment, Label: "Enrichment", Models: []string{"vl"}, Roles: []string{"enrichment"}},
	}
	check := func() []ai.PreflightResult {
		return []ai.PreflightResult{
			{Model: "vl", Role: "critic", OK: true},
			{Model: "vl", Role: "enrichment", Err: gone},
		}
	}
	w := newWorker(t, d, steps, check, &fakeSender{})
	w.RunCheck()
	snap := w.Snapshot()
	states := map[string]StepState{}
	for _, v := range snap.Steps {
		states[v.Name] = v.State
	}
	if states[StepCritic] != StepOK || states[StepEnrichment] != StepDown {
		t.Errorf("states %v, want critic ok and enrichment down", states)
	}
}

func TestWorker_DroughtAlarm(t *testing.T) {
	d, s := openDB(t), &fakeSender{}
	for _, name := range []string{"A", "B"} {
		id, _ := d.CreateSource(&db.Source{Name: name, URL: "https://" + name + ".example", Active: true})
		d.CreateRawArticle(&db.RawArticle{SourceID: id, Title: "T", URL: "https://" + name + ".example/1",
			Content: "c", PublishedAt: time.Now(), Hash: "h" + name})
	}
	w := New(Options{DB: d, Steps: []Step{criticStep}, Interval: time.Hour,
		Check: results(ok("critic", "super"), ok("critic", "kimi"))})
	w.SetAlerting(s, "ops@example.com", "")
	w.RunCheck()
	if s.count() != 1 || !strings.Contains(s.sent[0].Subject, "No article in 24 hours") {
		t.Errorf("sent %+v", s.sent)
	}
}

func TestWorker_NotEmailed(t *testing.T) {
	w := New(Options{DB: openDB(t), Steps: []Step{genStep, criticStep}, Check: criticGone})
	w.SetAlerting(nil, "", "no address: set the ALERT_EMAIL secret")
	w.RunCheck() // must not panic
	if snap := w.Snapshot(); snap.NotEmailed == "" {
		t.Error("the page is not told alerts are not emailed")
	}
	if err := w.SendTestAlert(); !errors.Is(err, ErrNotEmailed) {
		t.Errorf("SendTestAlert = %v, want ErrNotEmailed", err)
	}
}

func TestWorker_SendTestAlert(t *testing.T) {
	s := &fakeSender{}
	w := newWorker(t, openDB(t), []Step{criticStep}, nil, s)
	if err := w.SendTestAlert(); err != nil || s.count() != 1 {
		t.Errorf("err %v sent %d", err, s.count())
	}
}
