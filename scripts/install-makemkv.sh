#!/usr/bin/env bash
# Build and install MakeMKV from the official source tarballs.
#
# Ubuntu 26.04 has no MakeMKV package and the usual PPA has no build for it, so
# this compiles from source. Only makemkvcon is needed — hellbox never uses the
# GUI — so the Qt dependency is skipped.
set -Eeuo pipefail

PREFIX="${PREFIX:-/usr/local}"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
VERSION="${MAKEMKV_VERSION:-}"
TARBALL_DIR="${TARBALL_DIR:-}"
ACCEPT_EULA=0

usage() {
  cat <<'USAGE'
Usage: install-makemkv.sh [options]

Options:
  --version X.Y.Z   Install a specific version (default: latest)
  --accept-eula     Accept the MakeMKV EULA without prompting
  --prefix PATH     Install prefix (default: /usr/local). Applies to both
                    halves; they must agree or makemkvcon cannot find its
                    helper binaries.
  --tarball-dir D   Use makemkv-{oss,bin}-VERSION.tar.gz from D instead of
                    downloading. Needs --version, and skips makemkv.com
                    entirely.
  --help            Show this help

Requires sudo for dependency installation and for `make install`.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:?--version requires a value}"; shift 2 ;;
    --accept-eula) ACCEPT_EULA=1; shift ;;
    --prefix) PREFIX="${2:?--prefix requires a value}"; shift 2 ;;
    --tarball-dir) TARBALL_DIR="${2:?--tarball-dir requires a value}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null || die "curl is required"

if [[ -n "$TARBALL_DIR" && -z "$VERSION" ]]; then
  die "--tarball-dir needs --version: the version cannot be guessed from a local directory"
fi

if [[ -z "$VERSION" ]]; then
  say "Determining the current MakeMKV version"
  # The download page links only the Windows and Mac builds — it points Linux
  # users at a forum thread instead. So the version cannot be read off a Linux
  # tarball link; it is taken from the version-stamped artifacts that are
  # published there (the hash file, falling back to the Windows installer).
  # The tarballs themselves do exist under /download/, they are just unlinked.
  page="$(curl -fsSL https://www.makemkv.com/download/)" \
    || die "could not reach makemkv.com; pass --version to skip this step"
  VERSION="$(grep -oE 'makemkv-sha-[0-9]+(\.[0-9]+)+\.txt' <<<"$page" \
    | head -1 | grep -oE '[0-9]+(\.[0-9]+)+')"
  if [[ -z "$VERSION" ]]; then
    VERSION="$(grep -oE 'Setup_MakeMKV_v[0-9]+(\.[0-9]+)+\.exe' <<<"$page" \
      | head -1 | grep -oE '[0-9]+(\.[0-9]+)+')"
  fi
  [[ -n "$VERSION" ]] || die "could not determine the latest version; pass --version"
fi
echo "MakeMKV version: $VERSION"

# MakeMKV's binary component is proprietary and carries its own licence. It is
# free for DVD ripping; Blu-ray support runs on a beta key that expires roughly
# monthly. hellbox surfaces an expired key as a health check rather than letting
# it present as a mystery rip failure.
if [[ "$ACCEPT_EULA" -ne 1 ]]; then
  cat <<EOF

The MakeMKV binary component is proprietary software with its own licence:

    https://www.makemkv.com/eula/

You must accept it to install. Re-run with --accept-eula once you have read it.
EOF
  exit 1
fi

say "Installing build dependencies"
sudo apt-get update
sudo apt-get install -y --no-install-recommends \
  build-essential pkg-config \
  libssl-dev libexpat1-dev zlib1g-dev \
  libavcodec-dev libavutil-dev libavformat-dev

cd "$WORKDIR"

# Default to looking beside the work directory, so a re-run that reuses WORKDIR
# finds what the last one downloaded.
TARBALL_DIR="${TARBALL_DIR:-$WORKDIR}"

