#!/bin/sh
# Install/update the WeirdPhrog fork on Entware (including Keenetic/Netcraze).
set -eu

die() { printf 'MagiTrickle: %s\n' "$*" >&2; exit 1; }
download() {
    if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 3 --connect-timeout 20 --max-time 300 "$1" -o "$2"
    else
        wget -O "$2" "$1"
    fi
}

select_target() {
    target=$("$opkg" print-architecture | awk '
        $1 == "arch" && $2 ~ /^(mipsel-3\.4|mips-3\.4|aarch64-3\.10|armv7-3\.2)(_kn)?$/ && $3 > priority {
            priority = $3; target = $2
        }
        END { print target }')
    [ -n "$target" ] || die 'Unsupported Entware architecture.'
    case "$target" in
        *_kn) ;;
        mipsel-3.4|mips-3.4|aarch64-3.10)
            if [ -d /etc/ndm ] || [ -d /opt/etc/ndm ] || command -v ndmc >/dev/null 2>&1; then
                target="${target}_kn"
            fi
            ;;
    esac
    case "$target" in
        mipsel-3.4|mipsel-3.4_kn|mips-3.4|mips-3.4_kn|aarch64-3.10|aarch64-3.10_kn|armv7-3.2) ;;
        *) die "Unsupported target: $target" ;;
    esac
}

restore_config() {
    if [ -f "$mt_tmp/config.yaml" ] && ! cmp -s "$mt_tmp/config.yaml" /opt/var/lib/magitrickle/config.yaml; then
        cp -p "$mt_tmp/config.yaml" /opt/var/lib/magitrickle/config.yaml
    fi
}

main() {
    PATH=/opt/bin:/opt/sbin:$PATH
    export PATH
    opkg=/opt/bin/opkg
    [ -x "$opkg" ] || die 'Install Entware first, then run this from the Entware shell (SSH port 222).'
    [ "$(id -u)" = 0 ] || die 'Run as root in the Entware shell.'
    command -v sha256sum >/dev/null 2>&1 || die 'sha256sum is required.'
    check_only=false
    version=
    explicit_version=false
    case "${1:-}" in
        '') [ "$#" = 0 ] || die 'Unexpected arguments.' ;;
        --check) [ "$#" = 1 ] || die 'Usage: magitrickle-update [--check | --version VERSION]'; check_only=true ;;
        --version) [ "$#" = 2 ] || die '--version needs a release version.'; version=$2; explicit_version=true ;;
        *) die 'Usage: magitrickle-update [--check | --version VERSION]' ;;
    esac
    select_target
    mt_tmp=$(mktemp -d /tmp/magitrickle-update.XXXXXX)
    mt_locked=false
    trap 'rm -rf "$mt_tmp"; if [ "$mt_locked" = true ]; then rmdir /tmp/magitrickle-update.lock; fi' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    base=https://github.com/WeirdPhrog/MagiTrickle/releases
    if [ -z "$version" ]; then
        download "$base/latest/download/VERSION" "$mt_tmp/VERSION" || die 'Cannot fetch the fork release. Check HTTPS access and CA certificates.'
        version=$(cat "$mt_tmp/VERSION")
    fi
    printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' || die 'Invalid release version.'
    package="magitrickle_${version}-1_entware_${target}.ipk"
    release="$base/download/v$version"
    installed=$("$opkg" status magitrickle | awk '/^Version: / { print $2; exit }')
    printf 'Fork: WeirdPhrog/MagiTrickle | Target: %s | Installed: %s | Release: %s\n' "$target" "${installed:-none}" "$version-1"
    [ "$check_only" = false ] || return 0
    if [ "$installed" = "$version-1" ]; then
        printf 'Already up to date.\n'
        return 0
    fi
    if [ -n "$installed" ] && [ "$explicit_version" = false ] && "$opkg" compare-versions "$installed" '>' "$version-1"; then
        die 'Installed version is newer. Use --version explicitly to downgrade.'
    fi
    # /tmp is cleared on reboot; a power loss must not leave a permanent lock.
    mkdir /tmp/magitrickle-update.lock 2>/dev/null || die 'Another update is running (lock: /tmp/magitrickle-update.lock).'
    mt_locked=true
    # Pin both downloads to the same immutable release, even if latest changes.
    download "$release/SHA256SUMS" "$mt_tmp/SHA256SUMS" || die 'Cannot download release checksums.'
    checksum=$(awk -v file="$package" '$2 == file { print $1 }' "$mt_tmp/SHA256SUMS")
    printf '%s\n' "$checksum" | grep -Eq '^[a-fA-F0-9]{64}$' || die 'Package is missing from release checksums.'
    download "$release/$package" "$mt_tmp/$package" || die 'Cannot download package.'
    printf '%s  %s\n' "$checksum" "$package" > "$mt_tmp/checksum"
    (cd "$mt_tmp" && sha256sum -c checksum) || die 'Package checksum mismatch; nothing installed.'

    # Back up state before invoking opkg. Conffiles also preserve config.yaml.
    set --
    [ ! -d /opt/var/lib/magitrickle ] || set -- "$@" var/lib/magitrickle
    [ ! -d /opt/etc/magitrickle ] || set -- "$@" etc/magitrickle
    if [ "$#" -gt 0 ]; then
        mkdir -p /opt/var/backups/magitrickle
        backup="/opt/var/backups/magitrickle/before-${version}-$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
        (umask 077; tar -czf "$backup" -C /opt "$@")
        printf 'Configuration backup: %s\n' "$backup"
    fi
    if [ -f /opt/var/lib/magitrickle/config.yaml ]; then
        cp -p /opt/var/lib/magitrickle/config.yaml "$mt_tmp/config.yaml"
    fi
    mkdir -p /opt/etc/opkg
    printf 'arch %s 250\n' "$target" > /opt/etc/opkg/magitrickle-fork.conf
    "$opkg" update
    # Prevent a general opkg upgrade from replacing this fork with upstream.
    if [ -n "$installed" ]; then "$opkg" flag ok magitrickle; fi
    if "$opkg" --force-downgrade install "$mt_tmp/$package"; then
        "$opkg" flag hold magitrickle
    else
        restore_config
        if [ -n "$installed" ]; then "$opkg" flag hold magitrickle; fi
        die 'Package installation failed. Configuration backup has been retained.'
    fi
    restore_config
    /opt/etc/init.d/S99magitrickle restart
    sleep 2
    /opt/etc/init.d/S99magitrickle check || die 'Package installed, but service is not running. Check the router log.'
    printf 'Installed %s from WeirdPhrog/MagiTrickle. Future updates: magitrickle-update\n' "$version"
}

main "$@"
