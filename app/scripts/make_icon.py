#!/usr/bin/env python3
"""Generates the launcher icons (Android + iOS) from one geometry.

The mark matches lib/widgets/brand_mark.dart: an ink tile, an open white ring
and a green dot in the ring's gap. Rendered at 4x and downsampled for clean
edges. Run from app/:  python3 scripts/make_icon.py
"""
import json
import math
import os

from PIL import Image, ImageDraw

INK = (21, 23, 28, 255)
WHITE = (255, 255, 255, 255)
SIGNAL = (18, 161, 80, 255)
SS = 4  # supersampling

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def draw_mark(px, *, tile=True, radius_frac=0.27, scale=1.0):
    """Returns an RGBA image px*px. scale shrinks the ring+dot (for adaptive foreground)."""
    s = px * SS
    img = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    if tile:
        if radius_frac <= 0:
            d.rectangle([0, 0, s, s], fill=INK)
        else:
            d.rounded_rectangle([0, 0, s - 1, s - 1], radius=int(s * radius_frac), fill=INK)
    c = s / 2
    r = s * 0.25 * scale
    w = s * 0.095 * scale
    gap_center = -45.0  # degrees, PIL: 0 = 3 o'clock, clockwise positive (y down)
    gap = 0.42 * 180
    start = gap_center + gap / 2
    end = gap_center - gap / 2 + 360
    # Thick arc: draw with width, then round caps as circles.
    box = [c - r - w / 2, c - r - w / 2, c + r + w / 2, c + r + w / 2]
    d.arc(box, start=start, end=end, fill=WHITE, width=int(w))
    for ang in (start, end):
        a = math.radians(ang)
        cx, cy = c + r * math.cos(a), c + r * math.sin(a)
        d.ellipse([cx - w / 2, cy - w / 2, cx + w / 2, cy + w / 2], fill=WHITE)
    a = math.radians(gap_center)
    dx, dy = c + r * math.cos(a), c + r * math.sin(a)
    dr = s * 0.075 * scale
    d.ellipse([dx - dr, dy - dr, dx + dr, dy + dr], fill=SIGNAL)
    return img.resize((px, px), Image.LANCZOS)


def save(img, path):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    img.save(path)
    print("wrote", os.path.relpath(path, ROOT))


def android():
    res = os.path.join(ROOT, "android/app/src/main/res")
    legacy = {"mdpi": 48, "hdpi": 72, "xhdpi": 96, "xxhdpi": 144, "xxxhdpi": 192}
    for dens, px in legacy.items():
        save(draw_mark(px, radius_frac=0.22), f"{res}/mipmap-{dens}/ic_launcher.png")
        # Round variant: circular ink tile.
        rnd = draw_mark(px, tile=False)
        base = Image.new("RGBA", (px * SS, px * SS), (0, 0, 0, 0))
        ImageDraw.Draw(base).ellipse([0, 0, px * SS - 1, px * SS - 1], fill=INK)
        base = base.resize((px, px), Image.LANCZOS)
        base.alpha_composite(rnd)
        save(base, f"{res}/mipmap-{dens}/ic_launcher_round.png")
    # Adaptive icon (Android 8+): 108dp canvas, safe zone 66dp → shrink the mark.
    adaptive = {"mdpi": 108, "hdpi": 162, "xhdpi": 216, "xxhdpi": 324, "xxxhdpi": 432}
    for dens, px in adaptive.items():
        save(draw_mark(px, tile=False, scale=0.74), f"{res}/mipmap-{dens}/ic_launcher_foreground.png")
    os.makedirs(f"{res}/mipmap-anydpi-v26", exist_ok=True)
    xml = """<?xml version="1.0" encoding="utf-8"?>
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@color/ic_launcher_background"/>
    <foreground android:drawable="@mipmap/ic_launcher_foreground"/>
    <monochrome android:drawable="@mipmap/ic_launcher_foreground"/>
</adaptive-icon>
"""
    for name in ("ic_launcher.xml", "ic_launcher_round.xml"):
        with open(f"{res}/mipmap-anydpi-v26/{name}", "w") as f:
            f.write(xml)
    colors = f"{res}/values/ic_launcher_background.xml"
    with open(colors, "w") as f:
        f.write('<?xml version="1.0" encoding="utf-8"?>\n<resources>\n'
                '    <color name="ic_launcher_background">#15171C</color>\n</resources>\n')
    print("wrote adaptive xml + background color")


