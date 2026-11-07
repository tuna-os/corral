#!/usr/bin/env python3
"""macOS console driver: screenshot + OCR + VNC input with verify loops.

Automates KubeVirt macOS guest consoles without an agent watching every
step. Every action verifies itself: clicks land where OCR says the target
is, typing is read back with OCR, waits poll until text appears/vanishes.

Primitives (all verified against a real Sequoia guest):
  VNC pointer motion works; button *release* is unreliable, so buttons are
  pressed with a hold and verified by UI change, never by click alone.
  VNC keysyms reach full macOS (QMP PS/2 does not). Uppercase/symbols go
  through explicit Shift combos (@ = Shift+2). META (Command) does NOT
  arrive via VNC keysyms; use menu paths or `open -a` over SSH instead.
  Full Keyboard Access (AppleKeyboardUIMode=2) must be on for Tab/Space:
    ssh guest 'defaults write -g AppleKeyboardUIMode -int 2' + relogin.
  Do NOT send CTRL+F7 to enable it: on guests without "F-keys as
  standard", VNC F7 arrives as a media key and the toggle never fires.
  META (Command) combos do not arrive via VNC keysyms at all.

Usage:
  driver.py shot OUT.png
  driver.py ocr [OUT.png]                 # print OCR text of latest shot
  driver.py wait TEXT [TIMEOUT]           # exit 0 when TEXT visible
  driver.py wait-vanish TEXT [TIMEOUT]
  driver.py click TEXT [HOLD]             # OCR TEXT -> press-hold center
  driver.py press X Y [HOLD]
  driver.py type TEXT                     # type chars (Shift combos)
  driver.py key SPEC                      # TAB/RET/ESC/SPC/LEFT/.../CTRL+F7
  driver.py ring [OUT.png]                # print focus-ring pixel count
  driver.py activate TEXT [TIMEOUT]       # Tab-cycle until ring on TEXT,
                                          # then Space; verifies the pane
                                          # changed afterwards.

Environment: KCTX=karnataka-k3s KNS=corral-vms KVM=<vm>.
"""
import json
import os
import socket
import struct
import subprocess
import sys
import tempfile
import time

CTX = os.environ.get("KCTX", "karnataka-k3s")
NS = os.environ.get("KNS", "corral-vms")
VM = os.environ.get("KVM", "macos-beeper-sequoia")

KEYS = {
    "TAB": 0xFF09, "RET": 0xFF0D, "ESC": 0xFF1B, "SPC": 0x20,
    "LEFT": 0xFF51, "UP": 0xFF52, "RIGHT": 0xFF53, "DOWN": 0xFF54,
    "BKSP": 0xFF08, "F2": 0xFFBF, "F5": 0xFFC2, "F7": 0xFFC4,
    "F11": 0xFFC8, "CTRL": 0xFFE3, "SHIFT": 0xFFE1, "META": 0xFFE7,
    "PGUP": 0xFF55, "PGDN": 0xFF56, "HOME": 0xFF50, "END": 0xFF57,
}

# VNC keysym is Unicode for printable ASCII; shifted symbols need combos.
SHIFTED = {
    "@": "2", "!": "1", "#": "3", "$": "4", "%": "5", "^": "6",
    "&": "7", "*": "8", "(": "9", ")": "0", "_": "minus", "+": "equal",
    "{": "bracketleft", "}": "bracketright", "|": "backslash",
    ":": "semicolon", '"': "apostrophe", "<": "comma", ">": "period",
    "?": "slash", "~": "grave", "minus": 0x2D, "equal": 0x3D,
    "bracketleft": 0x5B, "bracketright": 0x5D, "backslash": 0x5C,
    "semicolon": 0x3B, "apostrophe": 0x27, "comma": 0x2C,
    "period": 0x2E, "slash": 0x2F, "grave": 0x60,
}


