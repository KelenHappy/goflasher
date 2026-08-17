# Release tooling trust chain

Production release workflows pin every GitHub Action to a full commit SHA. The
human-readable version comment is informational; updates must review the
upstream commit and change the SHA in the same pull request. Dependabot can
continue proposing `github-actions` updates, but a floating major tag must not
be reintroduced.

The former AppImage build downloaded and executed `appimagetool` and
`linuxdeploy` from their mutable `continuous` releases. GitHub's release digest
was fetched from the same mutable release metadata as the executable, so it was
not an independent trust anchor. AppImage production output is disabled until
reviewed immutable releases and repository-pinned SHA-256 values are available.
Debian, RPM, and Arch packages remain produced by the pinned Ubuntu runner and
its distribution package manager.

`download-github-release-asset.sh` is retained for future tooling but requires a
non-mutable tag and a caller-supplied SHA-256 value committed by the consuming
workflow. It never treats an upstream-reported digest as authorization.

Build/signing jobs have read-only `GITHUB_TOKEN` permissions. Windows and macOS
signing credentials are scoped to their signing steps. Only the final publisher
job receives `contents: write`; it has no source checkout or signing secrets and
only publishes artifacts transferred through the SHA-pinned artifact actions.
GitHub-hosted runner images, Ubuntu archives, Apple notarization, timestamping,
and GitHub's artifact/release services remain trusted third parties.
