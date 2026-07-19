#!/usr/bin/env python3
"""Regenerate the dynamic bootc catalog (Universal Blue / Project Bluefin /
TunaOS images + their installer ISOs) from the live ghcr.io registries.

Only images with a tag built within FRESH_DAYS are kept; stale variants are
dropped. ISO URLs are HEAD-checked and dropped if their Last-Modified is older
than FRESH_DAYS. Output is written to pkg/catalog/catalog_generated.go.

Run via `just regen-catalog`. Needs `gh` (authenticated) and `curl`.
"""
import datetime
import json
import subprocess
import sys

FRESH_DAYS = 60
ORGS = ["ublue-os", "projectbluefin", "tuna-os"]
OUT = "pkg/catalog/catalog_generated.go"

# Build artifacts / components that are not end-user bootable images.
EXCLUDE_SUBSTR = (
    "akmods", "kernel", "kmods", "wallpaper", "distrobox", "toolbox", "-cli",
    "artwork", "bling", "base-", "-zfs", "nvtest", "boxkit", "brew",
    "devcontainer", "buildroot", "image-template", "krunner", "builder",
    "uupd", "cache", "-pr", "common", "finpilot", "knuckle", "testsuite",
    "packages/", "beyond", "x13s",  # x13s = ARM, won't boot on amd64 VMs
)

# Curation: per "org/name" → (description, logo, variant, tag). Families fall
# back to a templated description + logo when an entry isn't listed here, so new
# fresh images still get reasonable cards until curated.
META = {
    "ublue-os/bluefin": ("Universal Blue Bluefin — GNOME bootc desktop", "gnome", "gnome", "stable"),
    "ublue-os/bluefin-dx": ("Bluefin DX — GNOME desktop + developer tooling", "gnome", "gnome", "stable"),
    "ublue-os/bluefin-gdx": ("Bluefin GDX — GNOME dev desktop (GPU/AI tooling)", "gnome", "gnome", "stable"),
    "ublue-os/aurora": ("Universal Blue Aurora — KDE Plasma bootc desktop", "kde", "kde", "stable"),
    "ublue-os/aurora-dx": ("Aurora DX — KDE Plasma + developer tooling", "kde", "kde", "stable"),
    "ublue-os/bazzite": ("Bazzite — gaming bootc desktop (KDE)", "steam", "steam", "stable"),
    "ublue-os/bazzite-gnome": ("Bazzite — gaming bootc desktop (GNOME)", "steam", "steam", "stable"),
    "ublue-os/bazzite-deck": ("Bazzite Deck — handheld/Steam Deck image", "steam", "steam", "stable"),
    "ublue-os/kinoite-main": ("Universal Blue Kinoite — KDE Fedora Atomic base", "kde", "kde", "latest"),
    "ublue-os/silverblue-main": ("Universal Blue Silverblue — GNOME Fedora Atomic base", "gnome", "gnome", "latest"),
    "ublue-os/ucore": ("uCore — Fedora CoreOS server, batteries included", "fedora", "server", "stable"),
    "ublue-os/ucore-minimal": ("uCore minimal — lean Fedora CoreOS server", "fedora", "server", "stable"),
    "ublue-os/ucore-hci": ("uCore HCI — hyper-converged (libvirt/KVM) server", "fedora", "server", "stable"),
    "projectbluefin/dakota": ("Bluefin Dakota — next-gen Bluefin", "gnome", "gnome", "latest"),
    "projectbluefin/bluefin-lts": ("Bluefin LTS — CentOS-based long-term Bluefin", "gnome", "gnome", "latest"),
}

# Per-family fallbacks (matched by name prefix) for non-curated fresh images.
FAMILY = [
    ("bazzite", "steam", "steam", "stable", "Bazzite — gaming bootc image"),
    ("bluefin", "gnome", "gnome", "stable", "Bluefin — GNOME bootc desktop"),
    ("aurora", "kde", "kde", "stable", "Aurora — KDE Plasma bootc desktop"),
    ("dakota", "gnome", "gnome", "latest", "Bluefin Dakota image"),
    ("ucore", "fedora", "server", "stable", "Universal Blue uCore server"),
    ("steambox", "steam", "steam", "latest", "SteamBox — Steam Big Picture image"),
    ("kinoite", "kde", "kde", "latest", "Kinoite — KDE Fedora Atomic"),
    ("silverblue", "gnome", "gnome", "latest", "Silverblue — GNOME Fedora Atomic"),
]
SOURCE = {"ublue-os": "universal-blue.org", "projectbluefin": "projectbluefin.io", "tuna-os": "tunaos.org"}