def ios():
    d = os.path.join(ROOT, "ios/Runner/Assets.xcassets/AppIcon.appiconset")
    with open(os.path.join(d, "Contents.json")) as f:
        contents = json.load(f)
    done = set()
    for im in contents["images"]:
        fn = im.get("filename")
        if not fn or fn in done:
            continue
        pt = float(im["size"].split("x")[0])
        px = int(round(pt * int(im["scale"].rstrip("x"))))
        # iOS masks the corners itself and forbids alpha: full-bleed square.
        img = draw_mark(px, radius_frac=0).convert("RGB")
        save(img, os.path.join(d, fn))
        done.add(fn)


SPLASH_BG = "#0D0F13"  # keep in sync with AppColors.night / launch screens


def splash():
    """Native splash: a plain dark screen. The Flutter splash draws the mark
    onto it with an animation, so nothing jumps between the two."""
    res = os.path.join(ROOT, "android/app/src/main/res")
    # Android 12+ splash icon: 288dp canvas, content must fit a 192dp circle.
    # The ring + dot span ~0.65 of the canvas, inside the 2/3 limit.
    blank = Image.new("RGBA", (288, 288), (0, 0, 0, 0))
    save(blank, f"{res}/drawable-nodpi/splash_mark.png")
    with open(f"{res}/values/colors_splash.xml", "w") as f:
        f.write('<?xml version="1.0" encoding="utf-8"?>\n<resources>\n'
                f'    <color name="splash_bg">{SPLASH_BG}</color>\n</resources>\n')
    layer = """<?xml version="1.0" encoding="utf-8"?>
<layer-list xmlns:android="http://schemas.android.com/apk/res/android">
    <item android:drawable="@color/splash_bg" />
</layer-list>
"""
    for d in ("drawable", "drawable-v21"):
        with open(f"{res}/{d}/launch_background.xml", "w") as f:
            f.write(layer)
    os.makedirs(f"{res}/values-v31", exist_ok=True)
    v31 = """<?xml version="1.0" encoding="utf-8"?>
<resources>
    <!-- Android 12+ system splash: dark background + bare ring mark, matching the Flutter splash. -->
    <style name="LaunchTheme" parent="@android:style/Theme.Black.NoTitleBar">
        <item name="android:windowBackground">@drawable/launch_background</item>
        <item name="android:windowSplashScreenBackground">@color/splash_bg</item>
        <item name="android:windowSplashScreenAnimatedIcon">@drawable/splash_mark</item>
        <item name="android:windowSplashScreenIconBackgroundColor">@color/splash_bg</item>
    </style>
    <style name="NormalTheme" parent="@android:style/Theme.Black.NoTitleBar">
        <item name="android:windowBackground">@color/splash_bg</item>
    </style>
</resources>
"""
    with open(f"{res}/values-v31/styles.xml", "w") as f:
        f.write(v31)
    # iOS LaunchImage: 160pt, centred by LaunchScreen.storyboard.
    d = os.path.join(ROOT, "ios/Runner/Assets.xcassets/LaunchImage.imageset")
    for name, px in (("LaunchImage.png", 160), ("LaunchImage@2x.png", 320), ("LaunchImage@3x.png", 480)):
        save(Image.new("RGBA", (px, px), (0, 0, 0, 0)), os.path.join(d, name))


def windows():
    """Windows exe / taskbar icon: rounded tile, all standard sizes in one .ico."""
    sizes = [16, 20, 24, 32, 40, 48, 64, 128, 256]
    img = draw_mark(256, radius_frac=0.22)
    path = os.path.join(ROOT, "windows/runner/resources/app_icon.ico")
    img.save(path, format="ICO", sizes=[(s, s) for s in sizes])
    print("wrote", os.path.relpath(path, ROOT))


def macos():
    """macOS app icon: Apple's grid puts the rounded tile at 824/1024 of the
    canvas (transparent margin), corner radius ~22.5 % of the tile."""
    d = os.path.join(ROOT, "macos/Runner/Assets.xcassets/AppIcon.appiconset")
    for px in (16, 32, 64, 128, 256, 512, 1024):
        tile = max(1, round(px * 824 / 1024))
        canvas = Image.new("RGBA", (px, px), (0, 0, 0, 0))
        mark = draw_mark(tile, radius_frac=0.225)
        off = (px - tile) // 2
        canvas.alpha_composite(mark, (off, off))
        save(canvas, os.path.join(d, f"app_icon_{px}.png"))


if __name__ == "__main__":
    import sys
    steps = {"android": android, "ios": ios, "splash": splash, "windows": windows, "macos": macos}
    # python3 scripts/make_icon.py [android|ios|splash|windows ...]; no args = all
    for name in sys.argv[1:] or steps:
        steps[name]()
