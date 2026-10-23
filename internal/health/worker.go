package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// AlertSender is the part of mail.Sender model alerts use.
type AlertSender interface {
	SendModelAlert(to string, a mail.ModelAlert) error
}

// ErrNotEmailed: alerts have no address, or SMTP is not configured.
var ErrNotEmailed = errors.New("model alerts are not emailed")

// Options configures a Worker.
type Options struct {
	DB    *db.Database
	Steps []Step
	// Check runs one preflight round; nil turns checks off.
	Check    func() []ai.PreflightResult
	AdminURL string        // the Models tab, linked from emails
	Interval time.Duration // between checks; 0 when the hourly check is off
	Buffer   int           // queued call outcomes; 0 means 1024
	Now      func() time.Time
}

// Worker records model calls and checks, judges each list, and emails the
// admin when that changes. One Worker serves the whole process.
type Worker struct {
	o      Options
	events chan ai.ModelCall

	// mu serialises row updates, evaluation and sends, so two evaluations
	// never race to send the same alert.
	mu sync.Mutex

	// view guards what Snapshot reads without waiting on mu (a slow SMTP
	// send holds mu).
	view       sync.Mutex
	sender     AlertSender
	to         string
	notEmailed string
	lastCheck  time.Time
	// drought is the last measured drought. Until a check has measured it
	// (droughtKnown), the stored alert state stands in: an unmeasured
	// drought must not read as "no drought" and email a false recovery.
	drought      bool
	droughtKnown bool
}

// New returns a Worker. Call SetAlerting, then Run in a goroutine.
func New(o Options) *Worker {
	if o.Buffer <= 0 {
		o.Buffer = 1024
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Worker{o: o, events: make(chan ai.ModelCall, o.Buffer)}
}

// SetAlerting sets where alerts go. s nil or to "" means they are shown on
// the Models tab and not emailed; notEmailed says why.
func (w *Worker) SetAlerting(s AlertSender, to, notEmailed string) {
	w.view.Lock()
	defer w.view.Unlock()
	w.sender, w.to, w.notEmailed = s, to, notEmailed
}

func (w *Worker) alerting() (AlertSender, string, string) {
	w.view.Lock()
	defer w.view.Unlock()
	return w.sender, w.to, w.notEmailed
}

// measuredDrought is the drought as the last check measured it; known is
// false until a check has.
func (w *Worker) measuredDrought() (drought, known bool) {
	w.view.Lock()
	defer w.view.Unlock()
	return w.drought, w.droughtKnown
}

// RecordModelCall queues a call's outcome. It never blocks: when the buffer
// is full the outcome is dropped and counted.
func (w *Worker) RecordModelCall(c ai.ModelCall) {
	select {
	case w.events <- c:
	default:
		metrics.ModelEventsDroppedTotal.Inc()
	}
}

// evaluateDelay batches failures: a critic chain fails model after model
// within a second, and judging after the first would email "running on a
// backup" moments before "down". One evaluation covers them all.
const evaluateDelay = 5 * time.Second

// Run applies queued outcomes until ctx is done, evaluating evaluateDelay
// after the first failure of a batch.
func (w *Worker) Run(ctx context.Context) {
	var due <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-w.events:
			if w.apply(c) && due == nil {
				due = time.After(evaluateDelay)
			}
		case <-due:
			due = nil
			w.evaluate()
		}
	}
}

// Drain applies every queued outcome now, then evaluates once if any
// failed. Tests use it instead of Run.
func (w *Worker) Drain() {
	failed := false
	for {
		select {
		case c := <-w.events:
			if w.apply(c) {
				failed = true
			}
		default:
			if failed {
				w.evaluate()
			}
			return
		}
	}
}

// apply records one outcome and reports whether it was a failure.
func (w *Worker) apply(c ai.ModelCall) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.applyLocked(c); err != nil {
		slog.Warn("model health: recording a call", "step", c.Step, "model", c.Model, "error", err)
		return false
	}
	return c.Outcome != ai.OutcomeOK
}

func (w *Worker) evaluate() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.evaluateLocked()
}

func (w *Worker) applyLocked(c ai.ModelCall) error {
	row, _, err := w.o.DB.GetModelHealth(c.Step, c.Model)
	if err != nil {
		return err
	}
	return w.o.DB.UpsertModelHealth(Apply(row, c))
}