# Reuse tarballs already on disk before reaching for the network.
#
# Reinstalling to correct a prefix needs no new source, and makemkv.com is not
# always reachable — it has no CDN and rate-limits. Having to re-download a
# working tree to change one install path is a poor trade, so a local copy is
# used when there is one.
have_local=1
for part in oss bin; do
  [[ -f "${TARBALL_DIR}/makemkv-${part}-${VERSION}.tar.gz" ]] || have_local=0
done

if (( have_local )); then
  say "Using tarballs already in ${TARBALL_DIR}"
  for part in oss bin; do
    [[ "$TARBALL_DIR" -ef "$WORKDIR" ]] \
      || cp "${TARBALL_DIR}/makemkv-${part}-${VERSION}.tar.gz" "$WORKDIR/"
  done
else
  say "Downloading source"
  # Confirm both tarballs exist before committing to a download, so a version
  # that was guessed wrong reports itself plainly rather than as a bare 404.
  for part in oss bin; do
    url="https://www.makemkv.com/download/makemkv-${part}-${VERSION}.tar.gz"
    curl -fsSI --max-time 30 "$url" -o /dev/null \
      || die "no makemkv-${part} tarball for version ${VERSION} at ${url}
Check the current version at https://www.makemkv.com/forum/viewtopic.php?f=3&t=224 and pass --version.
If makemkv.com is unreachable, download both tarballs by hand and pass
--tarball-dir with the directory holding them."
  done
  curl -fsSLO "https://www.makemkv.com/download/makemkv-oss-${VERSION}.tar.gz"
  curl -fsSLO "https://www.makemkv.com/download/makemkv-bin-${VERSION}.tar.gz"
fi

tar xzf "makemkv-oss-${VERSION}.tar.gz"
tar xzf "makemkv-bin-${VERSION}.tar.gz"

say "Building makemkv-oss (without the Qt GUI)"
cd "makemkv-oss-${VERSION}"
./configure --prefix="$PREFIX" --disable-gui
make -j"$(nproc)"
sudo make install

say "Building makemkv-bin"
cd "../makemkv-bin-${VERSION}"
mkdir -p tmp
echo accepted > tmp/eula_accepted
make -j"$(nproc)"
# PREFIX must be passed explicitly. makemkv-bin's Makefile assigns PREFIX=/usr
# unconditionally, so without this makemkvcon installs under /usr while
# makemkv-oss puts mmgplsrv and mmccextr under --prefix. makemkvcon resolves
# those helpers relative to its own location, so the two halves disagreeing
# leaves it unable to find either: mmgplsrv is what reads the disc, and
# mmccextr is what converts closed captions. Both failures are indirect —
# "Failed to open disc", and a rip that dies only on titles that carry
# subtitles — and neither names the prefix as the cause.
sudo make install PREFIX="$PREFIX"

say "Verifying"
hash -r
if ! command -v makemkvcon >/dev/null; then
  die "makemkvcon is still not on PATH after install"
fi

# The helpers have to sit beside makemkvcon, not merely exist somewhere. This is
# checked rather than assumed because the mismatch is silent: makemkvcon starts,
# reports its version, scans a disc, and only fails later during a rip.
con_dir="$(dirname "$(command -v makemkvcon)")"
missing=()
for helper in mmgplsrv mmccextr; do
  [[ -x "${con_dir}/${helper}" ]] || missing+=("$helper")
done
if (( ${#missing[@]} )); then
  found=""
  for helper in "${missing[@]}"; do
    other="$(command -v "$helper" 2>/dev/null || true)"
    [[ -n "$other" ]] && found+="
  ${helper} is at ${other}"
  done
  die "makemkvcon is at ${con_dir}/makemkvcon but cannot find: ${missing[*]}${found}
Both halves must share one prefix. Re-run with --prefix ${con_dir%/bin}."
fi

makemkvcon -r info disc:9999 2>&1 | grep -m1 'MakeMKV v' || true

cat <<EOF

MakeMKV installed.

The registration key lives at ~/.MakeMKV/settings.conf and must belong to the
user hellboxd runs as. The current beta key is published at:

    https://forum.makemkv.com/forum/viewtopic.php?f=5&t=1053

It expires roughly monthly. \`hellboxd -check\` reports an expired key directly,
so a lapsed key never presents as an unexplained rip failure.
EOF
