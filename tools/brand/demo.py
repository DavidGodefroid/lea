#!/usr/bin/env python3
"""Generate assets/demo.svg: lea's value in one looping image.

Three agents work side by side in the lea container; one pushes to GitHub through the proxy,
then tries your SSH keys and your private network and is refused; another asks for a
permission that you answer from the deck. Pure SVG + CSS keyframes, so it plays on GitHub,
on the site and in any browser. Every animation shares one 12 s clock: render-gif.sh seeks it.

    python3 tools/brand/demo.py > assets/demo.svg
"""

LOOP = 12.0
C = dict(crust="#11111b", mantle="#181825", base="#1e1e2e", s0="#313244", s1="#45475a",
         ov="#6c7086", sub="#a6adc8", text="#cdd6f4", blue="#89b4fa", red="#f38ba8",
         peach="#fab387", green="#a6e3a1", lav="#b4befe", mauve="#cba6f7")
FONT = "'JetBrains Mono','SF Mono',Menlo,Consolas,'DejaVu Sans Mono',monospace"
SANS = "Inter,'Segoe UI',system-ui,-apple-system,Helvetica,Arial,sans-serif"

css, body = [], []
n = 0


def p(t):
    return f"{t / LOOP * 100:.2f}%"


def anim(frames):
    """Register keyframes [(time_s, 'css decls'), ...] and return a class name."""
    global n
    n += 1
    name = f"k{n}"
    rules = " ".join(f"{p(t)}{{{d}}}" for t, d in frames)
    css.append(f"@keyframes {name}{{{rules}}}")
    css.append(f".{name}{{animation:{name} {LOOP}s linear infinite}}")
    return name


def show(t_in, t_out=11.4, fade=0.25):
    """Opacity window: hidden, visible from t_in to t_out."""
    return anim([(0, "opacity:0"), (t_in, "opacity:0"), (t_in + fade, "opacity:1"),
                 (t_out, "opacity:1"), (t_out + fade, "opacity:0"), (LOOP, "opacity:0")])


def fill(steps):
    """Colour steps [(t, colour)]: holds each colour until the next step."""
    frames = []
    for i, (t, col) in enumerate(steps):
        if i:
            frames.append((max(t - 0.01, 0), f"fill:{steps[i - 1][1]}"))
        frames.append((t, f"fill:{col}"))
    frames.append((LOOP, f"fill:{steps[-1][1]}"))
    return anim(frames)


def flash(t, col, prop="stroke", base=C["s0"]):
    return anim([(0, f"{prop}:{base}"), (t, f"{prop}:{base}"), (t + 0.15, f"{prop}:{col}"),
                 (t + 1.6, f"{prop}:{col}"), (t + 2.2, f"{prop}:{base}"), (LOOP, f"{prop}:{base}")])


def move(t0, t1, x0, y0, x1, y1):
    return anim([(0, f"transform:translate({x0}px,{y0}px);opacity:0"),
                 (t0, f"transform:translate({x0}px,{y0}px);opacity:0"),
                 (t0 + 0.05, f"transform:translate({x0}px,{y0}px);opacity:1"),
                 (t1, f"transform:translate({x1}px,{y1}px);opacity:1"),
                 (t1 + 0.15, f"transform:translate({x1}px,{y1}px);opacity:0"),
                 (LOOP, f"transform:translate({x1}px,{y1}px);opacity:0")])


def text(x, y, s, cls="", size=15, col=C["text"], weight=400, family=FONT, anchor="start"):
    s = s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    return (f'<text x="{x}" y="{y}" class="{cls}" font-family="{family}" font-size="{size}" '
            f'fill="{col}" font-weight="{weight}" text-anchor="{anchor}" xml:space="preserve">{s}</text>')


W, H = 1200, 640
body.append(f'<rect width="{W}" height="{H}" rx="24" fill="{C["crust"]}"/>')

# --- Your machine (left) -------------------------------------------------------------------
body.append(text(40, 70, "YOUR MACHINE", size=13, col=C["ov"], weight=700, family=SANS))
host = [("~/.ssh", "your keys", 5.2), ("~/.aws", "credentials", None),
        ("192.168.1.10", "private network", 7.6)]
