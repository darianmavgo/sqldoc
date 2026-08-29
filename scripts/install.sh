#!/bin/sh
# install.sh - build and install the standalone sqldoc app.
#
# The standalone app is the native window: sqldoc-view, and on macOS the
# sqldoc.app bundle that wraps it so a database can be double-clicked. The
# browser front end (`sqldoc`, `sqldoc serve`) is a separate program and this
# script deliberately does not install it; `make install` is still the way to
# get that one.
#
#   ./scripts/install.sh                  build and install
#   ./scripts/install.sh --user           keep the bundle in ~/Applications
#   ./scripts/install.sh --set-default    also make it open databases on double-click
#   ./scripts/install.sh --uninstall      take it all back off
#
# Options:
#   --prefix DIR    where sqldoc-view and sqldoc-app go (default ~/.local)
#   --app-dir DIR   where sqldoc.app goes on macOS (default /Applications)
#   --user          shorthand for --app-dir ~/Applications
#   --set-default   make sqldoc the handler for .db and friends
#   --no-build      install what is already in bin/, do not rebuild
#   --uninstall     remove everything this script installs
#
# Registering the bundle is what adds sqldoc to Finder's "Open With" menu. It
# claims SQLite files at rank Alternate, so installing does not take the file
# type away from whatever already owns it; --set-default is the separate,
# deliberate step that does.

set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
prefix=${SQLDOC_PREFIX-$HOME/.local}
app_dir=/Applications
set_default=0
build=1
uninstall=0
os=$(uname -s)

usage() { sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
say()   { printf '%s\n' "$*"; }
die()   { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
have()  { command -v "$1" >/dev/null 2>&1; }

while [ $# -gt 0 ]; do
  case $1 in
    --prefix)  [ $# -ge 2 ] || die "--prefix needs a directory";  prefix=$2; shift 2 ;;
    --app-dir) [ $# -ge 2 ] || die "--app-dir needs a directory"; app_dir=$2; shift 2 ;;
    --user)         app_dir=$HOME/Applications; shift ;;
    --set-default)  set_default=1; shift ;;
    --no-build)     build=0; shift ;;
    --uninstall)    uninstall=1; shift ;;
    -h|--help)      usage 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

bindir=$prefix/bin

# ------------------------------------------------------------------ uninstall
# Only ever removes the exact paths this script writes. A prefix pointing
# somewhere unexpected should cost you those files and nothing else.
if [ "$uninstall" = 1 ]; then
  for f in "$bindir/sqldoc-view" "$bindir/sqldoc-app"; do
    [ -e "$f" ] && rm -f "$f" && say "removed $f"
  done
  if [ "$os" = Darwin ]; then
    for d in "$app_dir/sqldoc.app" "$HOME/Applications/sqldoc.app" /Applications/sqldoc.app; do
      if [ -d "$d" ]; then
        lsregister=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
        [ -x "$lsregister" ] && "$lsregister" -u "$d" 2>/dev/null || true
        rm -rf "$d" && say "removed $d"
      fi
    done
    say "if a .db still opens in sqldoc, use Finder's Get Info -> Open with -> Change All"
  else
    f=${XDG_DATA_HOME-$HOME/.local/share}/applications/sqldoc.desktop
    [ -e "$f" ] && rm -f "$f" && say "removed $f"
    have update-desktop-database && \
      update-desktop-database "${XDG_DATA_HOME-$HOME/.local/share}/applications" 2>/dev/null || true
  fi
  exit 0
fi

# -------------------------------------------------------------------- prereqs
# The native window hosts the system WebView, so unlike the browser front end it
# genuinely needs cgo and a C toolchain; there is no pure-Go build of it.
if [ "$build" = 1 ]; then
  have go   || die "go is not installed"
  have make || die "make is not installed"
  have cc || have clang || have gcc || \
    die "a C compiler is required: the native window needs cgo"
fi

# --------------------------------------------------------------------- build
cd "$root"

if [ "$os" = Darwin ]; then
  if [ "$build" = 1 ]; then
    have swiftc || die "swiftc is required to build the app bundle (install Xcode command line tools)"
    say "building the app bundle..."
    make app >/dev/null
  fi
  [ -d bin/sqldoc.app ]  || die "bin/sqldoc.app is missing; run without --no-build"
  [ -x bin/sqldoc-view ] || die "bin/sqldoc-view is missing; run without --no-build"