def _vnc_connect():
    portfile = tempfile.mktemp(prefix="vncport-", dir="/tmp")
    proxy = subprocess.Popen(
        ["virtctl", "--context", CTX, "-n", NS, "vnc", VM,
         "--proxy-only", "--address=127.0.0.1", "--port=0"],
        stdout=open(portfile, "w"), stderr=subprocess.DEVNULL)
    port = None
    for _ in range(100):
        time.sleep(0.2)
        try:
            port = json.loads(open(portfile).readline().strip())["port"]
            break
        except Exception:
            continue
    os.unlink(portfile)
    if port is None:
        raise RuntimeError("vnc proxy did not report a port")
    conn = socket.create_connection(("127.0.0.1", port), timeout=10)
    conn.settimeout(10)
    return conn, proxy


def _rx(conn, n):
    body = b""
    while len(body) < n:
        chunk = conn.recv(n - len(body))
        if not chunk:
            raise RuntimeError("vnc closed")
        body += chunk
    return body


def _handshake(conn):
    _rx(conn, 12)
    conn.sendall(b"RFB 003.008\n")
    n = _rx(conn, 1)[0]
    kinds = _rx(conn, n)
    assert 1 in kinds
    conn.sendall(b"\x01")
    assert struct.unpack(">I", _rx(conn, 4))[0] == 0
    conn.sendall(b"\x01")
    header = _rx(conn, 24)
    name_len = struct.unpack(">I", header[20:24])[0]
    _rx(conn, min(name_len, 4096))
    conn.sendall(struct.pack(">BBHi", 2, 0, 1, 0))


def _key(conn, sym, down):
    conn.sendall(struct.pack(">BBHi", 4, down, 0, sym))
    time.sleep(0.05)


def _resolve(part):
    if part in KEYS:
        return KEYS[part]
    if len(part) == 1:
        return ord(part)
    raise KeyError(part)


def send_keys(specs):
    """Send one or more key specs; each is NAME or A+B combo."""
    conn, proxy = _vnc_connect()
    try:
        _handshake(conn)
        for spec in specs:
            parts = [_resolve(p) for p in spec.split("+")]
            if len(parts) == 1:
                _key(conn, parts[0], 1)
                time.sleep(0.1)
                _key(conn, parts[0], 0)
            else:
                for sym in parts:
                    _key(conn, sym, 1)
                for sym in reversed(parts):
                    _key(conn, sym, 0)
            time.sleep(0.2)
    finally:
        conn.close()
        proxy.terminate()


UNSHIFTED = {
    ".": 0x2E, "-": 0x2D, "=": 0x3D, "[": 0x5B, "]": 0x5D,
    "\\": 0x5C, ";": 0x3B, "'": 0x27, ",": 0x2C, "/": 0x2F, "`": 0x60,
}


def _char_parts(ch):
    """Keysym parts for one printable char (Shift combos as needed)."""
    shift = KEYS["SHIFT"]
    if ch == " ":
        return [KEYS["SPC"]]
    if "a" <= ch <= "z" or "0" <= ch <= "9":
        return [ord(ch)]
    if "A" <= ch <= "Z":
        return [shift, ord(ch)]
    if ch in UNSHIFTED:
        return [UNSHIFTED[ch]]
    if ch in SHIFTED:
        base = SHIFTED[ch]
        sym = SHIFTED.get(base, base) if not base.isdigit() else ord(base)
        return [shift, sym]
    raise ValueError(f"untypable char {ch!r}")


def _press_parts(conn, parts, hold=0.08):
    for sym in parts:
        _key(conn, sym, 1)
    time.sleep(hold)
    for sym in reversed(parts):
        _key(conn, sym, 0)


def type_text(text):
    """Type printable text; uppercase/symbols via Shift combos."""
    keys = [_char_parts(ch) for ch in text]
    conn, proxy = _vnc_connect()
    try:
        _handshake(conn)
        for parts in keys:
            _press_parts(conn, parts)
            time.sleep(0.15)
    finally:
        conn.close()
        proxy.terminate()


