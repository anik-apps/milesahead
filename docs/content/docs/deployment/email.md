---
title: Email
weight: 1
---

Outgoing mail (sign-up confirmations and the weekly newsletter) goes from the app
over SMTP to **OCI Email Delivery**, which signs it and delivers it. The
authentication records live in Cloudflare DNS. The setup was done on 2026-10-02.
No credential appears on this page; they exist only as GitHub secrets and in the
server's `config.yaml`.

## How Mail Flows

```
  Web server (/subscribe)          Newsletter (Saturdays 08:00 ET)
     confirmation email                weekly issue, one copy per reader
              \                          /
               v                        v
        internal/mail  (gomail, From: noreply@milesahead.kumaranik.com)
                         |
                         |  SMTP, port 587, STARTTLS
                         v
        OCI Email Delivery  (us-sanjose-1; DKIM-signs as milesahead.kumaranik.com)
                         |
                         v
        Readers' inboxes  (check SPF, DKIM and DMARC in Cloudflare DNS)
```

| Email | Sent by | Recipient |
|-------|---------|-----------|
| Sign-up confirmation | `internal/web/subscribe.go` through `mail.Sender.SendConfirmation` | The address that signed up, at most once per 10 minutes |
| Weekly newsletter | `newsletter.SendWeekly` through `mail.Sender.SendNewsletter` | Every confirmed subscriber, one copy each |
| Daily digest | The admin's manual Send on the Newsletter page (`SendDaily`) | Every confirmed subscriber |
| Vote alert | `cmd/milesahead/votealerts.go` through `mail.Sender.SendVoteAlert`, when an article's recent downvotes cross the alert line | `votes.alert_email` (the `ALERT_EMAIL` secret); not sent when it is empty or SMTP is unset. The admin Published page lists every alert either way |

