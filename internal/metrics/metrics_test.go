package metrics

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// Every kind and outcome exists from startup, at zero.
func TestEmailSeriesExistAtZero(t *testing.T) {
	initEmailSeries()
	reg := prometheus.NewRegistry()
	reg.MustRegister(EmailsSentTotal)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(mfs) != 1 || mfs[0].GetName() != "milesahead_emails_sent_total" {
		t.Fatalf("gathered %v, want milesahead_emails_sent_total", mfs)
	}
	seen := map[string]bool{}
	for _, m := range mfs[0].GetMetric() {
		labels := map[string]string{}
		for _, l := range m.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		seen[labels["kind"]+"/"+labels["outcome"]] = true
	}
	for _, want := range []string{"confirmation/sent", "confirmation/failed", "newsletter/sent", "newsletter/failed"} {
		if !seen[want] {
			t.Errorf("series %s missing; have %v", want, seen)
		}
	}
}

// alertRule is the part of a Grafana alert rule these checks read.
type alertRule struct {
	UID       string `json:"uid"`
	Title     string `json:"title"`
	Condition string `json:"condition"`
	For       string `json:"for"`
	Data      []struct {
		RefID string `json:"refId"`
		Model struct {
			Expr       string `json:"expr"`
			Expression string `json:"expression"`
			Conditions []struct {
				Evaluator struct {
					Type   string    `json:"type"`
					Params []float64 `json:"params"`
				} `json:"evaluator"`
			} `json:"conditions"`
		} `json:"model"`
	} `json:"data"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

func loadAlertRules(t *testing.T) []alertRule {
	t.Helper()
	b, err := os.ReadFile("../../deploy/grafana-alerts.json")
	if err != nil {
		t.Fatal(err)
	}
	var rules []alertRule
	if err := json.Unmarshal(b, &rules); err != nil {
		t.Fatalf("deploy/grafana-alerts.json is not valid: %v", err)
	}
	return rules
}

// Every rule is complete: a unique uid, a title and summary, and a
// condition that names one of its own queries.
func TestAlertRulesAreWellFormed(t *testing.T) {
	uids := map[string]bool{}
	for _, r := range loadAlertRules(t) {
		if r.UID == "" || uids[r.UID] {
			t.Errorf("rule %q: uid missing or repeated", r.Title)
		}
		uids[r.UID] = true
		if r.Title == "" || r.Annotations["summary"] == "" || r.Labels["service"] != "milesahead" {
			t.Errorf("rule %s: needs a title, a summary and service=milesahead", r.UID)
		}
		refs := map[string]bool{}
		for _, d := range r.Data {
			refs[d.RefID] = true
		}
		if !refs[r.Condition] {
			t.Errorf("rule %s: condition %q is not one of its queries", r.UID, r.Condition)
		}
	}
}

// The email rules watch the failures the app counts, by the labels it uses.
func TestAlertRulesWatchEmailFailures(t *testing.T) {
	want := map[string]string{
		"milesahead-confirmation-email-failures": `kind="` + EmailConfirmation + `",outcome="` + EmailFailed + `"`,
		"milesahead-newsletter-send-failed":      `kind="` + EmailNewsletter + `",outcome="` + EmailFailed + `"`,
	}
	for _, r := range loadAlertRules(t) {
		sel, ok := want[r.UID]
		if !ok {
			continue
		}
		delete(want, r.UID)
		expr := r.Data[0].Model.Expr
		if !strings.Contains(expr, "milesahead_emails_sent_total{"+sel+"}") {
			t.Errorf("rule %s: expr %q should count milesahead_emails_sent_total{%s}", r.UID, expr, sel)
		}
	}
	for uid := range want {
		t.Errorf("rule %s is missing from deploy/grafana-alerts.json", uid)
	}
}

// "Two or more" failures: increase() extrapolates, so one failure can read
// as a little over 1, and a threshold of gt 1 would fire on it.
func TestConfirmationAlertNeedsTwoFailures(t *testing.T) {
	for _, r := range loadAlertRules(t) {
		if r.UID != "milesahead-confirmation-email-failures" {
			continue
		}
		ev := r.Data[2].Model.Conditions[0].Evaluator
		if ev.Type != "gt" || len(ev.Params) != 1 || ev.Params[0] < 1.5 || ev.Params[0] >= 2 {
			t.Errorf("threshold = %s %v, want gt 1.5", ev.Type, ev.Params)
		}
		return
	}
	t.Error("the confirmation failures rule is missing")
}
