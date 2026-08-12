# Image assembled by GoReleaser. The binary is cross-compiled before the build
# context is created, so no Go toolchain runs here.
#
# Not `distroless/static`, even though the exporter is a CGO_ENABLED=0 binary
# that would run there: the container is useless without `mlxlink`, and MFT
# ships it dynamically linked against glibc. A host that bind mounts MFT into
# the container still needs a C library present for the exec to succeed, so the
# glibc-carrying `base` variant is the smallest base that can actually work.
FROM gcr.io/distroless/base-debian12:nonroot

# One build context serves every platform, so the binaries cannot share a path:
# GoReleaser files each under `<os>/<arch>/`, and only TARGETPLATFORM says which
# one this stage is building. Files listed in `extra_files` stay at the context
# root, which is why LICENSE is copied unprefixed.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/mlxlink_exporter /usr/local/bin/mlxlink_exporter
COPY LICENSE /usr/local/share/mlxlink_exporter/LICENSE

EXPOSE 9880
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/mlxlink_exporter"]
