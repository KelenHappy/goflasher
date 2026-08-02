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

The GUI must not be run as root. The current Linux backend delegates unmount
and power-off operations to `udisksctl`. A dedicated privileged helper has not
yet been implemented. See the README for the current architecture boundary.
