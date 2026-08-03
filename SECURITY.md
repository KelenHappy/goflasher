# Security policy

GoFlasher writes directly to block devices, so security and device-selection
bugs can cause irreversible data loss.

## Supported versions

Security fixes are provided for the latest GitHub release and the current
default branch. Older builds should be upgraded before reporting a problem.

## Reporting a vulnerability

Do **not** open a public issue for a vulnerability. Use GitHub's **Report a
vulnerability** button on the repository's Security tab to send a private
security advisory. Include:

- the affected version and Linux distribution;
- reproduction steps using a disposable image and device where possible;
- the expected and observed device path, model, serial, and size;
- logs with personal paths and device serial numbers redacted; and
- your assessment of impact.

Maintainers should acknowledge a complete report within seven days. There is
no bug-bounty program. Please do not test against devices containing valuable
data and do not publish details until a fix is available.

## Release verification

Every packaged release contains `SHA256SUMS`. Download it from the same GitHub
release as the package and verify before installation:

```sh
sha256sum --check SHA256SUMS
```

Checksums detect download corruption and replacement after publication; they
are not a substitute for reviewing the GitHub release and repository history.

## Privilege boundary

The GUI must not be run as root and is never granted a raw-device descriptor.
Unmount and power-off remain delegated to `udisksctl`. Write, read-back, and
flush are performed by the root-owned `/usr/libexec/goflasher-helper`, launched
by `pkexec` under the packaged polkit policy.

The IPC request schema intentionally contains no arbitrary file path. It
contains only the already revalidated hardware identity, serial/WWN when
available, expected major/minor, exact capacity, and one of `write`,
`read-back`, or `flush`. Before opening anything, the helper independently
resolves `/sys/dev/block/<major>:<minor>`, compares sysfs identity and capacity,
checks the derived `/dev` node's block type and device number, rejects mounted
devices and system/swap disks, and derives the device-node path itself. A
replacement, missing proc/sysfs safety metadata, malformed request, unknown
field, unsupported mode, or canceled authorization fails closed.

The helper accepts one request and one bounded operation per process. It does
not parse images, enumerate caller-selected paths, mount filesystems, or expose
a general-purpose root service. Polkit uses `auth_admin` without retained
authorization, so users should expect an authentication prompt for each raw
write/read-back/flush phase. Package ownership of both the helper and policy is
part of the trust boundary; AppImage users must install the bundled, auditable
copies into fixed root-owned locations before raw access can work.
