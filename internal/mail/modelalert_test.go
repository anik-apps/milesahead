package mail

import (
	"strings"
	"testing"
)

func TestSendModelAlert_SubjectAndItems(t *testing.T) {
	s, mock := newTestSender()
	err := s.SendModelAlert("ops@example.com", ModelAlert{
		Subject: "[Miles Ahead] URGENT: Critic is down: publishing has stopped",
		Urgent:  true,
		Items: []ModelAlertItem{{Severity: "URGENT", Title: "Critic is down: publishing has stopped",
			Detail: "nvidia/nemotron-3-super-120b-a12b: retired since Oct 20 01:08 UTC"}},
		AdminURL: "https://milesahead.example/admin/models",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.sent) != 1 || mock.sent[0].to[0] != "ops@example.com" {
		t.Fatalf("sent %+v", mock.sent)
	}
	text := mock.sent[0].part(t, "text/plain")
	for _, want := range []string{"Critic is down", "nemotron-3-super", "https://milesahead.example/admin/models"} {
		if !strings.Contains(text, want) {
			t.Errorf("text part lacks %q:\n%s", want, text)
		}
	}
}

func TestSendModelAlert_EscapesErrorText(t *testing.T) {
	s, mock := newTestSender()
	s.SendModelAlert("ops@example.com", ModelAlert{Subject: "s",
		Items: []ModelAlertItem{{Severity: "Warning", Title: "t", Detail: `<html><script>alert(1)</script>`}}})
	html := mock.sent[0].part(t, "text/html")
	if strings.Contains(html, "<script>") || !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("error text not escaped:\n%s", html)
	}
}
