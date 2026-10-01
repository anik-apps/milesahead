package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// ConfirmationMailer sends the email that confirms a new subscription.
// mail.Sender implements it.
type ConfirmationMailer interface {
	SendConfirmation(toEmail, token, domain string) error
}

// SetConfirmationMailer sets what sends confirmation emails, and so opens
// newsletter sign-up: the header, footer, home and article placements and
// the /subscribe form. Without one (newsletter.signup_enabled off, or SMTP
// unset; see cmd/milesahead) sign-up is closed: there are no placements,
// /subscribe says it opens soon, and no address is stored. Confirming and
// unsubscribing work either way. Call it before serving.
func (app *App) SetConfirmationMailer(m ConfirmationMailer) {
	app.mailer = m
}

// signupOpen reports whether readers can sign up for the newsletter.
func (app *App) signupOpen() bool {
	return app.mailer != nil
}

const (
	// confirmationCooldown is how long a pending address waits before the
	// form will send it another confirmation email.
	confirmationCooldown = 10 * time.Minute
	// Sign-up attempts allowed per IP address per subscribeWindow.
	subscribeMaxAttempts = 10
	subscribeWindow      = time.Hour
	// subscribeHoneypot is a form field people never see or fill in.
	subscribeHoneypot = "website"
)

// Page states rendered by subscribe.html.
const (
	subStateForm         = "form"         // GET /subscribe, or a rejected address
	subStateCheckEmail   = "check-email"  // after a sign-up
	subStateConfirmed    = "confirmed"    // first click on the confirmation link
	subStateAlready      = "already"      // a later click on it
	subStateWelcomeBack  = "welcome-back" // after Resubscribe
	subStateUnsubConfirm = "unsub-confirm"
	subStateUnsubscribed = "unsubscribed"
	subStateBadLink      = "bad-link" // unknown or missing token
	subStateNoToken      = "no-token" // /unsubscribe with no token at all
	subStateTooMany      = "too-many"
	subStateClosed       = "closed" // sign-up is not open yet
)

// renderSubscribePage renders subscribe.html in one of the states above.
// None of these pages belongs in a search index.
func (app *App) renderSubscribePage(w http.ResponseWriter, status int, state, title string, extra map[string]interface{}) {
	data := map[string]interface{}{
		"Title":      title,
		"State":      state,
		"Noindex":    true,
		"NavSection": "newsletter",
	}
	for k, v := range extra {
		data[k] = v
	}
	app.renderStatus(w, status, "subscribe.html", data)
}

// renderSignupClosed is every sign-up step while sign-up is closed: it
// opens soon, and nothing was stored.
func (app *App) renderSignupClosed(w http.ResponseWriter) {
	app.renderSubscribePage(w, http.StatusOK, subStateClosed, "Newsletter sign-up opens soon", nil)
}

// handleSubscribePage is the sign-up page.
func (app *App) handleSubscribePage(w http.ResponseWriter, r *http.Request) {
	if !app.signupOpen() {
		app.renderSignupClosed(w)
		return
	}
	app.renderSubscribePage(w, http.StatusOK, subStateForm, "Subscribe", nil)
}

// handleCheckEmail is where a sign-up lands after the redirect.
func (app *App) handleCheckEmail(w http.ResponseWriter, r *http.Request) {
	if !app.signupOpen() {
		app.renderSignupClosed(w)
		return
	}
	app.renderSubscribePage(w, http.StatusOK, subStateCheckEmail, "Check your inbox", nil)
}

