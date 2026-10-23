---
title: Model Health
weight: 5
---

Every AI step has a list of models, and a backup in a list is only called when the one before it fails. So a backup can die and nobody sees it. On 2026-10-20 both critic models had been retired, publishing stopped for two and a half days, and no alert reached anyone. Model health checks every model in every list on a schedule, records the outcome of every real call, shows the result on the admin **Models** tab, and emails the admin when a model dies, when a list runs on a backup, when a list has nothing left, and when publishing stops for any reason. The code is `internal/health`; outcomes are classified in `internal/ai/outcome.go`.

The lists stay in config (`deploy.yml` writes `config.yaml` on every deploy). The Models tab is read-only: changing a list is a pull request and a deploy.

## What it watches

One step per distinct model list. `health.Steps(cfg)` is the single definition, used by the wiring in `cmd/milesahead/main.go`, the check, the state evaluation and the page.

| Step | List | Jobs that use it |
|---|---|---|
| `generator` | `generator_model`, then `fallback_generator_model` | Article generation and revisions |
| `critic` | `critic_model` | Verifying every article (thinking off) |
| `enrichment` | `enrichment_model`; unset, the generator fallbacks then the critics | Scout's Take, key details, tags, image queries, image text-scoring, the newsletter pick, card-update detection, story dedup |
| `infographic` | `infographic_model`; unset, the enrichment list | Charts |
| `vision` | `vision_model`, a comma-separated list (default `meta/llama-3.2-11b-vision-instruct`) | Image scoring |

A step that is switched off is not checked and shows as "off": `infographic` when `ai.enrichment_infographics` is `off`, and `vision` when `ai.hf_base_url` is empty. `critic2_model` is not a step, because nothing calls it.

## How a model is judged

Each model in each step has one row in `model_health`, updated in place by the health worker. Two things feed it: the outcome of every real call a job makes, and the hourly check.

**Outcomes.** A call ends in one of seven outcomes, after the client's own retries:

| Outcome | When |
|---|---|
| `ok` | Usable content came back |
| `retired` | 404 or 410, or a 200 whose body says the model is retired |
| `auth` | 401, 402 or 403: the key, not the model |
| `timeout` | No answer in time, or a gateway 504 |
| `overloaded` | 429 or any other 5xx |
| `bad_reply` | Empty or cut-off content, an unparseable body, or a critic reply whose JSON will not parse |
| `error` | Anything else |

The critic's client records a success only after the reply parses as a verdict (`WithConfirmedOK`). A critic that answers in prose therefore counts as failing (`bad_reply`) rather than resetting its failure count on every call.

**When a model is failing.**

- 2 failed checks in a row, or 3 failed real calls in a row within the last 6 hours. One blip is not a failure.
- `retired` and `auth` count at once, on the first one.
- Any newer `ok`, from a call or a check, clears the failure.

A model that has not been checked or called yet is `unknown`: shown as such, and not counted as failing.

**Recording never blocks an AI call.** Outcomes go into a buffered channel read by one goroutine, the health worker. When the buffer (1024) is full the outcome is dropped and counted in `milesahead_model_events_dropped_total`. The worker is the only writer, so SQLite's `busy_timeout` can never stall a model call. A client that has no step label (tests, `ab-models`) records nothing.

**The check** is the same preflight that runs at boot. It runs 30 seconds after startup, then every `ai.health_check_interval` minutes. It probes each model once per preflight role, with that role's budget:

- `generator`, `fallback` and `critic`: 4096 tokens, which gets the full request timeout. Critics are probed through the critic's client (thinking off).
- `enrichment` and `vision`: 512 tokens, which gets 30 seconds.

Probes run at the same time, so a round lasts as long as its slowest probe, which for a slow critic can be minutes. Each step takes the results of its own roles (the `generator` step takes `generator` and `fallback`), so a model in two lists with different roles can pass one check and fail the other. Infographic models are checked through the `enrichment` role: a slow chart model can fail its 30-second check while its real chart calls, which get 120 seconds, work.

## List states

| State | Meaning |
|---|---|
| `ok` | The first model in the list is healthy (or unknown) |
| `degraded` | The first model is failing or retired and a backup answers |
| `down` | No model in the list is healthy |
| `off` | Switched off in config |

Two labels sit beside `ok`:

- **no working backup**: the list is `ok`, it has backups, and every one of them is failing or retired. This is the case that hid the 2026-10-15 retirement of the critic's second model for five days. It counts in the tab's badge.
- **no backup configured**: the list has one model. It does not count in the badge, so vision's single default model does not keep the badge lit.