# Candidate installer ISOs (name, url, logo, variant). Kept only if fresh.
ISO_CANDIDATES = [
    ("bluefin-iso", "https://download.projectbluefin.io/bluefin-stable-x86_64.iso", "Bluefin — GNOME bootc desktop (installer ISO)", "projectbluefin.io", "gnome"),
    ("bluefin-gts-iso", "https://download.projectbluefin.io/bluefin-gts-x86_64.iso", "Bluefin GTS — GNOME desktop (installer ISO)", "projectbluefin.io", "gnome"),
    ("aurora-iso", "https://dl.getaurora.dev/aurora-stable-webui-x86_64.iso", "Aurora — KDE Plasma bootc desktop (installer ISO)", "getaurora.dev", "kde"),
    ("aurora-nvidia-open-iso", "https://dl.getaurora.dev/aurora-nvidia-open-stable-webui-x86_64.iso", "Aurora — KDE, NVIDIA open driver (installer ISO)", "getaurora.dev", "kde"),
    ("bazzite-iso", "https://download.bazzite.gg/bazzite-stable-amd64.iso", "Bazzite — gaming desktop (installer ISO)", "bazzite.gg", "steam"),
    ("bazzite-deck-iso", "https://download.bazzite.gg/bazzite-deck-stable-amd64.iso", "Bazzite Deck — handheld (installer ISO)", "bazzite.gg", "steam"),
]

CUTOFF = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=FRESH_DAYS)


def sh(*args):
    return subprocess.run(args, capture_output=True, text=True)


def parse_dt(s):
    try:
        return datetime.datetime.fromisoformat(s.replace("Z", "+00:00"))
    except Exception:
        return None


def bootable(name):
    return not any(x in name for x in EXCLUDE_SUBSTR)


def meta_for(org, name):
    key = f"{org}/{name}"
    if key in META:
        return META[key]
    for pre, logo, variant, tag, blurb in FAMILY:
        if name.startswith(pre):
            return (blurb, logo, variant, tag)
    # Generic (e.g. TunaOS fish-named images).
    title = name.replace("/", " ").replace("-", " ").title()
    return (f"{org} {title} — bootc image", "linux", "server", "latest")


def resolve_tag(org, name, preferred_tag):
    """Resolve the best available tag for an image.

    Tries the preferred tag first, then falls back through stable > latest > the
    most recent version-ish tag. Returns None if no usable tag is found.
    """
    # Fetch actual tags for this image via the GH Container Registry API.
    # Use --paginate because `bluefin` has 200+ versions and the usable tag
    # (e.g. `stable`) may land on page 2+.
    r = sh("gh", "api", "--paginate", f"/orgs/{org}/packages/container/{name}/versions?per_page=100")
    if r.returncode != 0:
        print(f"  warn: gh api {org}/{name}: {r.stderr.strip()[:160]}", file=sys.stderr)
        return preferred_tag  # fall back to the curated tag

    try:
        versions = json.loads(r.stdout)
        if isinstance(versions, list) and versions and isinstance(versions[0], list):
            # --paginate returns a list-of-lists; flatten
            versions = [v for page in versions for v in page]
    except Exception:
        return preferred_tag

    all_tags = set()
    for v in versions:
        for t in v.get("metadata", {}).get("container", {}).get("tags", []):
            if not t.startswith("sha256-") and not t.endswith(".sig"):
                all_tags.add(t)

    if not all_tags:
        return None  # no usable tags at all

    # Priority order: preferred > stable > latest > stable-daily > a version tag
    candidates = [
        preferred_tag,
        "stable",
        "latest",
        "stable-daily",
    ]
    for tag in candidates:
        if tag in all_tags:
            return tag

    # Fall back to a non-testing, non-staging, non-sha256 tag that looks like a version.
    # SHA256-only tags are unmanageable (no stable alias), so skip those images.
    for t in sorted(all_tags, reverse=True):
        if any(x in t for x in ("testing", "staging")):
            continue
        if len(t) == 64 and all(c in "0123456789abcdef" for c in t):
            continue  # bare sha256 hash, not a usable alias
        return t

    return None