// handleSubscribe takes a sign-up. Every accepted address, new or already on
// the list, gets the same redirect to the same page, so the form cannot be
// used to find out who subscribes. A new or pending address is sent a
// confirmation email (at most one per confirmationCooldown); an address that
// had unsubscribed becomes pending again and must confirm.
func (app *App) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	// A form from a page cached before sign-up closed stores nothing.
	if !app.signupOpen() {
		app.renderSignupClosed(w)
		return
	}
	if !app.subscribeLimiter.allow(extractIP(r)) {
		app.renderSubscribePage(w, http.StatusTooManyRequests, subStateTooMany, "Try again later", nil)
		return
	}

	// Bots fill in every field. Answer them as if it worked.
	if r.FormValue(subscribeHoneypot) != "" {
		http.Redirect(w, r, "/subscribe/check-email", http.StatusSeeOther)
		return
	}

	email, ok := normalizeEmail(r.FormValue("email"))
	if !ok {
		app.renderSubscribePage(w, http.StatusBadRequest, subStateForm, "Subscribe", map[string]interface{}{
			"Error": "Please enter a valid email address.",
			"Email": strings.TrimSpace(r.FormValue("email")),
		})
		return
	}

	sub, err := app.db.GetSubscriberByEmail(email)
	if errors.Is(err, db.ErrSubscriberNotFound) {
		token := generateToken()
		id, cerr := app.db.CreateSubscriber(&db.Subscriber{Email: email, Token: token})
		if cerr == nil {
			sub = &db.Subscriber{ID: id, Email: email, Token: token}
			err = nil
		} else {
			// Most likely a second request for the same address got there
			// first; use that row.
			sub, err = app.db.GetSubscriberByEmail(email)
		}
	}
	if err != nil {
		app.serverError(w, r, "subscribe: looking up the address", err)
		return
	}

	switch sub.State() {
	case db.SubscriberUnsubscribed:
		if err := app.db.ReopenSubscriber(sub.ID); err != nil {
			app.serverError(w, r, "subscribe: reopening an unsubscribed address", err)
			return
		}
		app.sendConfirmation(sub)
	case db.SubscriberPending:
		app.sendConfirmation(sub)
	}

	http.Redirect(w, r, "/subscribe/check-email", http.StatusSeeOther)
}

// sendConfirmation emails sub its confirmation link in the background,
// unless one went out within confirmationCooldown.
func (app *App) sendConfirmation(sub *db.Subscriber) {
	if app.mailer == nil {
		slog.Warn("subscribe: no mail sender configured; confirmation email not sent", "subscriber_id", sub.ID)
		return
	}
	claimed, err := app.db.ClaimConfirmationSend(sub.ID, app.clock(), confirmationCooldown)
	if err != nil {
		slog.Error("subscribe: recording the confirmation send", "subscriber_id", sub.ID, "error", err)
		return
	}
	if !claimed {
		return
	}
	domain := app.siteDomain()
	app.bg.Add(1)
	go func() {
		defer app.bg.Done()
		if err := app.mailer.SendConfirmation(sub.Email, sub.Token, domain); err != nil {
			metrics.EmailsSentTotal.WithLabelValues(metrics.EmailConfirmation, metrics.EmailFailed).Inc()
			slog.Error("subscribe: sending the confirmation email failed", "subscriber_id", sub.ID, "error", err)
			if rerr := app.db.ReleaseConfirmationSend(sub.ID); rerr != nil {
				slog.Error("subscribe: releasing the confirmation claim", "subscriber_id", sub.ID, "error", rerr)
			}
			return
		}
		metrics.EmailsSentTotal.WithLabelValues(metrics.EmailConfirmation, metrics.EmailSent).Inc()
	}()
}

// handleSubscribeConfirm is the link in the confirmation email. Clicking it
// again (or a mail scanner opening it first) is not an error.
func (app *App) handleSubscribeConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		app.renderSubscribePage(w, http.StatusBadRequest, subStateBadLink, "Link not recognised", nil)
		return
	}
	err := app.db.ConfirmSubscriber(token)
	switch {
	case err == nil:
		app.renderSubscribePage(w, http.StatusOK, subStateConfirmed, "Subscription confirmed", nil)
	case errors.Is(err, db.ErrAlreadyConfirmed):
		if r.URL.Query().Get("welcome") == "back" {
			app.renderSubscribePage(w, http.StatusOK, subStateWelcomeBack, "You're subscribed again", nil)
			return
		}
		app.renderSubscribePage(w, http.StatusOK, subStateAlready, "Already subscribed", nil)
	case errors.Is(err, db.ErrUnsubscribed):
		app.renderSubscribePage(w, http.StatusOK, subStateUnsubscribed, "Unsubscribed", map[string]interface{}{"Token": token})
	case errors.Is(err, db.ErrSubscriberNotFound):
		app.renderSubscribePage(w, http.StatusNotFound, subStateBadLink, "Link not recognised", nil)
	default:
		app.serverError(w, r, "confirm: confirming a subscription", err)
	}
}

