# macos-console driver

Screenshot + tesseract OCR + VNC input with **verify loops** for KubeVirt
macOS guests. Replaces per-step agent driving of Setup Assistant, Apple
sign-in, and TCC/System Settings dialogs.

Verified against a real Sequoia guest (2026-11-04):

- VNC pointer **motion works, release is unreliable** — buttons are
  pressed with a hold and the UI change is verified by OCR, never assumed.
- VNC keysyms reach full macOS (QMP PS/2 does not). Uppercase and symbols
  go through explicit Shift combos (`@` = Shift+2, ...).
- META (Command) does **not** arrive via VNC keysyms. Neither do F-keys
  reliably (media-key mapping) — do not script Ctrl+F7 or Cmd+anything.
- `Tab`/`Space` need Full Keyboard Access in the guest:
  `defaults write -g AppleKeyboardUIMode -int 2` + relogin. Setup
  Assistant panes Tab-navigate regardless.
- `corral screenshot` needs `corral context use macos-karnataka` first and
  the API flakes — every shot retries with backoff.
- tesseract `eng` reads the 1280x800 console cleanly, including menubar.

## Commands

  KVM=<vm> ./driver.py shot OUT.png | ocr [PNG] | words PNG
  KVM=<vm> ./driver.py wait TEXT [TIMEOUT] | wait-vanish TEXT [TIMEOUT]
  KVM=<vm> ./driver.py click TEXT [HOLD]    # OCR -> press-hold, verify change
  KVM=<vm> ./driver.py press X Y [MASK] [HOLD]
  KVM=<vm> ./driver.py type TEXT | key SPEC[,SPEC] | ring | activate TEXT [TIMEOUT]

`activate` Tab-cycles (max 12) until the focus ring surrounds TEXT, then
Space, then verifies the pane changed. Fails fast when FKA is off.
