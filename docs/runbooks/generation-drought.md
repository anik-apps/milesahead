# Runbook: no articles generated

The site publishes nothing while looking entirely healthy. Scanning, the web
service, `/healthz` and the deploy pipeline all stay green, because none of them
depend on article generation.

This happened for **26 days** starting 2026-08-26 before anyone noticed.

## Confirm it

```bash
curl -s https://milesahead.kumaranik.com/healthz          # will say ok — not useful here
ssh ubuntu@<host> "journalctl -u milesahead -n200 | grep -E 'article created|generation skipped'"
```

Scanning filling `raw_articles` while `generated_articles` stands still is the
signature. Check both:

```bash
ssh ubuntu@<host> "python3 -c \"
import sqlite3
c=sqlite3.connect('file:/opt/milesahead/data/milesahead.db?mode=ro',uri=True)
print('raw      ', c.execute('select max(created_at) from raw_articles').fetchone())
print('generated', c.execute('select max(created_at) from generated_articles').fetchone())\""
```

## Most likely cause: a model was retired

Providers retire models on their own schedule, mid-deployment, and answer
`410 Gone` forever after. In the 2026-08-26 incident the generator, the critic
**and** the only fallback were all retired within the same window.

Check the boot-time preflight first — it names the culprit directly:

```bash
ssh ubuntu@<host> "journalctl -u milesahead | grep preflight | tail -20"
```

`preflight FAILED` names the model and role. If the service has not restarted
since the retirement, preflight will not have run against it — restart it, or
probe the models directly.

## Do not trust the provider's model catalogue

`GET /v1/models` lists models that do not work. Measured 2026-09-21 on
`integrate.api.nvidia.com`: of 81 models advertised, 49 looked chat-capable and
**only 9 answered**. `openai/gpt-oss-120b` was still listed while returning 410.

Verify candidates by calling them:

```bash
go run ./cmd/ab-models -critic-candidates "model-a,model-b" -generators model-a
go run ./cmd/ab-models -topics 5 -generators "model-a,model-b" -critic model-c
```

Liveness is not capability. `mistralai/mistral-nemotron` (the critic until its
retirement on 2026-09-28) passed a trivial probe and still returned 500 on the
real article prompt, so score candidates on real clusters before promoting one.

Latency matters as much as liveness. `deepseek-ai/deepseek-v4.1-flash` scored
well as critic on 2026-09-29, then its hosted endpoint answered every call with a
gateway 504 after ~5 minutes — and a critic *error* drops the article from the
scan (a low score only makes it a draft). The critic is a chain now, and critic
candidates should be measured with thinking off (`ab-models -critic-no-thinking`),
since production sends them that way (`critic_disable_thinking`). A critic only
verifies, with the full `request_timeout`; everything else — enrichment, image text-scoring, the
newsletter pick, card-update detection — runs on `enrichment_model` under a 30s
cap. Keep slow models out of that chain.

## Fix

Update `generator_model` / `fallback_generator_model` / `critic_model` (a chain) in
`.github/workflows/deploy.yml` and `config.example.yaml`, then merge to main.
The `config` job writes `config.yaml` and `deploy` depends on it, so the restart
always follows the write.

Confirm afterwards:

```bash
ssh ubuntu@<host> "journalctl -u milesahead -n30 | grep preflight"   # expect preflight ok
```

Article-path models are probed under the full `request_timeout`, so a slow
critic's `preflight ok` can take several minutes to appear
(`deepseek-ai/deepseek-v4.1-flash` took about five). The same results are in
`milesahead_ai_preflight_models_reachable{role}`; for `role="critic"` it counts
how many critics in the chain answered, and one is enough to publish. Which
critic scored each article is in the `critic scored` log line (`model`).

Then wait one scan interval (`default_interval`, 180 minutes) for `article created`.

## Notes on the alert itself

`milesahead_articles_generated_total` is a `CounterVec`, so its series does not
exist until the first article is recorded. During an outage the metric is
**absent, not zero** — which is why the drought rule treats NoData as firing. A
rule written the obvious way (`== 0`) would have stayed silent through the whole
26 days.

A fresh deploy also starts with the series absent, so expect the rule to be
pending until the first article of the new process. That is what `for: 1h` and
the 3-hour scan interval accommodate.
