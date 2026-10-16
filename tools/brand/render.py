#!/usr/bin/env python3
"""Render assets/demo.svg to assets/demo.gif and assets/social-card.png, for places that do not
play SVG (X, LinkedIn, Slack, Open Graph). Needs Google Chrome (or Chromium) and Pillow.

    python3 tools/brand/render.py [--fps 10] [--chrome google-chrome]

Each frame is a headless-Chrome screenshot of the SVG with every animation seeked to t.
"""
import argparse
import pathlib
import shutil
import subprocess
import tempfile

from PIL import Image

ROOT = pathlib.Path(__file__).resolve().parents[2]
LOOP = 12.0

PAGE = """<!doctype html><html><body style="margin:0;background:#11111b">{svg}
<script>
const t = parseFloat(location.hash.slice(1)) * 1000;
for (const a of document.getAnimations()) {{ a.pause(); a.currentTime = t; }}
</script></body></html>"""


def shoot(chrome, page, out, t, w, h):
    subprocess.run([chrome, "--headless=new", "--disable-gpu", "--hide-scrollbars",
                    f"--window-size={w},{h}", f"--screenshot={out}", f"file://{page}#{t:.3f}"],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fps", type=int, default=10)
    ap.add_argument("--chrome", default=shutil.which("google-chrome") or shutil.which("chromium"))
    ap.add_argument("--frame", type=float, help="only render one frame at this time, to stdout path")
    args = ap.parse_args()
    svg = (ROOT / "assets/demo.svg").read_text()
    with tempfile.TemporaryDirectory() as tmp:
        tmp = pathlib.Path(tmp)
        page = tmp / "demo.html"
        page.write_text(PAGE.format(svg=svg))
        if args.frame is not None:
            out = ROOT / "build" / f"frame-{args.frame:.1f}.png"
            out.parent.mkdir(exist_ok=True)
            shoot(args.chrome, page, out, args.frame, 1200, 640)
            print(out)
            return
        frames = []
        for i in range(int(LOOP * args.fps)):
            out = tmp / f"f{i:04d}.png"
            shoot(args.chrome, page, out, i / args.fps, 1200, 640)
            frames.append(Image.open(out).convert("RGB").resize((900, 480), Image.LANCZOS))
        pal = [f.quantize(colors=64, method=Image.Quantize.MEDIANCUT) for f in frames]
        pal[0].save(ROOT / "assets/demo.gif", save_all=True, append_images=pal[1:],
                    duration=int(1000 / args.fps), loop=0, optimize=True)
        # Social card (1200x630): the frame where the story is fullest.
        shoot(args.chrome, page, tmp / "card.png", 8.35, 1200, 640)
        Image.open(tmp / "card.png").convert("RGB").crop((0, 5, 1200, 635)).save(
            ROOT / "assets/social-card.png", optimize=True)


if __name__ == "__main__":
    main()
