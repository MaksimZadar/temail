#!/usr/bin/env bash
# Report-only environment check. Always exits 0: missing pieces are reported,
# never treated as a hard failure.

ok()   { printf '  [ok]    %s\n' "$1"; }
miss() { printf '  [MISS]  %s\n' "$1"; }
info() { printf '  [info]  %s\n' "$1"; }

echo "== temail doctor =="
echo "Platform: $(uname -s 2>/dev/null || echo unknown)"

case "$(uname -s 2>/dev/null)" in
  Linux*)
    echo
    echo "-- Linux build dependencies (GTK4 / WebKitGTK 6.0) --"
    if ! command -v pkg-config >/dev/null 2>&1; then
      miss "pkg-config not found (pacman -S --needed pkgconf)"
    else
      for mod in gtk4 webkitgtk-6.0; do
        if pkg-config --exists "$mod" 2>/dev/null; then
          ok "$mod $(pkg-config --modversion "$mod")"
        else
          miss "$mod not found by pkg-config"
        fi
      done
    fi
    if command -v gcc >/dev/null 2>&1 || command -v clang >/dev/null 2>&1; then
      ok "C compiler available"
    else
      miss "no C compiler (pacman -S --needed base-devel)"
    fi
    ;;
  MINGW*|MSYS*|CYGWIN*|Windows*)
    echo
    echo "-- Windows --"
    info "WebView2 Runtime is required (preinstalled on most Windows 10/11)."
    ;;
  *)
    echo
    info "Unrecognised OS; skipping OS-specific checks."
    ;;
esac

echo
echo "-- Wails CLI --"
if command -v wails3 >/dev/null 2>&1; then
  ok "wails3 $(wails3 version 2>/dev/null | head -n1)"
  echo "  (running 'wails3 doctor' for the full report)"
  wails3 doctor 2>&1 | sed 's/^/    /' || info "wails3 doctor reported problems (see above)"
else
  miss "wails3 not on PATH (run: mise run setup)"
fi

echo
echo "-- Proton Bridge (127.0.0.1) --"
port_open() {
  # Bash /dev/tcp works on Linux and Git Bash; timeout bounds the connect.
  timeout 2 bash -c "exec 3<>/dev/tcp/127.0.0.1/$1" >/dev/null 2>&1
}
if port_open 1143; then ok "IMAP port 1143 is listening"; else info "IMAP port 1143 not listening (Proton Bridge not running?)"; fi
if port_open 1025; then ok "SMTP port 1025 is listening"; else info "SMTP port 1025 not listening (Proton Bridge not running?)"; fi

echo
echo "doctor finished (report only)."
exit 0