def fetch_images():
    rows = []
    for org in ORGS:
        r = sh("gh", "api", "--paginate", f"/orgs/{org}/packages?package_type=container")
        if r.returncode != 0:
            print(f"warn: gh api {org}: {r.stderr.strip()[:160]}", file=sys.stderr)
            continue
        for p in json.loads(r.stdout):
            name = p["name"]
            if not bootable(name):
                continue
            dt = parse_dt(p.get("updated_at", ""))
            if not dt or dt < CUTOFF:
                continue
            desc, logo, variant, tag = meta_for(org, name)
            resolved_tag = resolve_tag(org, name, tag)
            if resolved_tag is None:
                print(f"  skip {org}/{name}: no usable tag found (preferred={tag})", file=sys.stderr)
                continue
            if resolved_tag != tag:
                print(f"  tag {org}/{name}: {tag} -> {resolved_tag}", file=sys.stderr)
            short = name.split("/")[-1] if org == "ublue-os" else f"{org.split('-')[0]}-{name.split('/')[-1]}"
            rows.append((short, desc, f"ghcr.io/{org}/{name}:{resolved_tag}", SOURCE[org], logo))
    # Dedup by catalog name, stable order.
    seen, out = set(), []
    for row in sorted(rows):
        if row[0] in seen:
            continue
        seen.add(row[0])
        out.append(row)
    return out


def iso_last_modified(url):
    r = sh("curl", "-s", "--max-time", "15", "-I", url)
    if r.returncode != 0:
        return None
    for line in r.stdout.splitlines():
        if line.lower().startswith("last-modified:"):
            try:
                return datetime.datetime.strptime(line.split(":", 1)[1].strip(),
                                                  "%a, %d %b %Y %H:%M:%S %Z").replace(tzinfo=datetime.timezone.utc)
            except Exception:
                return None
    return None


def fetch_isos():
    out = []
    for name, url, desc, src, logo in ISO_CANDIDATES:
        lm = iso_last_modified(url)
        if lm and lm >= CUTOFF:
            out.append((name, desc, url, src, logo))
            print(f"  keep ISO {name} ({lm.date()})", file=sys.stderr)
        else:
            print(f"  drop ISO {name} ({'stale '+str(lm.date()) if lm else 'unreachable'})", file=sys.stderr)
    return out


def go_str(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def main():
    print(f"querying {len(ORGS)} orgs (fresh <= {FRESH_DAYS}d)…", file=sys.stderr)
    images = fetch_images()
    print(f"  {len(images)} fresh bootc images", file=sys.stderr)
    print("checking ISO datestamps…", file=sys.stderr)
    isos = fetch_isos()

    L = []
    L.append("// Code generated by scripts/regen-catalog.py; DO NOT EDIT.")
    L.append(f"// Universal Blue / Project Bluefin / TunaOS images, freshness-filtered to")
    L.append(f"// tags built within {FRESH_DAYS} days. Regenerate with `just regen-catalog`.")
    L.append("")
    L.append("package catalog")
    L.append("")
    L.append("var generatedBootcImages = []BootcImage{")
    for n, d, img, src, logo in images:
        L.append(f"\t{{{go_str(n)}, {go_str(d)}, {go_str(img)}, {go_str(src)}, {go_str(logo)}}},")
    L.append("}")
    L.append("")
    L.append("var generatedISOs = []Image{")
    for n, d, url, src, logo in isos:
        L.append(f"\t{{Name: {go_str(n)}, Description: {go_str(d)}, ISO: {go_str(url)}, "
                 f"Source: {go_str(src)}, Logo: {go_str(logo)}, Variant: \"desktop\"}},")
    L.append("}")
    L.append("")
    with open(OUT, "w") as f:
        f.write("\n".join(L))
    print(f"wrote {OUT}: {len(images)} images, {len(isos)} ISOs", file=sys.stderr)


if __name__ == "__main__":
    main()
