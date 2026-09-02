#!/usr/bin/env python3
"""Cross-check card prose against structured card data.

The Amex Gold shipped with "2x on prepaid hotels" in four prose locations while
Amex publishes 5x -- and the structured earning_rates said 2x too, so no
single-source check would have caught it. What does catch this class of bug is
disagreement: between prose and structured data, or between two prose sections
of the same card.

This reports candidates for human review. It cannot know which side is right,
and some hits are legitimate -- a rate that differs by booking channel, or a
competitor's rate quoted inside a comparison table. Treat the output as a triage
list, not a defect list.

Usage:
    python3 scripts/check_card_consistency.py
    python3 scripts/check_card_consistency.py --slug amex-gold
"""
import argparse
import json
import re
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CARDS = ROOT / "cmd/seed-cards/cards.json"
STRUCTURED = ROOT / "cmd/seed-structured/structured-data.json"

# Prose category keywords -> the structured earning_rates keys they should agree with.
CATEGORIES = {
    "dining": (r"restaurant|dining", ["dining"]),
    "groceries": (r"supermarket|grocer", ["groceries"]),
    "flights": (r"flight|airfare|airline", ["airlines_direct", "airlines_portal"]),
    "hotels": (r"hotel", ["hotels_direct", "hotels_portal"]),
    "gas": (r"gas station|at gas\b", ["gas"]),
}

# Sections that quote OTHER cards' rates, where disagreement is expected.
COMPARISON_SECTIONS = {"competitor_comparison"}


def segments(html: str):
    """Split prose into block-level segments before matching.

    Rates live one per list item. Matching across an item boundary produced
    false positives like "1x) 3x points: Flights", where the 1x belongs to the
    previous bullet entirely.
    """
    blocks = re.split(r"</li>|</p>|<br\s*/?>|</tr>", html, flags=re.I)
    return [re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", b)).strip() for b in blocks]


def prose_rates(sections):
    """category -> [(rate, section_key, snippet)] found in prose."""
    found = defaultdict(list)
    for s in sections:
        for text in segments(s.get("content", "")):
            for cat, (kw, _) in CATEGORIES.items():
                for m in re.finditer(rf"(\d+)x[^.;]{{0,60}}?(?:{kw})", text, re.I):
                    found[cat].append((f"{m.group(1)}x", s.get("key", "?"), m.group(0).strip()[:70]))
    return found


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--slug", help="check a single card")
    args = ap.parse_args()

    cards = {c["slug"]: c for c in json.loads(CARDS.read_text())}
    structured = {c["slug"]: c for c in json.loads(STRUCTURED.read_text())}

    slugs = [args.slug] if args.slug else sorted(cards)
    total = 0

    for slug in slugs:
        card = cards.get(slug)
        if not card:
            print(f"{slug}: not found in cards.json")
            continue
        rates = structured.get(slug, {}).get("earning_rates", {}) or {}
        findings = []

        for cat, hits in prose_rates(card.get("sections", [])).items():
            core = sorted({(r, k, s) for r, k, s in hits if k not in COMPARISON_SECTIONS})
            distinct = {r for r, _, _ in core}

            if len(distinct) > 1:
                findings.append(("prose disagrees with itself", cat, sorted(distinct), core))
                continue
            if not distinct:
                continue

            prose_rate = next(iter(distinct))
            # Some cards store a rate as a bare string rather than {rate, note}.
            def rate_of(key):
                v = rates.get(key)
                raw = v.get("rate") if isinstance(v, dict) else v if isinstance(v, str) else None
                if not raw:
                    return None
                # Structured rates are sometimes a bare "4x" and sometimes a
                # sentence ("2x Rapid Rewards points (first $5,000...)").
                # Compare only the leading multiplier.
                m = re.match(r"\s*(\d+)x", raw, re.I)
                return f"{m.group(1)}x" if m else None

            expected = {rate_of(k) for k in CATEGORIES[cat][1]}
            expected -= {None, "N/A", ""}
            if expected and prose_rate not in expected:
                findings.append(("prose vs structured", cat,
                                 [f"prose {prose_rate}", f"structured {sorted(expected)}"], core))

        if findings:
            total += len(findings)
            print(f"\n{slug}")
            for kind, cat, summary, ctx in findings:
                print(f"  {kind} — {cat}: {', '.join(map(str, summary))}")
                for _, k, s in ctx[:3]:
                    print(f"      [{k}] {s}")

    print(f"\n{len(slugs)} cards checked, {total} finding(s).")


if __name__ == "__main__":
    main()
