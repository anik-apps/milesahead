#!/usr/bin/env python3
"""Render generated articles into a single self-contained review page.

Reading candidate output as raw JSON hides the thing you actually need to judge:
whether the prose is publishable. This lays each article out as it would read on
the site, next to its critic score and the sources it was built from, so a model
can be accepted or rejected on the page rather than on a number.

Given more than one input file, it groups by topic and shows each model's take on
the SAME sources one after another, which is the only way to see that one model
padded, another hedged, and a third invented a figure.

Usage:
    python3 scripts/render_article_review.py articles.json > review.html
    python3 scripts/render_article_review.py a.json b.json > compare.html
"""
import html
import json
import sys
from datetime import datetime, timezone

CSS = """
:root{--bg:#f6f7f9;--card:#fff;--ink:#14181f;--muted:#5d6672;--line:#e3e7ec;
--good:#0f7b46;--goodbg:#e6f5ed;--bad:#a3341f;--badbg:#fbecea;--accent:#1f4fd8}
@media(prefers-color-scheme:dark){:root{--bg:#0f1216;--card:#171b21;--ink:#e8ecf1;
--muted:#9aa4b2;--line:#252b33;--good:#4ade80;--goodbg:#10291d;--bad:#f87171;
--badbg:#2a1614;--accent:#7aa2ff}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
font:16px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
.wrap{max-width:860px;margin:0 auto;padding:48px 20px 96px}
header h1{font-size:1.65rem;margin:0 0 6px;letter-spacing:-.02em}
header p{color:var(--muted);margin:0 0 8px}
.meta{color:var(--muted);font-size:.85rem}
.summary{display:flex;gap:10px;flex-wrap:wrap;margin:22px 0 34px}
.stat{background:var(--card);border:1px solid var(--line);border-radius:10px;
padding:10px 14px;min-width:104px}
.stat b{display:block;font-size:1.3rem;line-height:1.2}
.stat span{color:var(--muted);font-size:.75rem;text-transform:uppercase;letter-spacing:.06em}
article{background:var(--card);border:1px solid var(--line);border-radius:14px;
padding:26px 28px;margin-bottom:26px}
article h2{font-size:1.3rem;margin:0 0 4px;letter-spacing:-.01em}
.topic{color:var(--muted);font-size:.8rem;margin-bottom:14px}
.badges{display:flex;gap:8px;flex-wrap:wrap;margin:0 0 16px}
.badge{font-size:.78rem;font-weight:600;padding:3px 10px;border-radius:999px;
border:1px solid transparent}
.pass{background:var(--goodbg);color:var(--good);border-color:var(--good)}
.fail{background:var(--badbg);color:var(--bad);border-color:var(--bad)}
.neutral{background:transparent;color:var(--muted);border-color:var(--line)}
.tldr{background:var(--bg);border-left:3px solid var(--accent);padding:12px 16px;
border-radius:0 8px 8px 0;margin:0 0 18px;font-size:.95rem}
.tldr b{display:block;font-size:.72rem;text-transform:uppercase;letter-spacing:.08em;
color:var(--muted);margin-bottom:4px}
.body{font-size:1rem}
.body h2,.body h3{font-size:1.05rem;margin:22px 0 8px}
.body a{color:var(--accent)}
.body ul{padding-left:20px}
.kd{margin:20px 0 0;border-top:1px solid var(--line);padding-top:16px}
.kd table{width:100%;border-collapse:collapse;font-size:.9rem}
.kd td{padding:5px 0;vertical-align:top}
.kd td:first-child{color:var(--muted);width:35%;padding-right:14px}
details{margin-top:18px;border-top:1px solid var(--line);padding-top:14px}
summary{cursor:pointer;color:var(--muted);font-size:.85rem}
.src{font-size:.85rem;color:var(--muted);margin:10px 0 0;padding-left:16px}
.issue{color:var(--bad);font-size:.88rem;margin:4px 0 0 18px}
.err{background:var(--badbg);color:var(--bad);padding:12px 14px;border-radius:8px;
font-family:ui-monospace,monospace;font-size:.85rem}
.topicgroup{margin:0 0 42px}
.topichead{font-size:.78rem;text-transform:uppercase;letter-spacing:.09em;
color:var(--muted);border-bottom:1px solid var(--line);padding-bottom:8px;margin-bottom:18px}
.model{font-family:ui-monospace,monospace;font-size:.78rem;color:var(--accent);
margin-bottom:10px;word-break:break-all}
table.cmp{width:100%;border-collapse:collapse;margin:0 0 34px;font-size:.9rem}
table.cmp th,table.cmp td{text-align:left;padding:8px 10px;border-bottom:1px solid var(--line)}
table.cmp th{color:var(--muted);font-size:.75rem;text-transform:uppercase;letter-spacing:.06em}
table.cmp td.num{font-variant-numeric:tabular-nums}
"""


def esc(s):
    return html.escape(s or "")


def field(d, *names):
    """Read a field regardless of case.

    Go structs without json tags marshal as "Label"/"Value"; ones with tags
    marshal lowercase. The dump mixes both, so accept either rather than
    silently rendering empty rows.
    """
    for n in names:
        if isinstance(d, dict) and d.get(n) not in (None, ""):
            return d[n]
    return ""


