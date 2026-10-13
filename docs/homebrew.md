# Homebrew Tap Guide

## Overview

The supported publisher is GoReleaser's `homebrew_casks` section. It generates
`Casks/rdhpf.rb` for `tomaszpeksa/homebrew-tap`, using the existing Linux/macOS
amd64/arm64 release archives. These are binary casks, not source-built formulae.
Use a current Homebrew with cross-platform binary-cask support; older clients
are not covered by this migration.

The project configuration does not itself publish the separate tap transition.
The cask and migration metadata must be reviewed and published before the cask
installation commands below become available. Configuration and snapshot checks
are not proof of installed migration or successful first launch on either OS.

## User Installation

After the tap transition is published:

```bash
brew install --cask tomaszpeksa/tap/rdhpf
rdhpf version
```

Alternatively, add the tap explicitly with `brew tap tomaszpeksa/tap`, then
`brew install --cask rdhpf`. Respect Homebrew's normal tap-trust prompts.

### Migrating from the Formula

Once the tap contains its migration metadata and cask, upstream's supported route is:

```bash
brew update
brew upgrade rdhpf
rdhpf version
```

The migration is expected to replace the formula with the same-named cask.
This is not an installation success claim: test the transition on the target
Homebrew/OS before depending on it. If automatic migration is unavailable,
inspect `brew list --formula` and `brew list --cask` first. With the old formula
still installed, the explicit fallback is:

```bash
brew uninstall --formula tomaszpeksa/tap/rdhpf
brew install --cask tomaszpeksa/tap/rdhpf
rdhpf version
```

This fallback removes the old installation before installing the new one; plan
for that interruption. Do not force-overwrite conflicting binaries or symlinks.
Update formula-specific Brewfile entries to cask entries. Caskroom/artifact
symlinks replace the formula's Cellar keg; the command remains `rdhpf` on PATH.
The former formula `test` block is replaced by an explicit `rdhpf version` smoke
check. The MIT license remains in the release archive, not cask license metadata.

### Normal OS Security

No signing/notarization or quarantine-removal hooks are configured. Normal OS
security remains in effect. On macOS, an unsigned binary may be blocked on first
launch even when its archive checksum is correct. Successful generation or a
local snapshot smoke test does not establish Gatekeeper acceptance of a downloaded
release. Stop and report a security block; this guide does not prescribe disabling
quarantine, bypassing Gatekeeper or changing global security settings.

### Verification and Removal

```bash
brew info --cask tomaszpeksa/tap/rdhpf
brew cat --cask tomaszpeksa/tap/rdhpf
rdhpf version
brew uninstall --cask tomaszpeksa/tap/rdhpf
# Optional, once no other packages from this tap are needed:
brew untap tomaszpeksa/tap
```

## Maintainer Migration

Coordinate the following as a separately reviewed tap change, not an automatic
side effect of changing this repository:

1. Validate the project configuration and render a nonpublishing snapshot.
2. At an authorized release, publish the four archives and generate
   `Casks/rdhpf.rb` pointing to that real release. Never publish snapshot URLs.
3. In the tap root, merge the following entry into `tap_migrations.json`, preserving
   any unrelated existing entries:
   ```json
   {"rdhpf": "rdhpf"}
   ```
4. Remove `Formula/rdhpf.rb` only when the cask and migration entry are available.
   Review the cask, metadata and formula removal together to avoid an empty tap
   or ambiguous same-named packages.
5. Validate fresh installation, formula-to-cask migration, binary PATH and
   `rdhpf version` on Linux and macOS. Record Homebrew versions and normal
   first-launch security behavior; generation alone is not this proof.

No tap publication or merge is implied by these instructions.

## Configuration and Validation

`.goreleaser.yml` remains the native configuration owner. The cask preserves the
existing repository, `HOMEBREW_TAP_TOKEN`, author and upload policy. Token access
remains limited to the existing tap-writing purpose; no new credential scopes
or workflow permissions are needed for this migration.

The essential configuration is:

```yaml
homebrew_casks:
  - name: rdhpf
    repository:
      owner: tomaszpeksa
      name: homebrew-tap
      token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"
    directory: Casks
    binaries: [rdhpf]
```

With the pinned project tools installed (see [CONTRIBUTING](../CONTRIBUTING.md)):

```bash
mise exec -- scripts/check-tools.sh go goreleaser
mise exec -- goreleaser check
mise exec -- goreleaser release --snapshot --clean --skip=publish
```

Run in an owned checkout/output directory with isolated HOME/cache and no
publication credentials. The snapshot skips release validation, so its exit
status cannot replace the separate `goreleaser check` result. Inspect the generated
cask under `dist/homebrew/`, verify its Linux/macOS and ARM/Intel URLs/checksums
against the four generated archives, and run the native binary's `version` command.
No Homebrew installation or tap access is needed for these rendering checks.

## Troubleshooting

- **Cask not found:** check that the reviewed tap transition was actually published,
  update Homebrew, and use the fully qualified `--cask` name. Do not assume the
  project configuration changed the remote tap.
- **Checksum mismatch:** stop and compare the published archive and cask checksum;
  report the mismatch rather than disabling verification.
- **Old formula or conflicting binary:** inspect installed formulae/casks and follow
  the migration procedure. Do not force-replace unrelated executables.
- **macOS launch blocked:** normal security is intentional; report the exact error.
  No automatic security bypass is provided.
- **Tap not updated after an authorized release:** inspect the release workflow's
  cask generation/publishing logs and existing tap-token permissions. Do not expose
  the token in reports or automatically broaden its scopes.

## Resources

- [GoReleaser Homebrew casks](https://goreleaser.com/customization/homebrew_casks/)
- [GoReleaser formula migration](https://goreleaser.com/deprecations/#brews)
- [Homebrew Cask Cookbook](https://docs.brew.sh/Cask-Cookbook)
- [Tap repository](https://github.com/tomaszpeksa/homebrew-tap)
- [User guide](user-guide.md)
- [CI/CD documentation](ci-cd.md)
