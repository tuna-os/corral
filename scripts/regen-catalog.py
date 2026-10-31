#!/usr/bin/env python3
"""Refresh the generated catalog from public GHCR packages and installer ISOs.

New entries and updated aliases must be backed by a recent upstream version.
Existing entries are retained even if absent or stale: removing them is a
manual, reviewable decision. Any incomplete or invalid upstream response
aborts before writing the catalog.

Run with `python3 scripts/regen-catalog.py`. Requires Python and curl; public
GHCR manifests are fetched with anonymous, repository-scoped pull tokens.
"""
import datetime
import email.utils
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

FRESH_DAYS = 60
ORGS = {"ublue-os", "projectbluefin", "tuna-os"}
OUT = "pkg/catalog/catalog_generated.go"

# Build artifacts / components that are not end-user bootable images.
EXCLUDE_SUBSTR = (
    "akmods", "kernel", "kmods", "wallpaper", "distrobox", "toolbox", "-cli",
    "artwork", "bling", "base-", "-zfs", "nvtest", "boxkit", "brew",
    "devcontainer", "buildroot", "image-template", "krunner", "builder",
    "uupd", "cache", "-pr", "common", "finpilot", "knuckle", "testsuite",
    "packages/", "beyond", "x13s",  # x13s = ARM, won't boot on amd64 VMs
)

# Only curated packages are known to be end-user bootable images.
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

SOURCE = {"ublue-os": "universal-blue.org", "projectbluefin": "projectbluefin.io", "tuna-os": "tunaos.org"}

# Candidate installer ISOs (name, URL, description, source, logo).
ISO_CANDIDATES = [
    ("bluefin-iso", "https://download.projectbluefin.io/bluefin-stable-x86_64.iso", "Bluefin — GNOME bootc desktop (installer ISO)", "projectbluefin.io", "gnome"),
    ("bluefin-gts-iso", "https://download.projectbluefin.io/bluefin-gts-x86_64.iso", "Bluefin GTS — GNOME desktop (installer ISO)", "projectbluefin.io", "gnome"),
    ("aurora-iso", "https://dl.getaurora.dev/aurora-stable-webui-x86_64.iso", "Aurora — KDE Plasma bootc desktop (installer ISO)", "getaurora.dev", "kde"),
    ("aurora-nvidia-open-iso", "https://dl.getaurora.dev/aurora-nvidia-open-stable-webui-x86_64.iso", "Aurora — KDE, NVIDIA open driver (installer ISO)", "getaurora.dev", "kde"),
    ("bazzite-iso", "https://download.bazzite.gg/bazzite-stable-amd64.iso", "Bazzite — gaming desktop (installer ISO)", "bazzite.gg", "steam"),
    ("bazzite-deck-iso", "https://download.bazzite.gg/bazzite-deck-stable-amd64.iso", "Bazzite Deck — handheld (installer ISO)", "bazzite.gg", "steam"),
]

CUTOFF = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=FRESH_DAYS)
STRING = r'"(?:[^"\\]|\\.)*"'
BOOTC_ROW = re.compile(r'\s*\{(' + STRING + r'), (' + STRING + r'), (' + STRING + r'), (' + STRING + r'), (' + STRING + r')\},')
ISO_ROW = re.compile(r'\s*\{Name: (' + STRING + r'), Description: (' + STRING + r'), ISO: (' + STRING + r'), Source: (' + STRING + r'), Logo: (' + STRING + r'), Variant: "desktop"\},')


class RefreshError(Exception):
    pass


def sh(*args):
    try:
        return subprocess.run(args, capture_output=True, text=True, timeout=90)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise RefreshError(f"{args[0]} could not complete") from exc


def parse_dt(value):
    try:
        result = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
        if result.tzinfo is None:
            raise ValueError("missing timezone")
        return result
    except (AttributeError, TypeError, ValueError) as exc:
        raise RefreshError("invalid upstream timestamp") from exc


def bootable(name):
    return not any(x in name for x in EXCLUDE_SUBSTR)