// handleUnsubscribePage is the Unsubscribe link in emails. Opening it only
// asks; the button POSTs. Link scanners open every link in a message, and
// used to unsubscribe people this way.
func (app *App) handleUnsubscribePage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		// Newsletters sent before tokens were added link here bare.
		app.renderSubscribePage(w, http.StatusBadRequest, subStateNoToken, "Unsubscribe", map[string]interface{}{
			"ContactEmail": app.contactEmail(),
		})
		return
	}
	sub, err := app.db.GetSubscriberByToken(token)
	if errors.Is(err, db.ErrSubscriberNotFound) {
		app.renderSubscribePage(w, http.StatusNotFound, subStateBadLink, "Link not recognised", nil)
		return
	}
	if err != nil {
		app.serverError(w, r, "unsubscribe: looking up the token", err)
		return
	}
	if sub.State() == db.SubscriberUnsubscribed {
		app.renderSubscribePage(w, http.StatusOK, subStateUnsubscribed, "Unsubscribed", map[string]interface{}{"Token": token})
		return
	}
	app.renderSubscribePage(w, http.StatusOK, subStateUnsubConfirm, "Unsubscribe", map[string]interface{}{
		"Token":  token,
		"Masked": maskEmail(sub.Email),
	})
}

// handleUnsubscribe unsubscribes. The token may be in the query string (as
// in a List-Unsubscribe URL) or the form. A one-click request from a mail
// provider (RFC 8058: a POST with List-Unsubscribe=One-Click) gets a plain
// 200; a reader's button press is redirected to the page that confirms it.
func (app *App) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if token == "" {
		app.renderSubscribePage(w, http.StatusBadRequest, subStateBadLink, "Link not recognised", nil)
		return
	}
	err := app.db.Unsubscribe(token)
	if errors.Is(err, db.ErrSubscriberNotFound) {
		app.renderSubscribePage(w, http.StatusNotFound, subStateBadLink, "Link not recognised", nil)
		return
	}
	if err != nil {
		app.serverError(w, r, "unsubscribe: unsubscribing", err)
		return
	}
	if r.PostFormValue("List-Unsubscribe") == "One-Click" {
		app.renderSubscribePage(w, http.StatusOK, subStateUnsubscribed, "Unsubscribed", map[string]interface{}{"Token": token})
		return
	}
	http.Redirect(w, r, "/unsubscribe?token="+url.QueryEscape(token), http.StatusSeeOther)
}

// handleResubscribe undoes an unsubscribe from the page that confirmed it.
func (app *App) handleResubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if token == "" {
		app.renderSubscribePage(w, http.StatusBadRequest, subStateBadLink, "Link not recognised", nil)
		return
	}
	err := app.db.Resubscribe(token)
	if errors.Is(err, db.ErrSubscriberNotFound) {
		app.renderSubscribePage(w, http.StatusNotFound, subStateBadLink, "Link not recognised", nil)
		return
	}
	if err != nil {
		app.serverError(w, r, "resubscribe", err)
		return
	}
	http.Redirect(w, r, "/subscribe/confirm?token="+url.QueryEscape(token)+"&welcome=back", http.StatusSeeOther)
}

// normalizeEmail trims and lower-cases an address and reports whether it is
// a plain, valid one ("Name <a@b>" forms are rejected).
func normalizeEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", false
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || !strings.Contains(email[at+1:], ".") {
		return "", false
	}
	return email, true
}

// maskEmail shows enough of an address to recognise it: "a•••@gmail.com".
func maskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 1 {
		return "your address"
	}
	return email[:1] + "•••" + email[at:]
}

// contactEmail is the bare address newsletters come from, for readers who
// cannot use a link; "" if none is configured.
func (app *App) contactEmail() string {
	from := strings.TrimSpace(app.cfg.Mail.SMTP.From)
	if from == "" {
		return ""
	}
	if addr, err := mail.ParseAddress(from); err == nil {
		return addr.Address
	}
	return ""
}

// clock is the App's clock, falling back to time.Now for fixtures built
// without a constructor.
func (app *App) clock() time.Time {
	if app.now != nil {
		return app.now()
	}
	return time.Now()
}
