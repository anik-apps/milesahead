---
title: Newsletter
weight: 4
---

A weekly, newspaper-style email: Scout's Pick, Card of the Week and the week's articles grouped by tag. The service is `internal/newsletter`; sending is `internal/mail`; the sign-up pages are `internal/web/subscribe.go` with one template (`subscribe.html`) per step. How mail leaves the server (OCI Email Delivery, DNS records, secrets) is on the [Email](../../deployment/email/) page.

## Schedule

By default (and in production) the issue is built Friday at 20:00 and sent Saturday at 08:00, New York time:

| When | Job | What happens |
|---|---|---|
| The day before the send, 20:00 (Friday) | `newsletter-build` (`BuildWeekly`) | Builds the issue for the next send day from the week's live articles. Ended offers are dropped first. The short-call AI chain ranks the articles by reader value (original order if it fails); the top 3 become candidates (`newsletter_candidates`), rank 1 is Scout's Pick, and Card of the Week is the published card this week's articles mention most. The issue is stored as `auto_approved`. Idempotent: a second run returns the existing issue. |
| `send_day` at `send_time` (Saturday 08:00) | `newsletter-send` (`SendWeekly`) | Sends that day's issue to every confirmed subscriber if its status is `approved` or `auto_approved`. The issue is rendered again at send time, from live articles, so an offer that ended overnight or an article unpublished since is left out, and a Scout's Pick that ended hands its slot to the top article still being sent. |

The schedule comes from `newsletter.send_day`, `send_time` and `timezone` (defaults `saturday`, `08:00`, `America/New_York`). `newsletter.ScheduleFor` turns them into both cron lines and into words, "every Saturday at 8:00 AM ET", for the sign-up page, the confirmation email and the admin Newsletter and Settings pages; tests hold the words to when the cron lines fire. US zones get their short name (ET, CT, MT, PT); any other zone is named after its city ("7:05 AM London time"). `config.Load` rejects a day, time or zone it cannot read, so `-check-config` fails the deploy rather than the send. `timezone` also dates the issues and decides which day counts as "today" on the admin page. The startup log line `weekly newsletter scheduled` shows the cron lines in use.

Between the build and the send, the admin's Newsletter page shows the issue and says "Goes out Sat Oct 3, 8:00 AM ET, to N confirmed subscribers", or why it will not. The admin can feature a different candidate, override the Card of the Week, or hold or skip the issue (`POST /admin/newsletter/feature`, `/card-override`, `/status`).

The page's manual **Send** is a separate daily digest (`SendDaily`): the live articles of the 24 hours before the issue was generated, re-rendered as it is sent. It sends nothing on the day of the weekly issue or for an issue the admin held or skipped, and always says why when nothing went out. Known issue: the digest's excerpts are cut from the article HTML, so they can show raw tags; the weekly uses the TL;DR and is not affected.

## Sign-up and Unsubscribe

| Step | Route | Behaviour |
|---|---|---|
| Form | `GET /subscribe` | A labelled email field, a honeypot field (`website`) people never see, and what you get and how often. |
| Submit | `POST /subscribe` → 303 to `/subscribe/check-email` | The same "check your inbox" page for new, pending and already-subscribed addresses, so the form does not reveal who subscribes. At most 10 attempts per IP per hour (429 after). |
| Confirmation email | - | Plain text and HTML, absolute links to `https://DOMAIN`, says what the newsletter is and that it comes "every Saturday at 8:00 AM ET". A pending address gets at most one per 10 minutes (`confirmation_sent_unix`, claimed atomically). |
| Confirm | `GET /subscribe/confirm?token=` | Idempotent: a second click says already subscribed, and an old confirm link cannot undo an unsubscribe. |
| Unsubscribe page | `GET /unsubscribe?token=` | Only asks, so a mail scanner following the link changes nothing. A bare `/unsubscribe` (from issues sent before per-reader links) explains the link is incomplete and offers a mailto. |
| Unsubscribe | `POST /unsubscribe` | Unsubscribes. Also accepts a mail provider's RFC 8058 one-click body (`List-Unsubscribe=One-Click`). The row is kept with `unsubscribed_at`, so the page can offer **Resubscribe** (`POST /resubscribe`) and the token in old emails keeps resolving. |

All these pages are `noindex` and answer 200/303/400/404/429 as appropriate. The admin's Subscribers page lists Active, Pending and Unsubscribed, with a CSV export.

## Per-Reader Unsubscribe Links

The issue is rendered once with a placeholder where the Unsubscribe link goes. `mail.Sender.SendNewsletter` then gives each reader their own copy with:

- their `https://DOMAIN/unsubscribe?token=...` link in the body;
- `List-Unsubscribe: <https://DOMAIN/unsubscribe?token=...>`, plus a `mailto:` only when `newsletter.unsubscribe_mailbox` names a mailbox someone reads and acts on (empty by default: a mailto to an unread address would leave readers thinking they had unsubscribed);
- `List-Unsubscribe-Post: List-Unsubscribe=One-Click` (RFC 8058), which Gmail and Yahoo require of bulk senders.

It refuses to send if the issue has no placeholder or a recipient has no https link, and an empty recipient list returns `ErrNoRecipients` without marking the issue sent. Admin previews and stored copies never contain a token. A send stops at the first failure.

Confirmation emails carry the same `List-Unsubscribe` and one-click headers (added when sign-up was switched on). This matters with OCI Email Delivery, which adds its own `List-Unsubscribe` to any message without an https one; see [Email](../../deployment/email/).

## The Sign-up Switch

Everything above is behind `newsletter.signup_enabled` (default `false`; on in production).

- **Off:** no sign-up placements anywhere, `/subscribe` says "Newsletter sign-up opens soon" with no form, and a POST stores nothing and sends nothing. Confirm and unsubscribe links keep working.
- **On:** the header **Newsletter** pill (an envelope on phones), the footer link, "Get it weekly by email →" in the home hero, and a sign-up box at the end of every article (`article-signup`, posting to `/subscribe` with the same honeypot). The box and the `/subscribe` page carry the line "One email a week. Unsubscribe anytime. We only use your address to send the newsletter." Startup logs `newsletter sign-up is open`.

**Startup safety check.** Once at startup the app decides whether SMTP looks unset (`smtpLooksUnset` in `cmd/milesahead/main.go`): no host or From, a user or password still `placeholder` or an unexpanded `${...}`, or a `localhost`/`127.0.0.1`/`::1` relay that does not accept a connection. Then:

- with the switch on, it logs an ERROR (`newsletter sign-up stays closed: newsletter.signup_enabled is on but SMTP is not configured`) and keeps sign-up closed, since readers would sign up and never get the email;
- the Saturday send is skipped with a WARN (not a failure, so no alert fires every week) and the issue stays unsent;
- the admin's Send says outgoing mail (SMTP) is not configured.

## Metrics and Alerts

- `milesahead_emails_sent_total{kind,outcome}`: `kind` is `confirmation` or `newsletter`, `outcome` is `sent` or `failed` (one `sent` per reader reached, one `failed` per send that stops).
- `milesahead_subscribers_total`: confirmed subscribers.

`deploy/grafana-alerts.json` has two rules for mail, provisioned to Grafana Cloud by the deploy's `grafana` job with the others:

| Alert | Fires when |
|---|---|
| Confirmation emails failing | Two or more failed confirmations in 6 hours (the rule's threshold is 1.5, because Prometheus extrapolates `increase`) |
| Newsletter send failed | Any failed newsletter send in 6 hours |

Each failure is also logged ("subscribe: sending the confirmation email ...", "weekly newsletter send failed").