def main():
    if len(sys.argv) < 2:
        raise SystemExit(__doc__)
    data = []
    for path in sys.argv[1:]:
        data.extend(json.loads(open(path, encoding="utf-8").read()))
    if not data:
        raise SystemExit("no articles in input")

    models = []
    for a in data:
        if a.get("model") not in models:
            models.append(a.get("model"))
    compare = len(models) > 1

    ok = [a for a in data if not a.get("err")]
    passed = [a for a in ok if a.get("passed")]
    scores = [a["score"] for a in ok if a.get("score")]
    avg = round(sum(scores) / len(scores)) if scores else 0
    secs = [a.get("seconds", 0) for a in data]
    model = " vs ".join(models) if compare else (models[0] or "unknown")

    out = [
        "<!doctype html><html lang='en'><head><meta charset='utf-8'>",
        "<meta name='viewport' content='width=device-width,initial-scale=1'>",
        "<title>Generated article review</title>",
        f"<style>{CSS}</style></head><body><div class='wrap'>",
        "<header><h1>Generated article review</h1>",
        f"<p>Model: <code>{esc(model)}</code></p>",
        f"<p class='meta'>Full pipeline: generate, critic-verify, auto-revise. "
        f"Rendered {datetime.now(timezone.utc).strftime('%Y-%m-%d %H:%M UTC')}.</p></header>",
        "<div class='summary'>",
        f"<div class='stat'><b>{len(data)}</b><span>attempted</span></div>",
        f"<div class='stat'><b>{len(ok)}</b><span>generated</span></div>",
        f"<div class='stat'><b>{len(passed)}</b><span>above threshold</span></div>",
        f"<div class='stat'><b>{avg}</b><span>avg score</span></div>",
        f"<div class='stat'><b>{round(sum(secs)/max(len(secs),1))}s</b><span>avg time</span></div>",
        "</div>",
    ]

    if compare:
        out.append("<table class='cmp'><tr><th>Model</th><th>Generated</th>"
                   "<th>Avg score</th><th>Above threshold</th><th>Avg sec</th>"
                   "<th>Avg chars</th></tr>")
        for m in models:
            rows = [a for a in data if a.get("model") == m]
            good = [a for a in rows if not a.get("err")]
            sc = [a["score"] for a in good if a.get("score")]
            ch = [len(a.get("content") or "") for a in good]
            out.append(
                f"<tr><td><code>{esc(m)}</code></td>"
                f"<td class='num'>{len(good)}/{len(rows)}</td>"
                f"<td class='num'>{round(sum(sc)/len(sc)) if sc else 0}</td>"
                f"<td class='num'>{len([a for a in good if a.get('passed')])}</td>"
                f"<td class='num'>{round(sum(a.get('seconds',0) for a in rows)/max(len(rows),1))}</td>"
                f"<td class='num'>{round(sum(ch)/len(ch)) if ch else 0}</td></tr>")
        out.append("</table>")

    # Group by topic so the same sources can be compared across models.
    groups = {}
    for a in data:
        groups.setdefault(a.get("topic"), []).append(a)

    for topic_name, items in groups.items():
        if compare:
            out.append("<div class='topicgroup'>")
            out.append(f"<div class='topichead'>Topic &mdash; {esc(topic_name)}</div>")
        for a in items:
            out.append(render_article(a, compare))
        if compare:
            out.append("</div>")

    out.append("</div></body></html>")
    print("\n".join(out))


def render_article(a, show_model):
    out = []
    out.append("<article>")
    if show_model:
        out.append(f"<div class='model'>{esc(a.get('model'))}</div>")
    if a.get("err"):
        out.append(f"<h2>{esc(a['topic'])}</h2>")
        out.append(f"<div class='err'>{esc(a['err'])}</div></article>")
        return "\n".join(out)

    cls = "pass" if a.get("passed") else "fail"
    verdict = "above threshold" if a.get("passed") else "below threshold"
    out.append(f"<h2>{esc(a.get('title'))}</h2>")
    out.append(f"<div class='topic'>from cluster: {esc(a.get('topic'))}</div>")
    out.append("<div class='badges'>")
    out.append(f"<span class='badge {cls}'>critic {a.get('score', 0)} &middot; {verdict}</span>")
    out.append(f"<span class='badge neutral'>{round(a.get('seconds', 0))}s</span>")
    out.append(f"<span class='badge neutral'>{len(a.get('sources', []))} sources</span>")
    out.append("</div>")

    if a.get("tldr"):
        out.append(f"<div class='tldr'><b>TL;DR</b>{esc(a['tldr'])}</div>")

        # Content is model-authored HTML; it is rendered here deliberately so the
        # markup itself can be reviewed. This page is a local artifact, never served.
    out.append(f"<div class='body'>{a.get('content','')}</div>")

    if a.get("key_details"):
        out.append("<div class='kd'><table>")
        for kd in a["key_details"]:
            out.append(f"<tr><td>{esc(field(kd, 'label', 'Label'))}</td>"
                       f"<td>{esc(field(kd, 'value', 'Value'))}</td></tr>")
        out.append("</table></div>")

    if a.get("issues"):
        out.append("<details open><summary>Critic issues "
                   f"({len(a['issues'])})</summary>")
        for i in a["issues"]:
            out.append(f"<div class='issue'>&bull; {esc(i)}</div>")
    out.append("</details>")

    out.append(f"<details><summary>Sources ({len(a.get('sources', []))})</summary>")
    for s in a.get("sources", []):
        out.append(f"<div class='src'>&bull; <b>{esc(field(s, 'Name', 'name'))}</b> &mdash; "
                   f"<a href='{esc(field(s, 'URL', 'url'))}'>"
                   f"{esc(field(s, 'Title', 'title'))}</a></div>")
    out.append("</details>")
    out.append("</article>")

    return "\n".join(out)


if __name__ == "__main__":
    main()