Review emails with a reply address exist in the code (`SendAdminNotification`) but
nothing sends them; see [Inbound mail](#inbound-mail).

Every reader email (confirmation, newsletter, digest) carries the reader's own
`https://milesahead.kumaranik.com/unsubscribe?token=...` link, in the body and in
`List-Unsubscribe`, with `List-Unsubscribe-Post: List-Unsubscribe=One-Click`
(RFC 8058). The confirmation email's headers were added when sign-up was switched
on. See [Newsletter]({{< relref "/docs/features/newsletter" >}}) for the reader
side.

## OCI Email Delivery

| Setting | Value |
|---------|-------|
| Region | `us-sanjose-1` |
| Tier | Always Free: 3,000 emails a month |
| Email domain | `milesahead.kumaranik.com` |
| DKIM selector | `milesahead-202610` (active) |
| Approved sender | `noreply@milesahead.kumaranik.com` |
| SMTP endpoint | `smtp.email.us-sanjose-1.oci.oraclecloud.com`, port 587, STARTTLS |
| Sending identity | IAM user `milesahead-smtp`, member of group `milesahead-smtp-senders` |
| Policy | `milesahead-smtp-send`: `Allow group milesahead-smtp-senders to use email-family in tenancy` |

The IAM user exists only to send: it holds the SMTP credential, and the
group's single policy grants `email-family` and nothing else. If its credentials
leak, the damage is mail sent as the approved sender, and rotating them is a new
SMTP credential on that user plus two secret updates.

The From address must be an approved sender, or OCI refuses the message. To send
from another address, approve it in OCI first, then set the `SMTP_FROM` secret.

## DNS Records

All in Cloudflare (DNS only), on the subdomain the mail is sent as:

- **DKIM**: CNAME `milesahead-202610._domainkey.milesahead.kumaranik.com` →
  `milesahead-202610.milesahead.kumaranik.com.dkim.sjc1.oracleemaildelivery.com`
- **SPF**: TXT on `milesahead.kumaranik.com`:
  `v=spf1 include:rp.oracleemaildelivery.com ~all`
- **DMARC**: TXT on `_dmarc.milesahead.kumaranik.com`: `v=DMARC1; p=none;`

A test message from the server to Gmail passed SPF, DKIM
(`d=milesahead.kumaranik.com`) and DMARC, over TLS 1.3, into the inbox.

## GitHub Secrets

| Secret | Required | Becomes |
|--------|----------|---------|
| `SMTP_HOST` | yes | `mail.smtp.host` (the endpoint above) |
| `SMTP_USER` | yes | `mail.smtp.user`, the SMTP credential's user name |
| `SMTP_PASSWORD` | yes | `mail.smtp.password` |
| `SMTP_PORT` | no | `mail.smtp.port`, default 587; must be numeric |
| `SMTP_FROM` | no | `mail.smtp.from`, default `noreply@milesahead.kumaranik.com` |
| `ALERT_EMAIL` | no | `votes.alert_email`, where vote alerts are emailed; empty by default (alerts are then shown in the admin only) |

The deploy's config job writes them into `config.yaml` on the runner, each
JSON-encoded with `jq` so any character in a password survives, runs
`milesahead -check-config` on the result and only then copies it to the server.
Unset secrets leave the placeholders `localhost` and `placeholder`, which the app
recognises as "SMTP not configured". See
[Generated config]({{< relref "/docs/deployment#generated-config" >}}).

## The Sign-up Switch

`newsletter.signup_enabled` opens sign-up: the header, footer, home and
end-of-article placements, the `/subscribe` form and confirmation emails. It
defaults to `false` and is on in production. Off, `/subscribe` says sign-up opens
soon and stores nothing, while confirm and unsubscribe links keep working.

At startup the app checks whether SMTP looks unset (`smtpLooksUnset` in
`cmd/milesahead/main.go`): no host or From, a user or password that is still
`placeholder` or an unexpanded `${...}`, or a `localhost` relay that does not
answer. If it does:

- with the switch on, it logs an ERROR, `newsletter sign-up stays closed:
  newsletter.signup_enabled is on but SMTP is not configured`, and sign-up stays
  closed, because readers would sign up and never get the email;
- it warns `mail: SMTP is not configured; no newsletter or confirmation email will
  be sent`;
- the Saturday send is skipped with a warning rather than failing (so the
  send-failed alert does not fire every week), and the admin's Send says SMTP is
  not configured.

With SMTP set and the switch on, the journal says `newsletter sign-up is open`
with the SMTP host (never the credentials).

## Alerts

`milesahead_emails_sent_total{kind,outcome}` counts confirmation and newsletter
emails sent and failed. Two Grafana rules in `deploy/grafana-alerts.json` watch it,
pushed by the deploy's grafana job:

| Alert | Fires when |
|-------|-----------|
| Confirmation emails failing | 2 or more failed confirmations in 6 hours (threshold 1.5, since `increase` extrapolates) |
| Newsletter send failed | any failed newsletter send in 6 hours (a send stops at its first failure) |

## OCI's List-Unsubscribe and the Suppression List

OCI Email Delivery adds its own `List-Unsubscribe` header to any message that does
not already have an https one. An unsubscribe through that header puts the address
on OCI's **suppression list**, not in the app: OCI then silently drops mail to it,
while the app still counts the reader as subscribed.

So every reader email carries the app's own per-reader https link and one-click
header, which OCI leaves alone; an unsubscribe then reaches `POST /unsubscribe`
and the app marks the reader unsubscribed.

Admin emails (the vote alert, sent to `votes.alert_email`) have no app link, so
they still get OCI's header, and Gmail may show an Unsubscribe link beside them.
Do not click it: it would put the owner's address on OCI's suppression list, and
no further alert (or any other mail from the app) would reach it. If that happens,
remove the address from the suppression list in the OCI console (Email Delivery,
Suppression List).

OCI also suppresses addresses that hard-bounce or complain. Those are dropped by
OCI and still count as sent in the app's metric.

## Inbound Mail

The code has a review-by-email loop: a review email with a `review+TOKEN@` reply
address (`SendAdminNotification`), and an IMAP poller (`internal/mail/inbound.go`)
that reads replies and treats `APPROVE`, `REJECT`, `UNPUBLISH` or anything else
as rewrite feedback. **It is not running in production:**

- the deploy config sets `mail.imap` to `localhost` with placeholder credentials,
  so startup logs `mail polling disabled`;
- the token resolver in `cmd/milesahead/main.go` is a placeholder that resolves
  nothing;
- nothing calls `SendAdminNotification`, so no review emails go out.

Articles are reviewed in the admin dashboard. Mail sent to
`noreply@milesahead.kumaranik.com` is not read by the app.

## Troubleshooting

**Is mail configured?** After a deploy, look for `newsletter sign-up is open` in the
journal. `newsletter sign-up stays closed` or `mail: SMTP is not configured` means
the SMTP secrets are missing or still placeholders.

```bash
journalctl -u milesahead --since today | grep -E 'sign-up|SMTP|confirmation email|newsletter send'
```

**Did a message leave?** `milesahead_emails_sent_total` in Grafana, split by `kind`
and `outcome`. A failure is logged with the SMTP error. An authentication error
(535) means the SMTP credential is wrong or was rotated: create a new SMTP
credential for `milesahead-smtp`, update `SMTP_USER` and `SMTP_PASSWORD`, and
redeploy. A rejected sender means `SMTP_FROM` is not an approved sender.

**Did it arrive, and was it trusted?** Open the message's original headers in the
receiving mailbox. `Authentication-Results` should show `spf=pass`, `dkim=pass`
with `header.d=milesahead.kumaranik.com`, and `dmarc=pass`. Check the records:

```bash
dig +short TXT milesahead.kumaranik.com
dig +short CNAME milesahead-202610._domainkey.milesahead.kumaranik.com
dig +short TXT _dmarc.milesahead.kumaranik.com
```

**A reader gets nothing, but the app counts them subscribed.** Look the address up
in the OCI Console's Email Delivery suppression list. It may have bounced, been
reported as spam, or used OCI's own unsubscribe header. Remove it only if the
reader asks to receive mail again.

**Tightening DMARC.** `p=none` only monitors, and the record names no report
address, so no reports arrive. Once mail has been passing for a while, add a
`rua=mailto:` address someone reads, watch the reports, then move to
`p=quarantine` (and later `p=reject`). That is an open ROADMAP item.

## Costs

The Always Free tier covers 3,000 emails a month. A weekly issue is one email per
confirmed subscriber per Saturday, about 4.3 a month, plus one confirmation per
sign-up (and any resend), so the free tier holds roughly 650 subscribers with room
for confirmations. `milesahead_subscribers_total` shows where the count stands;
check OCI's current pricing before the list grows past that.
