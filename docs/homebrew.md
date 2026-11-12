# Homebrew Tap Guide

## Installation

The tap provides a binary formula for Linux and macOS on amd64 and arm64.
It downloads a release archive, verifies its SHA-256 checksum and installs
`rdhpf`. Users do not need Go or a local build. macOS requires version 13 or newer.

```bash
brew install --formula tomaszpeksa/tap/rdhpf
rdhpf version
```

Respect Homebrew's normal package-trust prompts. The package is a formula, not
a cask; no formula-to-cask migration is required.

## Upgrading

Before starting v0.2.0, stop older instances sharing the same actual
`HOME/.rdhpf` directory. Different SSH targets do not isolate status ownership.
Read the [v0.2.0 upgrade notes](../CHANGELOG.md#upgrade-notes).

```bash
brew update
brew upgrade --formula tomaszpeksa/tap/rdhpf
rdhpf version
```

The legacy `remote-docker-host-port-forwarder` formula is not the current package.
If it is installed, replace it with `rdhpf` instead of installing both:

```bash
brew uninstall --formula tomaszpeksa/tap/remote-docker-host-port-forwarder
brew install --formula tomaszpeksa/tap/rdhpf
rdhpf version
```

That replacement removes the old command before installing the new one; plan
for the interruption. Do not force-overwrite conflicting binaries or symlinks.

## Packaging and macOS Security

The formula retains the prebuilt-binary installation model. If that packaging
causes installation or compatibility problems, the fallback plan is to switch
to a source-built formula. Homebrew would install the build tools and the recipe
would fetch the pinned Go modules automatically; users would not manage them
manually. This fallback is not a second installation mode currently provided by
the tap.

The release binaries are not Developer ID signed or notarized. A checksum
verifies archive consistency, not Apple publisher identity. Direct macOS
downloads may be blocked by Gatekeeper. No quarantine-removal hooks, Gatekeeper
bypasses or global security changes are configured. Stop and report any security
block rather than disabling those checks.

## Verification

```bash
brew info --formula tomaszpeksa/tap/rdhpf
rdhpf version
brew test tomaszpeksa/tap/rdhpf
```

## Removal

To remove the application:

```bash
brew uninstall --formula tomaszpeksa/tap/rdhpf
```

## Maintaining the Tap

GoReleaser publishes the four GitHub release archives and `checksums.txt`.
The binary formula is maintained separately in
[`tomaszpeksa/homebrew-tap`](https://github.com/tomaszpeksa/homebrew-tap).
GoReleaser's retired `brews` generator and its `homebrew_casks` publisher are
not used.

For each release:

1. Verify the published archives against `checksums.txt`.
2. Update `Formula/rdhpf.rb` with the real release version, four archive URLs and
   matching checksums. Never use snapshot URLs.
3. Review the tap diff. Test installation or upgrade, `rdhpf version` and
   `brew test` on macOS and Linux before publishing; record any untested platform.
4. Push the reviewed formula to the tap with the existing write access. GitHub
   release publication does not automatically update the tap. No additional
   token permissions or automated pull requests are required.

## Troubleshooting

- **Old version:** run `brew update`, check `brew info --formula
  tomaszpeksa/tap/rdhpf` and confirm the tap has the release version.
- **Checksum mismatch:** stop and compare the published archive and formula
  checksum. Do not disable verification.
- **Conflicting command:** inspect installed packages before replacing anything.
  Do not force-overwrite unrelated executables.
- **macOS security block:** report the exact error and installation route. The
  source-built formula is the planned packaging fallback, not a security bypass.