else
  if [ "$build" = 1 ]; then
    # webview_go links against WebKitGTK; saying so now beats a wall of
    # compiler errors about missing headers.
    if have pkg-config; then
      pkg-config --exists webkit2gtk-4.1 || pkg-config --exists webkit2gtk-4.0 || \
        say "note: webkit2gtk development files were not found; the build may fail"
    fi
    say "building the viewer..."
    make bin/sqldoc-view >/dev/null
  fi
  [ -x bin/sqldoc-view ] || die "bin/sqldoc-view is missing; run without --no-build"
fi

# ------------------------------------------------------------------- install
mkdir -p "$bindir"
install -m 0755 bin/sqldoc-view "$bindir/sqldoc-view"
install -m 0755 scripts/sqldoc-app "$bindir/sqldoc-app"
say "installed $bindir/sqldoc-view"
say "installed $bindir/sqldoc-app"

if [ "$os" = Darwin ]; then
  # /Applications is writable by an admin account and not by a standard one;
  # saying so here beats a cp failure halfway through the copy.
  mkdir -p "$app_dir" 2>/dev/null || true
  [ -w "$app_dir" ] || die "$app_dir is not writable; try --user, or re-run with sudo"
  rm -rf "$app_dir/sqldoc.app"
  cp -R bin/sqldoc.app "$app_dir/sqldoc.app"
  say "installed $app_dir/sqldoc.app"

  # Finder caches an application's icon against the bundle it came from, and a
  # bundle replaced at the same path can keep showing the icon it used to have.
  # Bumping the modification date is what tells it to look again.
  touch "$app_dir/sqldoc.app"

  # Registering is the step that makes Finder aware of the bundle's document
  # types. Copying it into /Applications alone is not enough on every macOS.
  lsregister=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister
  if [ -x "$lsregister" ]; then
    "$lsregister" -f "$app_dir/sqldoc.app" 2>/dev/null || true
    say "registered with Launch Services"
  fi

  if [ "$set_default" = 1 ]; then
    if [ ! -x bin/set-default-handler ] && have swiftc; then
      swiftc -O -o bin/set-default-handler packaging/macos/set-default-handler.swift 2>/dev/null || true
    fi
    if [ -x bin/set-default-handler ]; then
      say "making sqldoc the default handler:"
      ./bin/set-default-handler || say "note: some types could not be claimed"
    else
      say "note: swiftc unavailable; set the default with Finder's Get Info instead"
    fi
  fi
else
  # The Linux equivalent of registering the bundle: a desktop entry is what puts
  # sqldoc in a file manager's "Open With" list.
  apps=${XDG_DATA_HOME-$HOME/.local/share}/applications
  mkdir -p "$apps"
  cat > "$apps/sqldoc.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=sqldoc
Comment=Read-only viewer for SQLite databases
Exec=$bindir/sqldoc-app %F
Terminal=false
Categories=Utility;Database;Viewer;
MimeType=application/vnd.sqlite3;application/x-sqlite3;
DESKTOP
  say "installed $apps/sqldoc.desktop"
  have update-desktop-database && update-desktop-database "$apps" 2>/dev/null || true

  if [ "$set_default" = 1 ]; then
    if have xdg-mime; then
      xdg-mime default sqldoc.desktop application/vnd.sqlite3 application/x-sqlite3
      say "sqldoc is now the default for SQLite files"
    else
      say "note: xdg-mime unavailable; set the default in your file manager"
    fi
  fi
fi

# ---------------------------------------------------------------------- done
say
case ":$PATH:" in
  *":$bindir:"*) ;;
  *) say "$bindir is not on your PATH; add it to open databases by name:"
     say "  export PATH=\"$bindir:\$PATH\""
     say ;;
esac
say "sqldoc-app data.db      open a database in its own window"
if [ "$os" = Darwin ] && [ "$set_default" != 1 ]; then
  say "right-click a .db in Finder -> Open With -> sqldoc"
  say "run './scripts/install.sh --set-default' to open them with a double-click"
fi