ACCEPT_MANIFEST = ", ".join(("application/vnd.oci.image.index.v1+json",
                             "application/vnd.docker.distribution.manifest.list.v2+json",
                             "application/vnd.oci.image.manifest.v1+json",
                             "application/vnd.docker.distribution.manifest.v2+json"))
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        old, new = urllib.parse.urlsplit(req.full_url), urllib.parse.urlsplit(newurl)
        if new.scheme != "https":
            raise RefreshError("registry redirected to a non-HTTPS URL")
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if redirected and old.netloc != new.netloc:
            redirected.remove_header("Authorization")
        return redirected


OPENER = urllib.request.build_opener(SafeRedirect())


def registry_json(url, token=None, missing_token_ok=False, missing_manifest_ok=False):
    headers = {"Accept": ACCEPT_MANIFEST}
    if token:
        headers["Authorization"] = "Bearer " + token
    try:
        request = urllib.request.Request(url, headers=headers)
        with OPENER.open(request, timeout=30) as response:
            body = response.read(4 * 1024 * 1024 + 1)
            if len(body) > 4 * 1024 * 1024:
                raise RefreshError("registry response too large")
            return json.loads(body), response.headers, body
    except urllib.error.HTTPError as exc:
        # GHCR reports nonexistent/nonpublic repositories as DENIED (403).
        # A manifest 404 is a missing alias. Neither may remove an old entry.
        if missing_token_ok and exc.code == 403:
            try:
                error = json.loads(exc.read(4096))
                if error.get("errors", [{}])[0].get("code") == "DENIED":
                    return None
            except (ValueError, AttributeError, IndexError, TypeError):
                pass
        if missing_manifest_ok and exc.code == 404:
            return None
        raise RefreshError(f"registry HTTP {exc.code}") from exc
    except (OSError, ValueError, UnicodeError) as exc:
        raise RefreshError("registry request or JSON response failed") from exc


def registry_token(repo):
    url = "https://ghcr.io/token?" + urllib.parse.urlencode({"scope": f"repository:{repo}:pull"})
    result = registry_json(url, missing_token_ok=True)
    if result is None:
        return None
    data = result[0]
    if not isinstance(data, dict) or not isinstance(data.get("token"), str) or not data["token"]:
        raise RefreshError("invalid anonymous registry token response")
    return data["token"]


def image_release(repo, tag, token):
    base = "https://ghcr.io/v2/" + repo
    result = registry_json(base + "/manifests/" + tag, token, missing_manifest_ok=True)
    if result is None:
        return None
    manifest, headers, body = result
    digest = headers.get("Docker-Content-Digest", "")
    if not DIGEST.fullmatch(digest) or digest != "sha256:" + hashlib.sha256(body).hexdigest():
        raise RefreshError(f"invalid manifest digest for {repo}:{tag}")
    if not isinstance(manifest, dict):
        raise RefreshError(f"invalid manifest for {repo}:{tag}")
    if "manifests" in manifest:
        entries = manifest["manifests"]
        if not isinstance(entries, list):
            raise RefreshError(f"invalid manifest index for {repo}:{tag}")
        amd64 = [item for item in entries if isinstance(item, dict) and isinstance(item.get("platform"), dict)
                 and item["platform"].get("os") == "linux" and item["platform"].get("architecture") == "amd64"]
        if not amd64:
            return None
        child = amd64[0].get("digest", "")
        if not DIGEST.fullmatch(child):
            raise RefreshError(f"invalid child manifest for {repo}:{tag}")
        manifest, _, body = registry_json(base + "/manifests/" + child, token)
        if "sha256:" + hashlib.sha256(body).hexdigest() != child or not isinstance(manifest, dict):
            raise RefreshError(f"invalid child manifest for {repo}:{tag}")
    config = manifest.get("config")
    if not isinstance(config, dict) or not DIGEST.fullmatch(config.get("digest", "")):
        # Existing catalogs can contain artifacts rather than bootable images.
        return None
    config_digest = config["digest"]
    data, _, body = registry_json(base + "/blobs/" + config_digest, token)
    if "sha256:" + hashlib.sha256(body).hexdigest() != config_digest or not isinstance(data, dict):
        raise RefreshError(f"invalid image config for {repo}:{tag}")
    if data.get("os") != "linux" or data.get("architecture") != "amd64":
        return None
    return digest, parse_dt(data.get("created"))