def pointer(mask, x, y, hold=0.0):
    """Press (mask=1 left, 2 right) at x,y, hold, release."""
    conn, proxy = _vnc_connect()
    try:
        _handshake(conn)
        conn.sendall(struct.pack(">BBHH", 5, mask, x, y))
        time.sleep(hold if hold > 0 else 0.15)
        conn.sendall(struct.pack(">BBHH", 5, 0, x, y))
    finally:
        time.sleep(0.5)
        conn.close()
        proxy.terminate()


CTX_ALIAS = "macos-karnataka"


def shot(path, tries=6):
    """Screenshot the guest console with backoff; returns path."""
    out = None
    for attempt in range(tries):
        subprocess.run(["corral", "context", "use", CTX_ALIAS],
                       capture_output=True)
        out = subprocess.run(
            ["corral", "screenshot", f"kubevirt/{CTX}/{NS}/{VM}",
             "-o", path],
            capture_output=True, text=True, timeout=180)
        if "Screenshot written" in out.stdout:
            return path
        time.sleep(15 + 10 * attempt)
    tail = f"{out.stdout} {out.stderr}"[-500:] if out else "no output"
    raise RuntimeError(f"screenshot failed after {tries} tries: {tail}")


def ocr_text(path):
    """Run tesseract; return full UTF-8 text of the screenshot."""
    out = subprocess.run(["tesseract", path, "stdout", "-l", "eng"],
                         capture_output=True, text=True, timeout=120)
    return out.stdout