The tab's badge counts the lists that are not `ok`, plus `ok` lists marked "no working backup". It turns red when publishing has stopped.

## The Models tab

Between Sources and Subscribers. At the top, in order:

1. **Publishing**: "working, last article ..." or "stopped since ..." with the reason, and a note that stories older than 48 hours are being lost.
2. **Alerts**: who they go to, when the last email was sent and whether it failed, and the **Send test alert** button. When there is no address, or SMTP is not configured, it says why in red.
3. **Last check**: when the last check round finished, as a UTC time ("Oct 22 14:02 UTC"), with the interval beside it ("every 60 min"). A red "late" chip appears when no check has finished for two intervals. With the hourly check off it says "hourly check off" instead of the interval.

Below that, each list (down, degraded, ok with no working backup, ok, off) with its models in order: status, last OK, failing since, last error, which model answered the list's last real call, and what to do about a model that has failed (see below).

The Review tab shows a one-line banner only while publishing has stopped or alerts are not being emailed. Settings links to the Models tab.

## Emails

The worker evaluates every list after each check round, and 5 seconds after the first failed call of a batch. The delay batches failures: a critic list fails model after model within a second, and judging after the first would send "running on a backup" moments before "down". It also covers every other failure that arrived in those 5 seconds, so one outage sends one email.

| Event | Severity |
|---|---|
| A backup in a list becomes `retired`, `auth` or failing while the list is still `ok` ("Critic: backup X is retired") | Warning |
| A list becomes `degraded` ("Critic is running on a backup") | Warning |
| `generator` or `critic` becomes `down` ("publishing has stopped") | Urgent |
| `enrichment`, `infographic` or `vision` becomes `down` | Warning |
| Every model that has been seen, in every list, is `auth`, or every one is `retired` ("API key or provider problem"). Replaces all the per-list events | Urgent |
| The publishing drought (below) | Urgent |
| `generator` or `critic` leaves `down` ("Critic is back: publishing has resumed", or "Critic answers again on a backup: publishing has resumed") | Recovered |
| The provider problem or the drought clears | Recovered |

The rules:

- **One email per evaluation.** All the events of one evaluation go in one email, most severe first. The subject names the most severe, with "(+N more)".
- **Urgent repeats daily** while it lasts, with "Still:" in front. Warnings never repeat: if things get worse, the urgent email is the reminder.
- **A warning has a 24-hour cool-down.** After a warning is emailed, its list (or backup) sends no other warning for 24 hours; changes in that time are shown on the tab, not emailed. A list flapping between `degraded` and `ok` sends one email a day at most.
- **Only urgent states email their recovery.** A list going back to `ok` from `degraded`, a list that does not stop publishing coming back, and a backup coming back are shown on the tab and not emailed.
- **An alert is saved only after its email went out.** State lives in `model_alerts`. A failed send saves nothing, is retried at the next evaluation, is shown under Alerts on the tab, and is counted in `milesahead_emails_sent_total{kind="model_alert",outcome="failed"}`. When nothing can be sent, nothing is saved. A restart does not re-send, because the state survives it.
- **Recipient:** `ai.alert_email`, else `votes.alert_email`. `deploy.yml` writes `ai.alert_email` from the `ALERT_EMAIL` secret and emits a deploy warning when it is empty.
- **With no address, or no SMTP,** nothing is emailed and nothing is saved. The events are logged at WARN as `model health alert not emailed` at every evaluation, and the tab and the Review banner say alerts are not emailed. The first email after an address is configured lists everything that is wrong at that moment; problems that cleared in the meantime are not mentioned.
- **Send test alert** (`POST /admin/models/test-alert`, admin login and CSRF token required) sends a one-line email to the recipient, to prove the address and SMTP work. It updates the last-send line under Alerts on the tab (and its error, if it failed) and the `milesahead_emails_sent_total{kind="model_alert"}` counter, as `sent` or `failed`. It does not change any alert state.

## The drought alarm

The one alarm that does not depend on knowing the cause. With each check round the worker asks two questions: has any `generated_articles` row of any status been created in the last 24 hours, and have 2 or more sources added raw articles in that time. No articles with 2 or more active sources is an urgent "No article in 24 hours" email, repeated daily.

It catches what model health cannot see: a critic answering in prose, story dedup in `block` mode eating every cluster, a pipeline bug, a scanner fault. The page shows the stored result and never runs the count itself.

Two details:

- Until the first check after a restart has measured the drought, the stored alert state stands in. Without that, a restart during a drought would read as "no drought" and email a false "recovered".
- A failed measurement keeps the last known answer. With the hourly check off (`health_check_interval` negative) the drought is measured only at boot.

## The scan circuit breaker and revisions

While every critic fails, each cluster in a scan used to be generated and then thrown away: about 130 wasted generator calls a day in the 2026-10-20 outage.

- `Pipeline.Process` returns an error wrapping `ai.ErrCriticUnavailable` when no model in the critic list answered: each was retired, refused the key, was overloaded, timed out or returned nothing usable.
- The cluster loop in `cmd/milesahead/scan.go` stops on it, logs `generation stopped for this scan: every critic failed` with the clusters left, and the next scan tries again. The cost while the critics are down is one generator call per scan.
- The revision job treats the same error as "try later". The article stays `revision_requested` and the failure does not count against its 5 retries, so an outage does not push articles into the admin's to-do list. It is tried again in an hour (`revision deferred: no critic answered`). The pass stops there, logs `revision pass stopped: no critic answered`, and puts the revisions it had not reached off for the same hour, untried. The cost while the critics are down is one generator call an hour.
- An article-specific refusal does not trip it. A non-retryable error the chain stopped on (a 400, 413 or 422: prompt too large, content filter) is about that article, not the list. Nor does a critic that replies in prose: that is a `bad_reply`, which model health and the drought alarm pick up.

It cannot lock itself on, since each scan tries again, and a false alarm from the check cannot trigger it. Nothing is marked covered when it stops, which is the same state any failed generation leaves. Stories whose sources age out of the 48-hour window before the critics return are lost; the urgent email is what keeps that window short.

## Vision lists

`vision_model` takes a comma-separated list. Within one image-scoring call, once a vision model fails with `retired`, `timeout` or `overloaded`, the rest of that call's images use the next model. When every vision model has failed, text scoring stands, as before. A 4xx about the image itself (a thumbnail the provider cannot fetch) is not counted against the model. A score that will not parse is a `bad_reply`.

## What to do

The tab shows these next to the failing model, and the emails include them.

| Status | What to do |
|---|---|
| `retired` | Pick a replacement with `go run ./cmd/ab-models -critic-candidates "model-a,model-b"`, change the list in `.github/workflows/deploy.yml` and `config.example.yaml`, merge and deploy. The [runbook](https://github.com/anik-apps/milesahead/blob/main/docs/runbooks/generation-drought.md) has the detail. |
| `auth` | Rotate the `NVIDIA_API_KEY` secret and redeploy. When every model shows `auth`, it is the key, not the models. |
| failing (`timeout`, `overloaded`, `bad_reply`) | Usually passes. If it lasts over a day, move the model down its list. |

A test fails when a known-retired model is in any list of `config.example.yaml` or `deploy.yml`, and the lists must match between the two files.

## Config

```yaml
ai:
  alert_email: ""            # model and drought alerts; empty uses votes.alert_email
  health_check_interval: 60  # minutes; absent or 0 = 60; negative turns the hourly check off
```

| Setting | Meaning |
|---|---|
| `ai.alert_email` | Where alerts go. A plain address; `config.Load` rejects anything else. Empty uses `votes.alert_email` |
| `ai.health_check_interval` | Minutes between checks. Absent or 0 means 60. Negative turns the hourly check off; one check still runs at boot |
| `ALERT_EMAIL` secret | The GitHub repository secret `deploy.yml` writes into `ai.alert_email` and `votes.alert_email`. Until it is set, alerts are shown on the tab and not emailed |

## Metrics

| Metric | Meaning |
|---|---|
| `milesahead_model_step_state{step}` | 0 `ok`, 1 `degraded`, 2 `down`. No series for a list that is off |
| `milesahead_model_check_last_success_timestamp_seconds` | When the last check round finished |
| `milesahead_model_events_dropped_total` | Call outcomes dropped because the worker was behind |
| `milesahead_emails_sent_total{kind="model_alert"}` | Alert emails, by `outcome` (`sent`, `failed`). Created at zero at startup |

`milesahead_ai_preflight_models_reachable{role}` is still set by every check round. Per-call history is not stored in the database; it is in Loki, in the `ai api call` log line.

## Tables

| Table | Purpose |
|---|---|
| `model_health` | One row per (step, model): last OK, last failure, failing since, both failure counts, last outcome and error (300 bytes), last check, last real answer |
| `model_alerts` | The state last emailed for each alert key (`step:critic`, `backup:critic:model`, `provider`, `drought`) |
| `model_alert_sends` | The last alert email attempt and its error, for the Models tab |