for i, (label, sub, hit) in enumerate(host):
    y = 150 + i * 110
    cls = flash(hit, C["red"]) if hit else ""
    body.append(f'<rect x="30" y="{y}" width="230" height="74" rx="12" fill="{C["mantle"]}" '
                f'stroke="{C["s0"]}" stroke-width="2" class="{cls}"/>')
    body.append(text(50, y + 32, label, size=17))
    body.append(text(50, y + 56, sub, size=13, col=C["ov"], family=SANS))
    if hit:
        body.append(f'<g class="{show(hit + 0.1, hit + 2.1)}">'
                    f'<rect x="166" y="{y + 44}" width="80" height="24" rx="12" fill="{C["red"]}"/>'
                    + text(206, y + 61, "blocked", size=12, col=C["crust"], weight=700,
                           family=SANS, anchor="middle") + "</g>")

# --- Internet (right) ----------------------------------------------------------------------
body.append(text(940, 70, "INTERNET", size=13, col=C["ov"], weight=700, family=SANS))
net = [("github.com", "git push", 2.8), ("registry.npmjs.org", "packages", None),
       ("api.anthropic.com", "Claude", None)]
for i, (label, sub, hit) in enumerate(net):
    y = 150 + i * 110
    cls = flash(hit, C["green"]) if hit else ""
    body.append(f'<rect x="940" y="{y}" width="230" height="74" rx="12" fill="{C["mantle"]}" '
                f'stroke="{C["s0"]}" stroke-width="2" class="{cls}"/>')
    body.append(text(960, y + 32, label, size=16))
    body.append(text(960, y + 56, sub, size=13, col=C["ov"], family=SANS))
    if hit:
        body.append(f'<g class="{show(hit + 0.1, hit + 2.1)}">'
                    f'<rect x="1068" y="{y + 44}" width="88" height="24" rx="12" fill="{C["green"]}"/>'
                    + text(1112, y + 61, "allowed", size=12, col=C["crust"], weight=700,
                           family=SANS, anchor="middle") + "</g>")

# --- The lea container ---------------------------------------------------------------------
X0, Y0, X1, Y1 = 300, 96, 900, 540
wall = anim([(0, f"stroke:{C['blue']}")] +
            sum([[(t, f"stroke:{C['blue']}"), (t + 0.1, f"stroke:{C['red']}"),
                  (t + 0.7, f"stroke:{C['blue']}")] for t in (5.2, 7.6)], []) +
            [(LOOP, f"stroke:{C['blue']}")])
body.append(f'<rect x="{X0}" y="{Y0}" width="{X1 - X0}" height="{Y1 - Y0}" rx="20" '
            f'fill="{C["base"]}" stroke="{C["blue"]}" stroke-width="3" class="{wall}"/>')
body.append(f'<rect x="{X0 + 24}" y="{Y0 - 14}" width="236" height="28" rx="14" fill="{C["blue"]}"/>')
body.append(text(X0 + 142, Y0 + 5, "lea · isolated container", size=13, col=C["crust"],
                 weight=700, family=SANS, anchor="middle"))

# Deck column.
DX, DY = 324, 128
body.append(f'<rect x="{DX}" y="{DY}" width="248" height="300" rx="10" fill="{C["mantle"]}" '
            f'stroke="{C["s1"]}" stroke-width="1.5"/>')
body.append(text(DX + 14, DY + 26, "lea ─ 3 agents", size=13, col=C["sub"]))
agents = [
    ("fix login timeout", [(0, C["peach"]), (3.3, C["green"])],
     [(0.3, "shop · fix/login · now"), (3.3, "shop · pushed · done")]),
    ("review cart api", [(0, C["peach"]), (10.2, C["green"])],
     [(0.7, "reviewer · shop · 2m"), (10.2, "reviewer · approved")]),
    ("add dark mode", [(0, C["peach"]), (8.4, C["red"]), (9.6, C["peach"])],
     [(1.1, "shop · ui · working"), (8.4, "needs you: npm install?"), (9.6, "shop · ui · allowed")]),
]
for i, (name, dots, subs) in enumerate(agents):
    y = DY + 56 + i * 76
    t_in = subs[0][0]
    g = [f'<g class="{show(t_in)}">']
    if i == 0:
        g.append(f'<rect x="{DX + 6}" y="{y - 20}" width="236" height="62" rx="8" fill="{C["s0"]}"/>')
        g.append(f'<rect x="{DX + 6}" y="{y - 20}" width="4" height="62" rx="2" fill="{C["blue"]}"/>')
    g.append(f'<circle cx="{DX + 26}" cy="{y - 5}" r="7" class="{fill(dots)}"/>')
    g.append(text(DX + 42, y, name, size=15))
    for j, (t, s) in enumerate(subs):
        t_out = subs[j + 1][0] - 0.01 if j + 1 < len(subs) else 11.4
        g.append(f'<g class="{show(t, t_out, fade=0.05 if j else 0.25)}">'
                 + text(DX + 42, y + 24, s, size=12,
                        col=C["red"] if "needs" in s else C["ov"]) + "</g>")
    g.append("</g>")
    body.append("".join(g))