def existing_catalog():
    try:
        with open(OUT, encoding="utf-8") as source:
            text = source.read()
        sections = re.findall(r'var (generatedBootcImages|generatedISOs) = \[\](?:BootcImage|Image)\{\n(.*?)\n\}', text, re.S)
        if len(sections) != 2 or [s[0] for s in sections] != ["generatedBootcImages", "generatedISOs"]:
            raise ValueError("missing catalog sections")
        result = []
        for (_, body), pattern in zip(sections, (BOOTC_ROW, ISO_ROW)):
            rows = {}
            timestamp = ""
            for line in body.splitlines():
                comment = re.fullmatch(r'\s*// upstream: (\S+)', line)
                if comment:
                    if timestamp:
                        raise ValueError("duplicate upstream annotation")
                    timestamp = comment.group(1)
                    if not DIGEST.fullmatch(timestamp):
                        parse_dt(timestamp)
                    continue
                match = pattern.fullmatch(line)
                if not match:
                    raise ValueError("unrecognized catalog row")
                row = tuple(json.loads(value) for value in match.groups()) + (timestamp,)
                timestamp = ""
                if row[0] in rows:
                    raise ValueError("duplicate catalog name")
                rows[row[0]] = row
            if timestamp:
                raise ValueError("orphaned upstream annotation")
            result.append(rows)
        return result
    except (OSError, ValueError, TypeError) as exc:
        raise RefreshError("cannot safely parse existing generated catalog") from exc


def fetch_images(existing):
    rows = dict(existing)
    targets = {}
    for name, row in existing.items():
        match = re.fullmatch(r'ghcr\.io/([a-z0-9]+(?:[._/-][a-z0-9]+)*):([A-Za-z0-9_][A-Za-z0-9_.-]{0,127})', row[2])
        if not match or match.group(1).split('/')[0] not in ORGS:
            raise RefreshError(f"unsupported existing image reference: {name}")
        repo, tag = match.groups()
        if repo in targets:
            raise RefreshError(f"duplicate package reference: {repo}")
        targets[repo] = (name, tag)
    for repo, meta in META.items():
        if repo not in targets and bootable(repo):
            targets[repo] = (None, meta[3])

    accessible = 0
    for repo, (old_name, preferred) in sorted(targets.items()):
        token = registry_token(repo)
        if token is None:
            print(f"  unavailable/nonpublic repository: {repo}; retaining existing entry", file=sys.stderr)
            continue
        accessible += 1
        # Never change an existing architecture/flavour alias to a different tag.
        candidates = [preferred] if old_name else list(dict.fromkeys((preferred, "stable", "latest")))
        found = None
        for tag in candidates:
            release = image_release(repo, tag, token)
            if release is not None and release[1] >= CUTOFF:
                found = (tag, release[0])
                break
        if found is None:
            print(f"  no recent amd64 release for {repo}; retaining existing entry", file=sys.stderr)
            continue
        tag, digest = found
        ref = f"ghcr.io/{repo}:{tag}"
        if old_name:
            old = existing[old_name]
            rows[old_name] = (old[0], old[1], ref, old[3], old[4], digest)
        else:
            org, name = repo.split('/', 1)
            short = name.split('/')[-1] if org == "ublue-os" else f"{org.split('-')[0]}-{name.split('/')[-1]}"
            if short in rows:
                raise RefreshError(f"catalog name collision: {short}")
            meta = META[repo]
            rows[short] = (short, meta[0], ref, SOURCE[org], meta[1], digest)
    if not accessible:
        raise RefreshError("no public GHCR repositories were accessible")
    return rows


