#!/bin/sh
# Install a checksum-verified release binary; setup remains a separate explicit action.
set -eu
umask 077
version=
archive=
checksums=
install_dir=${HOME:?}/.local/bin

# Require explicit releases until a public stable release channel exists.
usage() {
  echo 'usage: sh install.sh --version vX.Y.Z [--dir DIR] [--archive FILE --checksums FILE]' >&2
  exit 2
}
while [ "$#" -gt 0 ]; do
  [ "$#" -ge 2 ] || usage
  case "$1" in
    --version) version=$2 ;;
    --dir) install_dir=$2 ;;
    --archive) archive=$2 ;;
    --checksums) checksums=$2 ;;
    *) usage ;;
  esac
  shift 2
done
printf '%s\n' "$version" | LC_ALL=C awk '/^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/ { ok=1 } END { exit !ok }' || usage
case "$(uname -s)" in Darwin) target_os=darwin ;; Linux) target_os=linux ;; *) echo 'Unsupported OS.' >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) target_arch=amd64 ;; arm64|aarch64) target_arch=arm64 ;; *) echo 'Unsupported architecture.' >&2; exit 1 ;; esac
asset="in-${version}-${target_os}-${target_arch}.tar.gz"
staging=$(mktemp -d)
installed_temp=
trap 'rm -rf "$staging"; [ -z "$installed_temp" ] || rm -f "$installed_temp"' EXIT HUP INT TERM
if [ -n "$archive" ] || [ -n "$checksums" ]; then
  [ -n "$archive" ] && [ -n "$checksums" ] || usage
  cp "$archive" "$staging/$asset"
  cp "$checksums" "$staging/checksums.txt"
else
  base="https://github.com/fschrhunt/in/releases/download/$version"
  curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 "$base/$asset" -o "$staging/$asset"
  curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 "$base/checksums.txt" -o "$staging/checksums.txt"
fi
expected=$(awk -v name="$asset" '$2 == name { count++; value=$1 } END { if (count != 1 || length(value) != 64 || value ~ /[^0-9a-f]/) exit 1; print value }' "$staging/checksums.txt") || { echo 'Missing or ambiguous checksum.' >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$staging/$asset" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$staging/$asset" | awk '{print $1}')
fi
[ "$actual" = "$expected" ] || { echo 'Archive checksum mismatch; nothing installed.' >&2; exit 1; }
tar -xOzf "$staging/$asset" in > "$staging/in"
chmod 700 "$staging/in"
[ "$("$staging/in" --version)" = "$version" ] || { echo 'Archive version mismatch; nothing installed.' >&2; exit 1; }
mkdir -p "$install_dir"
installed_temp=$(mktemp "$install_dir/.in.XXXXXX")
cp "$staging/in" "$installed_temp"
chmod 755 "$installed_temp"
mv -f "$installed_temp" "$install_dir/in"
installed_temp=
printf 'Installed %s/in (%s). Run that path with setup remote or setup laptop HOST.\n' "$install_dir" "$version"
