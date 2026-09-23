# Generates the misty-mountain artwork as SVG, matching the strip at the bottom
# of the card infographics:
#   band-light.svg / band-dark.svg    footer band and home hero (1600x220)
#   strip-light.svg / strip-dark.svg  faint ridges along the bottom of the header (1600x64)
#   plane.svg / plane-dark.svg        the plane and its contrail
# Deterministic: ridgelines come from fixed seeds, so re-running reproduces the
# committed files. The logo mark (mark.svg) is drawn by hand, not generated.
#
#   python3 scripts/brand/gen_band.py
import itertools
import os
import random

OUT = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "..", "..", "static", "img", "brand"
)


def fractal(seed, n=7, rough=0.58):
    """Midpoint-displacement ridgeline, normalised to 0..1 (1 = highest peak)."""
    rnd = random.Random(seed)
    pts = [rnd.uniform(-1, 1), rnd.uniform(-1, 1)]
    disp = 1.0
    for _ in range(n):
        new = []
        for a, b in itertools.pairwise(pts):
            new += [a, (a + b) / 2 + rnd.uniform(-disp, disp)]
        new.append(pts[-1])
        pts = new
        disp *= rough
    lo, hi = min(pts), max(pts)
    return [(p - lo) / (hi - lo) for p in pts]


def ridge(w, h, seed, base, amp, rough):
    ys = fractal(seed, 7, rough)
    n = len(ys) - 1
    d = f"M0,{h} " + "".join(
        f"L{-10 + (w + 20) * i / n:.1f},{base - amp * v:.1f} " for i, v in enumerate(ys)
    )
    return d + f"L{w + 10},{h} Z"


def write(name, text):
    with open(os.path.join(OUT, name), "w") as f:
        f.write(text)


BAND_LAYERS = [
    (5, 98, 100, 0.62),
    (17, 124, 92, 0.60),
    (29, 150, 82, 0.58),
    (41, 176, 70, 0.56),
    (53, 200, 54, 0.54),
    (67, 222, 34, 0.50),
]


def band(name, palette):
    """Six layered ridges, each darker at its crest and fading into valley mist."""
    w, h = 1600, 220
    s = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" preserveAspectRatio="xMidYMax slice" aria-hidden="true">',
        "<defs>",
    ]
    for i, (top, bottom) in enumerate(palette):
        s.append(
            f'<linearGradient id="r{i}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="{top}"/><stop offset=".55" stop-color="{bottom}"/></linearGradient>'
        )
    s.append("</defs>")
    for i, (seed, base, amp, r) in enumerate(BAND_LAYERS):
        s.append(f'<path fill="url(#r{i})" d="{ridge(w, h, seed, base, amp, r)}"/>')
    s.append("</svg>")
    write(name, "\n".join(s))


STRIP_LAYERS = [(5, 34, 30, 0.62), (29, 48, 26, 0.58), (53, 62, 18, 0.54)]


def strip(name, fills):
    w, h = 1600, 64
    s = [
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" preserveAspectRatio="xMidYMax slice" aria-hidden="true"><g opacity="1">'
    ]
    for (seed, base, amp, r), fill in zip(STRIP_LAYERS, fills):
        s.append(f'<path fill="{fill}" d="{ridge(w, h, seed, base, amp, r)}"/>')
    s.append("</g></svg>")
    write(name, "\n".join(s))


# Material Design "flight" glyph (Apache-2.0), rotated to fly up-right along the contrail.
PLANE = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 60" width="320" height="60" aria-hidden="true">
<defs><linearGradient id="t" x1="0" y1="0" x2="1" y2="0"><stop offset="0" stop-color="{trail}" stop-opacity="0"/><stop offset="1" stop-color="{trail}" stop-opacity=".85"/></linearGradient></defs>
<path d="M4 56 Q160 40 282 24" fill="none" stroke="url(#t)" stroke-width="1.5" stroke-linecap="round"/>
<g transform="translate(298 20) rotate(68) scale(1.9) translate(-12 -12)" fill="{plane}"><path d="M21 16v-2l-8-5V3.5c0-.83-.67-1.5-1.5-1.5S10 2.67 10 3.5V9l-8 5v2l8-2.5V19l-2 1.5V22l3.5-1 3.5 1v-1.5L13 19v-5.5l8 2.5z"/></g>
</svg>"""

if __name__ == "__main__":
    band(
        "band-light.svg",
        [
            ("#bcd9f1", "#e6f2fb"),
            ("#a6cdee", "#dcecf9"),
            ("#8fc0ea", "#cfe5f7"),
            ("#78b1e3", "#c2ddf4"),
            ("#62a0dc", "#b3d4f0"),
            ("#4f90d3", "#a6cbec"),
        ],
    )
    band(
        "band-dark.svg",
        [
            ("#1b2d50", "#0f1a31"),
            ("#203660", "#122039"),
            ("#25406f", "#152643"),
            ("#2a4a80", "#182c4e"),
            ("#305592", "#1b3259"),
            ("#3761a3", "#1f3a66"),
        ],
    )
    strip("strip-light.svg", ["#e4f0fa", "#d2e6f6", "#bcd8f2"])
    strip("strip-dark.svg", ["#16243f", "#1b2d4f", "#22385f"])
    write("plane.svg", PLANE.format(trail="#4f7fc4", plane="#15307a"))
    write("plane-dark.svg", PLANE.format(trail="#8fb4ea", plane="#d6e7ff"))