def iso_last_modified(url):
    result = sh("curl", "--silent", "--show-error", "--fail", "--location", "--proto", "=https", "--proto-redir", "=https", "--max-time", "30", "--head", url)
    if result.returncode:
        raise RefreshError(f"ISO request failed: {url}")
    # Only the final response's headers count after redirects.
    blocks = re.split(r'\r?\n\r?\n', result.stdout.strip())
    lines = blocks[-1].splitlines()
    if not lines or not re.match(r'^HTTP/\S+ 200(?:\s|$)', lines[0]):
        raise RefreshError(f"unexpected ISO response: {url}")
    content_types = [line.split(':', 1)[1].strip().lower().split(';', 1)[0] for line in lines[1:] if line.lower().startswith('content-type:')]
    if len(content_types) != 1 or content_types[0] not in {"application/vnd.efi.iso", "application/octet-stream", "application/x-iso9660-image"}:
        raise RefreshError(f"unexpected ISO content type: {url}")
    values = [line.split(':', 1)[1].strip() for line in lines[1:] if line.lower().startswith('last-modified:')]
    try:
        if len(values) != 1:
            raise ValueError("missing or duplicate Last-Modified")
        modified = email.utils.parsedate_to_datetime(values[0])
        if modified is None or modified.tzinfo is None:
            raise ValueError("invalid Last-Modified")
        return modified
    except (ValueError, TypeError, OverflowError) as exc:
        raise RefreshError(f"invalid ISO Last-Modified: {url}") from exc


def fetch_isos(existing):
    rows = dict(existing)
    for name, url, desc, src, logo in ISO_CANDIDATES:
        modified = iso_last_modified(url)
        if modified >= CUTOFF:
            if name in rows and rows[name][2] != url:
                raise RefreshError(f"ISO catalog name collision: {name}")
            rows[name] = (name, desc, url, src, logo, modified.isoformat())
    return rows


def go_str(value):
    return json.dumps(value, ensure_ascii=False)


def main():
    images, isos = existing_catalog()
    print("querying curated and existing public GHCR references", file=sys.stderr)
    images = fetch_images(images)
    print("checking installer ISO sources", file=sys.stderr)
    isos = fetch_isos(isos)
    lines = ["// Code generated by scripts/regen-catalog.py; DO NOT EDIT.",
             "// New entries and updated aliases are freshness-checked; existing entries are retained.",
             "// Regenerate with `python3 scripts/regen-catalog.py`.", "", "package catalog", "",
             "var generatedBootcImages = []BootcImage{"]
    for n, d, img, src, logo, updated in sorted(images.values()):
        if updated:
            lines.append(f"\t// upstream: {updated}")
        lines.append(f"\t{{{go_str(n)}, {go_str(d)}, {go_str(img)}, {go_str(src)}, {go_str(logo)}}},")
    lines.extend(["}", "", "var generatedISOs = []Image{"])
    for n, d, url, src, logo, updated in sorted(isos.values()):
        if updated:
            lines.append(f"\t// upstream: {updated}")
        lines.append(f"\t{{Name: {go_str(n)}, Description: {go_str(d)}, ISO: {go_str(url)}, "
                     f"Source: {go_str(src)}, Logo: {go_str(logo)}, Variant: \"desktop\"}},")
    lines.extend(["}", ""])
    # Replace atomically only after every source and every row has succeeded.
    directory = os.path.dirname(OUT) or "."
    tmp = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=directory, delete=False) as target:
            tmp = target.name
            target.write("\n".join(lines))
        os.chmod(tmp, 0o644)
        os.replace(tmp, OUT)
    finally:
        if tmp and os.path.exists(tmp):
            os.unlink(tmp)
    print(f"wrote {OUT}: {len(images)} images, {len(isos)} ISOs", file=sys.stderr)


if __name__ == "__main__":
    try:
        main()
    except RefreshError as exc:
        print(f"catalog refresh failed: {exc}", file=sys.stderr)
        sys.exit(1)