def ocr_words(path):
    """Word boxes via tesseract TSV: list of (text, cx, cy, conf)."""
    out = subprocess.run(["tesseract", path, "stdout", "-l", "eng", "tsv"],
                         capture_output=True, text=True, timeout=120)
    words = []
    for line in out.stdout.splitlines()[1:]:
        cols = line.split("\t")
        if len(cols) < 12 or not cols[11].strip():
            continue
        try:
            conf = float(cols[10])
        except ValueError:
            conf = -1.0
        if conf < 0:
            continue
        left, top, w, h = map(int, cols[6:10])
        words.append((cols[11].strip(), left + w // 2, top + h // 2, conf))
    return words


def find_text(path, needle, loose=True):
    """Center of first word sequence matching needle; None if absent."""
    want = needle.lower().split()
    words = ocr_words(path)
    hay = [w[0].lower() for w in words]
    for i in range(len(hay) - len(want) + 1):
        if hay[i:i + len(want)] == want:
            group = words[i:i + len(want)]
            cx = sum(w[1] for w in group) // len(group)
            cy = sum(w[2] for w in group) // len(group)
            return cx, cy
    if loose:
        for i, w in enumerate(words):
            if want[0] in w[0].lower():
                return w[1], w[2]
    return None


def poll(path, needle, timeout=120, vanish=False, interval=8):
    """while loop: shot -> OCR until needle appears (or vanishes)."""
    deadline = time.time() + timeout
    last = ""
    while time.time() < deadline:
        shot(path)
        last = ocr_text(path)
        hit = needle.lower() in last.lower()
        if hit != vanish:
            return True, last
        time.sleep(interval)
    return False, last


def click_text(path, needle, hold=1.0, timeout=60):
    """OCR needle -> press-hold its center; verify pane changed."""
    ok, _ = poll(path, needle, timeout=timeout)
    if not ok:
        raise RuntimeError(f"text {needle!r} never appeared")
    loc = find_text(path, needle)
    if loc is None:
        raise RuntimeError(f"text {needle!r} seen but not located")
    before = ocr_text(path)
    pointer(1, loc[0], loc[1], hold=hold)
    time.sleep(3)
    shot(path)
    after = ocr_text(path)
    return before != after, after


def ring_pixels(path="/tmp/ring-check.png"):
    """Count focus-ring blue pixels (differential blue detector)."""
    from PIL import Image
    shot(path)
    img = Image.open(path).convert("RGB")
    px = img.load()
    n = 0
    for y in range(0, img.height, 2):
        for x in range(0, img.width, 2):
            r, g, b = px[x, y]
            if b > 180 and b - r > 60 and b - g > 40:
                n += 1
    return n


def _crop_blue(path, loc, box=24):
    from PIL import Image
    crop = Image.open(path).convert("RGB").crop(
        (max(0, loc[0] - box), max(0, loc[1] - box // 2),
         loc[0] + box, loc[1] + box // 2))
    px = crop.load()
    return sum(1 for y in range(crop.height) for x in range(crop.width)
               if (lambda c: c[2] > 180 and c[2] - c[0] > 60
                   and c[2] - c[1] > 40)(px[x, y]))


def activate(path, needle, timeout=180):
    """Tab-cycle with ring checks until needle's button is focused.

    Requires Full Keyboard Access in the guest (see module docstring).
    Sends TAB up to 12 times; stops when the OCR-matched word's box
    contains focus blue. Then Space and verify the pane changed.
    Fails fast with an actionable error when Tab moves no ring.
    Returns (activated, ocr_after).
    """
    ok, _ = poll(path, needle, timeout=timeout)
    if not ok:
        raise RuntimeError(f"text {needle!r} never appeared")
    focused = False
    for _ in range(12):
        shot(path)
        loc = find_text(path, needle)
        if loc is not None and _crop_blue(path, loc) > 30:
            focused = True
            break
        send_keys(["TAB"])
        time.sleep(2)
    if not focused:
        raise RuntimeError(
            "Tab never focused the target: enable Full Keyboard Access "
            "in the guest (defaults write -g AppleKeyboardUIMode -int 2) "
            "and relogin, then retry")
    before = ocr_text(path)
    send_keys(["SPC"])
    time.sleep(4)
    ok2, after = poll(path, needle, timeout=60, vanish=True)
    return ok2 or before != after, after


def main(argv):
    cmd, args = argv[0], argv[1:]
    if cmd == "shot":
        print(shot(args[0]))
    elif cmd == "ocr":
        p = args[0] if args else "/tmp/ocr-check.png"
        if args:
            print(ocr_text(p))
        else:
            print(ocr_text(shot(p)))
    elif cmd == "words":
        print(json.dumps(ocr_words(shot(args[0])),
                         indent=0)[:4000])
    elif cmd == "wait":
        ok, text = poll("/tmp/wait.png", args[0],
                        timeout=int(args[1]) if len(args) > 1 else 180)
        print("FOUND" if ok else "TIMEOUT")
        print(text[:2000])
        sys.exit(0 if ok else 1)
    elif cmd == "wait-vanish":
        ok, text = poll("/tmp/wait.png", args[0],
                        timeout=int(args[1]) if len(args) > 1 else 180,
                        vanish=True)
        print("VANISHED" if ok else "STILL THERE")
        sys.exit(0 if ok else 1)
    elif cmd == "click":
        changed, after = click_text(
            "/tmp/click.png", args[0],
            hold=float(args[1]) if len(args) > 1 else 1.0)
        print("CHANGED" if changed else "NO CHANGE")
        print(after[:2000])
    elif cmd == "press":
        pointer(int(args[2]) if len(args) > 2 else 1,
                int(args[0]), int(args[1]),
                hold=float(args[3]) if len(args) > 3 else 1.5)
        print("pressed")
    elif cmd == "type":
        type_text(" ".join(args))
        print("typed")
    elif cmd == "key":
        send_keys(args[0].split(","))
        print("sent")
    elif cmd == "ring":
        print(ring_pixels(args[0] if args else "/tmp/ring-check.png"))
    elif cmd == "activate":
        ok, after = activate("/tmp/activate.png", args[0],
                             timeout=int(args[1]) if len(args) > 1 else 180)
        print("ACTIVATED" if ok else "NOT ACTIVATED")
        print(after[:2000])
        sys.exit(0 if ok else 1)
    else:
        sys.exit(f"unknown command {cmd}")


if __name__ == "__main__":
    main(sys.argv[1:])