# Keys hint, lit when the deck answers.
body.append(f'<g class="{show(8.9, 9.7, fade=0.1)}"><rect x="{DX + 14}" y="{DY + 262}" width="220" '
            f'height="26" rx="6" fill="{C["red"]}"/>'
            + text(DX + 124, DY + 280, "a  allow   d  deny", size=13, col=C["crust"], weight=700,
                   anchor="middle") + "</g>")

# Selected agent's terminal.
TX, TY = 590, 128
body.append(f'<rect x="{TX}" y="{TY}" width="290" height="300" rx="10" fill="{C["crust"]}" '
            f'stroke="{C["s1"]}" stroke-width="1.5"/>')
body.append(text(TX + 14, TY + 26, "✻ Claude Code", size=13, col=C["peach"]))
lines = [
    (1.5, "> git push", C["text"]),
    (3.0, "✓ pushed to github.com", C["green"]),
    (4.2, "> cat ~/.ssh/id_ed25519", C["text"]),
    (5.4, "✕ no such file:", C["red"]),
    (5.4, "  your keys are not in lea", C["red"]),
    (6.6, "> curl 192.168.1.10", C["text"]),
    (7.8, "✕ 403: private network", C["red"]),
    (7.8, "  refused by the proxy", C["red"]),
]
for i, (t, s, col) in enumerate(lines):
    body.append(text(TX + 14, TY + 62 + i * 29, s, cls=show(t), size=13, col=col))

# Proxy gate on the right wall.
body.append(f'<rect x="{X1 - 9}" y="200" width="18" height="96" rx="9" fill="{C["green"]}"/>')
body.append(text(X1, 190, "proxy", size=12, col=C["green"], weight=700, family=SANS, anchor="middle"))

# Projects mount, the one shared directory.
body.append(f'<rect x="{X0 + 24}" y="{Y1 - 92}" width="{X1 - X0 - 48}" height="64" rx="12" '
            f'fill="{C["mantle"]}" stroke="{C["green"]}" stroke-width="1.5" stroke-dasharray="6 5"/>')
body.append(text(X0 + 46, Y1 - 54, "~/projects", size=16, col=C["green"]))
body.append(text(X1 - 46, Y1 - 54, "the only directory lea sees · read/write", size=13,
                 col=C["sub"], family=SANS, anchor="end"))

# Packets.
pkt = 'r="7" cx="0" cy="0"'
body.append(f'<circle {pkt} fill="{C["green"]}" class="{move(2.0, 2.8, 880, 180, 940, 187)}"/>')
body.append(f'<circle {pkt} fill="{C["red"]}" class="{move(4.6, 5.2, 590, 220, 300, 187)}"/>')
body.append(f'<circle {pkt} fill="{C["red"]}" class="{move(7.0, 7.6, 590, 300, 300, 407)}"/>')

# Captions.
caps = [(0.0, 2.9, "Claude Code agents, side by side."),
        (2.9, 4.9, "The internet: yes, through lea's proxy."),
        (4.9, 8.8, "Your keys and your network: no."),
        (8.8, 11.4, "You answer every agent from one deck.")]
for t0, t1, s in caps:
    body.append(text(W / 2, 598, s, cls=show(t0 + 0.05, t1 - 0.2), size=26, weight=650,
                     family=SANS, anchor="middle"))

print(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" '
      f'role="img" aria-label="lea: Claude Code agents side by side in an isolated container; '
      f'the internet is reachable through a proxy, your SSH keys and private network are not.">')
print("<title>lea: Claude Code agents, side by side, isolated from your machine</title>")
print("<style>" + "\n".join(css) +
      "\n@media (prefers-reduced-motion: reduce){*{animation-delay:-5.6s!important;animation-play-state:paused!important}}</style>")
print("\n".join(body))
print("</svg>")
