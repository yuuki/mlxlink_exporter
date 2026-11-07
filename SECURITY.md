# Security policy

mlxlink_exporter runs NVIDIA MFT's `mlxlink`, which talks to the adapter
firmware, and may be granted extra privileges for that (up to the root
override in `deploy/systemd/`). It reads `/sys/class/infiniband`, parses the
JSON `mlxlink` prints, and serves the results over plain HTTP. See
[Security considerations](docs/design.md#8-security-considerations) for what
it does and does not do.

Examples of what is in scope:

- Any way to make the exporter run something other than the configured
  `mlxlink` binary, or `mlxlink` with arguments other than its fixed
  vectors (for instance through a device name).
- Output from `mlxlink` or a sysfs entry that crashes the exporter, makes a
  scrape fail, exhausts its memory, or lands free-form text in a label.
- Privileges in the shipped systemd unit, the root override or the container
  image that are broader than the documentation says.
- Problems with the release archives, checksums or the published image.

Out of scope: vulnerabilities in MFT or `mlxlink` themselves (report those
to NVIDIA), and the absence of authentication and TLS on `/metrics`, which
like most Prometheus exporters is expected to be reachable only from the
monitoring network.

## Supported versions

Security fixes are made on the `main` branch and released in the next
version. Only the latest release is supported.

## Reporting a vulnerability

Please do **not** open a public issue, pull request or discussion.

Report the vulnerability privately with GitHub's
[private vulnerability reporting](https://github.com/yuuki/mlxlink_exporter/security/advisories/new)
(the "Report a vulnerability" button of the Security tab). If that is not
possible, contact the author privately via the contact information on
https://github.com/yuuki.

Please include the exporter version (`mlxlink_exporter --version`), the MFT
version, how it is deployed (shipped unit, root override, container), the
flags or environment it runs with, what an attacker controls (sysfs, the
`mlxlink` binary or its output, the network in front of `/metrics`, …),
steps to reproduce and the impact. You should receive an answer within a
few weeks; mlxlink_exporter is maintained on a best-effort basis.

## Verifying releases

Every release publishes `mlxlink_exporter_checksums.txt`; verify a
downloaded archive against it (see [Install](README.md#install)).