// RunCheck probes every model in every list that is on, records each result
// against the steps that use it, checks for a publishing drought, and evaluates.
func (w *Worker) RunCheck() {
	if w.o.Check == nil {
		return
	}
	results := w.o.Check()
	at := w.o.Now().UTC()
	drought, droughtErr := w.droughtAt(at)
	if droughtErr != nil {
		slog.Warn("model health: drought check", "error", droughtErr)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.o.Steps {
		if s.Off {
			continue
		}
		for _, m := range s.Models {
			r, ok := findResult(results, m, s.Roles)
			if !ok {
				continue
			}
			c := ai.ModelCall{Step: s.Name, Model: m, Source: ai.SourceCheck, Outcome: ai.OutcomeOf(r.Err), At: at}
			if r.Err != nil {
				c.Detail = cut(r.Err.Error(), 300)
			}
			if err := w.applyLocked(c); err != nil {
				slog.Warn("model health: recording a check", "step", s.Name, "model", m, "error", err)
			}
		}
	}
	w.view.Lock()
	w.lastCheck = at
	if droughtErr == nil { // a failed measurement keeps the last known answer
		w.drought, w.droughtKnown = drought, true
	}
	w.view.Unlock()
	metrics.ModelCheckLastSuccess.Set(float64(at.Unix()))
	w.evaluateLocked()
}

func findResult(results []ai.PreflightResult, model string, roles []string) (ai.PreflightResult, bool) {
	for _, r := range results {
		if r.Model != model {
			continue
		}
		for _, role := range roles {
			if r.Role == role {
				return r, true
			}
		}
	}
	return ai.PreflightResult{}, false
}

// droughtAt: no article of any status in 24 hours while 2 or more sources
// added stories.
func (w *Worker) droughtAt(now time.Time) (bool, error) {
	since := now.Add(-24 * time.Hour)
	n, err := w.o.DB.CountArticlesCreatedSince(since)
	if err != nil || n > 0 {
		return false, err
	}
	sources, err := w.o.DB.CountSourcesWithRawSince(since)
	return sources >= 2, err
}

// evaluateLocked judges every list, plans alerts, and sends one email. An
// alert's state is saved only after its email went out; a failed send is
// retried at the next evaluation. With no address or no SMTP nothing can go
// out, so nothing is saved either: the first evaluation after alerting is set
// up emails everything that is still wrong.
func (w *Worker) evaluateLocked() {
	now := w.o.Now().UTC()
	rows, err := w.o.DB.ListModelHealth()
	if err != nil {
		slog.Warn("model health: reading rows", "error", err)
		return
	}
	views := Evaluate(w.o.Steps, rows, now)
	setStepGauges(views)
	stored, err := w.o.DB.ListModelAlerts()
	if err != nil {
		slog.Warn("model health: reading alert state", "error", err)
		return
	}
	drought, known := w.measuredDrought()
	if !known {
		drought = stored[droughtKey].State == droughtStopped
	}
	events, next := Plan(views, drought, stored, now)

	sender, to, why := w.alerting()
	switch {
	case len(events) == 0:
	case sender == nil || to == "":
		slog.Warn("model health alert not emailed", "reason", why, "events", titles(events))
		return
	default:
		if err := sender.SendModelAlert(to, Compose(events, w.o.AdminURL)); err != nil {
			metrics.EmailsSentTotal.WithLabelValues(metrics.EmailModelAlert, metrics.EmailFailed).Inc()
			_ = w.o.DB.RecordModelAlertSend(now, err.Error())
			slog.Error("model health alert email failed; it is sent again at the next evaluation", "error", err)
			return
		}
		metrics.EmailsSentTotal.WithLabelValues(metrics.EmailModelAlert, metrics.EmailSent).Inc()
		_ = w.o.DB.RecordModelAlertSend(now, "")
		slog.Info("model health alert emailed", "events", titles(events))
	}
	if err := w.o.DB.SaveModelAlerts(next); err != nil {
		slog.Warn("model health: saving alert state", "error", err)
	}
}

func setStepGauges(views []StepView) {
	for _, v := range views {
		switch v.State {
		case StepOK:
			metrics.ModelStepState.WithLabelValues(v.Name).Set(0)
		case StepDegraded:
			metrics.ModelStepState.WithLabelValues(v.Name).Set(1)
		case StepDown:
			metrics.ModelStepState.WithLabelValues(v.Name).Set(2)
		}
	}
}

func titles(events []Event) string {
	var t []string
	for _, e := range events {
		t = append(t, e.Title)
	}
	return strings.Join(t, "; ")
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Publishing says whether articles are being published.
type Publishing struct {
	Stopped     bool
	Reason      string
	Since       time.Time
	LastArticle time.Time
}

// Snapshot is what the admin Models tab shows.
type Snapshot struct {
	Steps         []StepView // down first, then degraded, ok with no backup, ok, off
	Publishing    Publishing
	LastCheck     time.Time
	CheckEvery    time.Duration // 0 when the hourly check is off
	CheckLate     bool          // no check for two intervals
	Recipient     string
	NotEmailed    string // why alerts are not emailed; "" when they are
	LastSendAt    time.Time
	LastSendError string
	NotOK         int  // for the tab badge: lists not ok, plus ok lists whose backups are all dead
	Stopped       bool // publishing has stopped: the badge turns red
}

// Snapshot reads the current state. It never waits on an email send.
func (w *Worker) Snapshot() Snapshot {
	now := w.o.Now().UTC()
	_, to, why := w.alerting()
	w.view.Lock()
	s := Snapshot{CheckEvery: w.o.Interval, Recipient: to, NotEmailed: why, LastCheck: w.lastCheck}
	drought, droughtKnown := w.drought, w.droughtKnown
	w.view.Unlock()

	if !droughtKnown { // after a restart, before a check has measured it
		stored, err := w.o.DB.ListModelAlerts()
		if err != nil {
			slog.Warn("model health: snapshot alert state", "error", err)
		}
		drought = stored[droughtKey].State == droughtStopped
	}

	rows, err := w.o.DB.ListModelHealth()
	if err != nil {
		slog.Warn("model health: snapshot rows", "error", err)
	}
	for _, r := range rows { // after a restart, before the first check
		if r.LastCheck.After(s.LastCheck) {
			s.LastCheck = r.LastCheck
		}
	}
	s.CheckLate = s.CheckEvery > 0 && (s.LastCheck.IsZero() || now.Sub(s.LastCheck) > 2*s.CheckEvery)
	s.LastSendAt, s.LastSendError, _ = w.o.DB.LastModelAlertSend()
	s.Publishing.LastArticle, _ = w.o.DB.LatestArticleCreatedAt()

	s.Steps = Evaluate(w.o.Steps, rows, now)
	sort.SliceStable(s.Steps, func(i, j int) bool { return pageRank(s.Steps[i]) < pageRank(s.Steps[j]) })
	for _, v := range s.Steps {
		if v.State == StepDegraded || v.State == StepDown || (v.State == StepOK && v.NoBackup) {
			s.NotOK++
		}
		if v.State == StepDown && v.StopsPublishing && !s.Publishing.Stopped {
			s.Publishing.Stopped = true
			s.Publishing.Reason = fmt.Sprintf("no %s model answers", strings.ToLower(v.Label))
			s.Publishing.Since = v.Since
		}
	}
	if !s.Publishing.Stopped && drought {
		s.Publishing.Stopped = true
		s.Publishing.Reason = "no article in 24 hours while sources kept adding stories"
		s.Publishing.Since = s.Publishing.LastArticle
	}
	s.Stopped = s.Publishing.Stopped
	return s
}

func pageRank(v StepView) int {
	switch {
	case v.State == StepDown:
		return 0
	case v.State == StepDegraded:
		return 1
	case v.State == StepOK && v.NoBackup:
		return 2
	case v.State == StepOK:
		return 3
	}
	return 4
}

// SendTestAlert emails a test message so the admin can see alerts arrive.
func (w *Worker) SendTestAlert() error {
	sender, to, why := w.alerting()
	if sender == nil || to == "" {
		return fmt.Errorf("%w: %s", ErrNotEmailed, why)
	}
	err := sender.SendModelAlert(to, mail.ModelAlert{
		Subject: "[Miles Ahead] Test alert",
		Items: []mail.ModelAlertItem{{Severity: "Test", Title: "Test alert from the Models tab",
			Detail: "If you can read this, model health alerts reach " + to + "."}},
		AdminURL: w.o.AdminURL,
	})
	msg, outcome := "", metrics.EmailSent
	if err != nil {
		msg, outcome = err.Error(), metrics.EmailFailed
	}
	metrics.EmailsSentTotal.WithLabelValues(metrics.EmailModelAlert, outcome).Inc()
	_ = w.o.DB.RecordModelAlertSend(w.o.Now().UTC(), msg)
	return err
}
