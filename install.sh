#!/bin/bash

set -euo pipefail

REPOSITORY="leo1394/homebrew-conven"
DEFAULT_BRANCH="master"
INSTALL_DIR="${CONVEN_INSTALL_DIR:-${HOME}/.local/bin}"
REQUESTED_VERSION="${1:-${CONVEN_VERSION:-}}"
TEMP_DIR=""
STAGED_FILE=""
VERSION_PARTS=""

print_info() {
  printf '=> %s\n' "$1"
}

print_error() {
  printf 'conven installer: %s\n' "$1" >&2
}

cleanup() {
  if [[ -n "${STAGED_FILE}" ]]
  then
    rm -f "${STAGED_FILE}"
  fi
  if [[ -n "${TEMP_DIR}" ]]
  then
    rm -rf "${TEMP_DIR}"
  fi
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

download() {
  curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 10 "$1" -o "$2"
}

sha256_file() {
  if command -v shasum >/dev/null 2>&1
  then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1
  then
    sha256sum "$1" | awk '{print $1}'
  else
    print_error "shasum or sha256sum is required"
    exit 1
  fi
}

for command in curl tar go
do
  if ! command -v "${command}" >/dev/null 2>&1
  then
    print_error "${command} is required; the Bash fallback builds Conven from source with Go 1.23 or later"
    exit 1
  fi
done

GO_VERSION="$(go env GOVERSION 2>/dev/null || true)"
GO_VERSION="${GO_VERSION#go}"
GO_MAJOR="$(printf '%s' "${GO_VERSION}" | awk -F. '{print $1}')"
GO_MINOR="$(printf '%s' "${GO_VERSION}" | awk -F. '{print $2}')"
case "${GO_MAJOR}.${GO_MINOR}" in
  *[!0-9.]* | .* | *.)
    print_error "could not determine the installed Go version"
    exit 1
    ;;
  *) ;;
esac
if [[ "${GO_MAJOR}" -lt 1 ]] || { [[ "${GO_MAJOR}" -eq 1 ]] && [[ "${GO_MINOR}" -lt 23 ]]; }
then
  print_error "Go 1.23 or later is required; found go ${GO_VERSION}"
  exit 1
fi

if [[ -z "${REQUESTED_VERSION}" ]]
then
  print_info "Resolving the latest published version"
  REQUESTED_VERSION="$(curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 10 \
    "https://raw.githubusercontent.com/${REPOSITORY}/${DEFAULT_BRANCH}/VERSION.txt")"
fi

VERSION="${REQUESTED_VERSION#v}"
case "${VERSION}" in
  '' | *[!0-9.]* | .* | *..* | *.)
    print_error "invalid version: ${REQUESTED_VERSION}"
    exit 1
    ;;
  *) ;;
esac
VERSION_PARTS="$(printf '%s' "${VERSION}" | awk -F. '{print NF}')"
if [[ "${VERSION_PARTS}" -ne 3 ]]
then
  print_error "invalid version: ${REQUESTED_VERSION}"
  exit 1
fi

case "${INSTALL_DIR}" in
  /*) ;;
  *)
    print_error "CONVEN_INSTALL_DIR must be an absolute path"
    exit 1
    ;;
esac

TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/conven-install.XXXXXX")"
CHECKSUM_FILE="${TEMP_DIR}/conven.sha256"
FORMULA="${TEMP_DIR}/conven.rb"
ARCHIVE="${TEMP_DIR}/conven.tar.gz"
SOURCE_DIR="${TEMP_DIR}/source"
EXECUTABLE="${TEMP_DIR}/conven"
TAG="v${VERSION}"

print_info "Downloading Conven ${VERSION} source"
download "https://github.com/${REPOSITORY}/archive/refs/tags/${TAG}.tar.gz" "${ARCHIVE}"
if curl -fsL --retry 3 --retry-delay 1 --connect-timeout 10 \
   "https://raw.githubusercontent.com/${REPOSITORY}/${DEFAULT_BRANCH}/checksums/conven-${VERSION}.sha256" \
   -o "${CHECKSUM_FILE}" 2>/dev/null
then
  EXPECTED_SHA="$(awk 'NR == 1 {print $1}' "${CHECKSUM_FILE}")"
else
  download "https://raw.githubusercontent.com/${REPOSITORY}/${DEFAULT_BRANCH}/Formula/conven.rb" "${FORMULA}"
  if ! grep -Fq "/refs/tags/${TAG}.tar.gz\"" "${FORMULA}"
  then
    print_error "no published SHA256 was found for Conven ${VERSION}"
    exit 1
  fi
  EXPECTED_SHA="$(awk '/^[[:space:]]*sha256 "[0-9a-f]+"/ {gsub(/"/, "", $2); print $2; exit}' "${FORMULA}")"
fi
if [[ "${#EXPECTED_SHA}" -ne 64 ]]
then
  print_error "could not read the published SHA256 for Conven ${VERSION}"
  exit 1
fi
ACTUAL_SHA="$(sha256_file "${ARCHIVE}")"
if [[ "${ACTUAL_SHA}" != "${EXPECTED_SHA}" ]]
then
  print_error "SHA256 verification failed for Conven ${VERSION}"
  exit 1
fi

mkdir "${SOURCE_DIR}"
tar -xzf "${ARCHIVE}" -C "${SOURCE_DIR}" --strip-components=1
print_info "Building Conven ${VERSION} with Go ${GO_VERSION}"
(
  cd "${SOURCE_DIR}"
  go build -trimpath -ldflags "-s -w" -o "${EXECUTABLE}" ./cmd/conven
)

if ! "${EXECUTABLE}" --version | grep -Fq "conven version ${VERSION} "
then
  print_error "built executable did not report Conven version ${VERSION}"
  exit 1
fi

mkdir -p "${INSTALL_DIR}"
STAGED_FILE="$(mktemp "${INSTALL_DIR}/.conven.XXXXXX")"
cp "${EXECUTABLE}" "${STAGED_FILE}"
chmod 0755 "${STAGED_FILE}"
mv -f "${STAGED_FILE}" "${INSTALL_DIR}/conven"
STAGED_FILE=""

print_info "Installed Conven ${VERSION} to ${INSTALL_DIR}/conven"
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    printf 'Add this directory to PATH, then open a new shell:\n'
    printf '  export PATH="%s:%s"\n' "${INSTALL_DIR}" "\$PATH"
    ;;
esac
