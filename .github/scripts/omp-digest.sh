#!/usr/bin/env bash
# The Oh My Pi build .omp-pin names is a release of a fork, cut by hand, and a release's tag can be
# moved: mise installs whatever bytes the tag names on the day it runs. So each install of the pin
# checks those bytes here, against the digest .omp-pin.sha256 records, before anything runs them.
# The release's one asset holds `bin/omp` and nothing else, so that file's digest covers every byte
# an install copies.
#
# The digest file is one line in sha256sum's format: 64 lower-case hex digits, two spaces, and the
# path of the file relative to the install directory (`mise where "$pin"`). A pin bump writes the new
# build's line in the same commit, hashed from the build the bump was tested on:
#   (cd "$(mise where "$pin")" && sha256sum bin/omp) > .omp-pin.sha256
#
# This script reads the file and hashes it; it runs nothing it checks. On a match it prints one line
# naming the file and its digest and exits 0. On another digest or a missing file it prints one line
# to stderr naming the file, the digest it expected and the digest it read, and exits 1; a digest
# file not in that format exits 1 too.
#
# Run from anywhere: .github/scripts/omp-digest.sh <install dir> <digest file>
# Its callers are .github/actions/install-omp and the tools stage of
# packages/daemon/docker/worker.Dockerfile, each between the install and the first run of the binary.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <install dir> <digest file>" >&2
  exit 2
fi
dir=$1 digests=$2

lines=()
if ! { mapfile -t lines <"$digests"; } 2>/dev/null; then
  echo "$digests: cannot be read" >&2
  exit 1
fi
# An explicit digit list rather than a range: under a UTF-8 locale bash's bracket ranges can match
# characters past ASCII.
hex='[0123456789abcdef]'
pattern="^($hex{64})  ([^/[:space:]][^[:space:]]*)\$"
if [ "${#lines[@]}" -ne 1 ] || ! [[ ${lines[0]} =~ $pattern ]]; then
  echo "$digests: not one line of 64 lower-case hex digits, two spaces and a relative path" >&2
  exit 1
fi
want=${BASH_REMATCH[1]} path=${BASH_REMATCH[2]}
case "/$path/" in
*/../*)
  echo "$digests: the path $path leaves the install directory" >&2
  exit 1
  ;;
esac

file=$dir/$path
if [ ! -f "$file" ]; then
  echo "$file: expected sha256 $want ($digests), read nothing: no such file" >&2
  exit 1
fi
got=$(sha256sum <"$file")
got=${got%% *}
if [ "$got" != "$want" ]; then
  echo "$file: expected sha256 $want ($digests), read sha256 $got" >&2
  exit 1
fi
echo "$file: sha256 $got, as $digests pins"
