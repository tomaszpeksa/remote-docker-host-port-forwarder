# Homebrew Tap Guide

This document explains how the Homebrew tap for rdhpf works, how to maintain it, and how to troubleshoot common issues.

## Overview

The `tomaszpeksa/tap` Homebrew tap provides automated formula-based installation for rdhpf on macOS and Linux. When a new version is released, GoReleaser automatically:

1. Generates a Homebrew formula (`Formula/rdhpf.rb`)
2. Commits it to the `tomaszpeksa/homebrew-tap` repository
3. Makes the new version available via `brew install` or `brew upgrade`

## User Installation

### First Time Setup

```bash
# Add the tap
brew tap tomaszpeksa/tap

# Install rdhpf
brew install rdhpf
```

Or in a single command:

```bash
brew install tomaszpeksa/tap/rdhpf
```

### Upgrading

```bash
# Upgrade to the latest version
brew upgrade rdhpf

# Or upgrade all tapped formulae
brew upgrade
```

### Verification

```bash
# Check installed version
rdhpf version

# View formula info
brew info rdhpf

# View formula definition
brew cat rdhpf
```

### Uninstallation

```bash
# Uninstall rdhpf
brew uninstall rdhpf

# Remove the tap (optional)
brew untap tomaszpeksa/tap
```

## How It Works

### Automated Release Process

When you create a new Git tag (e.g., `v1.0.0`):

1. **GitHub Actions** triggers the release workflow (`.github/workflows/release.yml`)
2. **GoReleaser** builds binaries for all platforms
3. **GoReleaser** creates a GitHub Release with artifacts
4. **GoReleaser** generates the Homebrew formula:
   - Reads configuration from `.goreleaser.yml` → `brews` section
   - Downloads the macOS/Linux binaries
   - Calculates SHA256 checksums
   - Generates `Formula/rdhpf.rb` with proper metadata
5. **GoReleaser** commits the formula to `tomaszpeksa/homebrew-tap`
6. Users can now `brew install rdhpf` to get the latest version

### Formula Structure

The generated formula (`Formula/rdhpf.rb`) includes:

- **Metadata**: Name, description, homepage, license
- **Download URLs**: Pre-built binaries for macOS (amd64/arm64) and Linux (amd64/arm64)
- **SHA256 checksums**: Verify download integrity
- **Installation instructions**: Where to place the binary
- **Test instructions**: Verify installation with `rdhpf version`

Example formula structure:

```ruby
class Rdhpf < Formula
  desc "Automatically forward published container ports from a remote Docker host to localhost via SSH"
  homepage "https://github.com/tomaszpeksa/remote-docker-host-port-forwarder"
  version "1.0.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases/download/v1.0.0/rdhpf_1.0.0_Darwin_arm64.tar.gz"
      sha256 "..."
    end
    if Hardware::CPU.intel?
      url "https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases/download/v1.0.0/rdhpf_1.0.0_Darwin_x86_64.tar.gz"
      sha256 "..."
    end
  end

  on_linux do
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases/download/v1.0.0/rdhpf_1.0.0_Linux_arm64.tar.gz"
      sha256 "..."
    end
    if Hardware::CPU.intel?
      url "https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/releases/download/v1.0.0/rdhpf_1.0.0_Linux_x86_64.tar.gz"
      sha256 "..."
    end
  end

  def install
    bin.install "rdhpf"
  end

  test do
    system "#{bin}/rdhpf", "version"
  end
end
```

## Manual Formula Updates

While the formula is auto-generated, you may need to manually update it in rare cases.

### When Manual Updates Are Needed

- Emergency hotfix before automated release completes
- Formula template adjustments (caveats, post-install messages)
- Dependency changes
- Testing pre-release versions

### Steps for Manual Update

1. **Clone the tap repository**:
   ```bash
   git clone https://github.com/tomaszpeksa/homebrew-tap.git
   cd homebrew-tap
   ```

2. **Edit the formula**:
   ```bash
   vim Formula/rdhpf.rb
   ```

3. **Test locally**:
   ```bash
   brew install --build-from-source ./Formula/rdhpf.rb
   brew test rdhpf
   brew audit --strict rdhpf
   ```

4. **Commit and push**:
   ```bash
   git add Formula/rdhpf.rb
   git commit -m "Manual update: <description>"
   git push origin main
   ```

5. **Verify users can install**:
   ```bash
   brew update
   brew reinstall rdhpf
   ```

### Formula Best Practices

- Always include SHA256 checksums
- Keep descriptions concise (< 80 chars)
- Use semantic versioning
- Test on both macOS and Linux if possible
- Follow [Homebrew Formula Cookbook](https://docs.brew.sh/Formula-Cookbook)

## Troubleshooting

### Issue: Formula Not Found

**Symptom**:
```bash
$ brew install rdhpf
Error: No available formula with the name "rdhpf"
```

**Solutions**:

1. **Add the tap first**:
   ```bash
   brew tap tomaszpeksa/tap
   brew install rdhpf
   ```

2. **Use the full formula name**:
   ```bash
   brew install tomaszpeksa/tap/rdhpf
   ```

3. **Update Homebrew**:
   ```bash
   brew update
   ```

### Issue: Checksum Mismatch

**Symptom**:
```bash
Error: SHA256 mismatch
Expected: abc123...
  Actual: def456...
```

**Cause**: Downloaded file doesn't match expected checksum (corrupted download or formula error)

**Solutions**:

1. **Clear cache and retry**:
   ```bash
   rm -rf ~/Library/Caches/Homebrew/downloads/*rdhpf*
   brew install rdhpf
   ```

2. **If persistent, report the issue**: The formula may have incorrect checksums

### Issue: Old Version Installed

**Symptom**:
```bash
$ rdhpf version
v0.9.0  # Expected v1.0.0
```

**Solutions**:

1. **Update tap and upgrade**:
   ```bash
   brew update
   brew upgrade rdhpf
   ```

2. **Force reinstall**:
   ```bash
   brew reinstall rdhpf
   ```

3. **Check formula version**:
   ```bash
   brew info rdhpf
   ```

### Issue: Permission Denied

**Symptom**:
```bash
Error: Permission denied @ dir_s_mkdir - /usr/local/Cellar/rdhpf
```

**Solutions**:

1. **Fix Homebrew permissions**:
   ```bash
   sudo chown -R $(whoami) /usr/local/Cellar /usr/local/opt
   ```

2. **Or use user-local Homebrew** (recommended for non-admin users)

### Issue: Binary Not in PATH

**Symptom**:
```bash
$ rdhpf version
command not found: rdhpf
```

**Solutions**:

1. **Ensure Homebrew bin is in PATH**:
   ```bash
   # Add to ~/.zshrc or ~/.bashrc
   export PATH="/usr/local/bin:$PATH"
   # Or for Apple Silicon Macs:
   export PATH="/opt/homebrew/bin:$PATH"
   ```

2. **Reload shell**:
   ```bash
   source ~/.zshrc  # or ~/.bashrc
   ```

3. **Verify installation**:
   ```bash
   which rdhpf
   brew list rdhpf
   ```

### Issue: Release Not Published to Tap

**Symptom**: GitHub Release created, but Homebrew formula not updated

**Debugging Steps**:

1. **Check GitHub Actions logs**:
   - Go to: https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/actions
   - Look for the release workflow run
   - Review GoReleaser step output

2. **Verify `HOMEBREW_TAP_TOKEN` secret**:
   - Go to repository Settings → Secrets → Actions
   - Confirm `HOMEBREW_TAP_TOKEN` exists and is valid
   - Token needs `repo` + `workflow` scopes

3. **Check tap repository**:
   - Visit: https://github.com/tomaszpeksa/homebrew-tap
   - Verify `Formula/rdhpf.rb` was updated
   - Check commit history for automation failures

4. **Manual formula push** (if automation failed):
   - Download release artifacts
   - Calculate checksums: `shasum -a 256 rdhpf_*.tar.gz`
   - Manually update `Formula/rdhpf.rb` in tap repository
   - Commit and push

## Configuration

### GoReleaser Configuration

The Homebrew tap is configured in `.goreleaser.yml`:

```yaml
brews:
  - repository:
      owner: tomaszpeksa
      name: homebrew-tap
      token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"
    directory: Formula
    homepage: "https://github.com/tomaszpeksa/remote-docker-host-port-forwarder"
    description: "Automatically forward published container ports from a remote Docker host to localhost via SSH"
    license: "MIT"
    skip_upload: false
    install: |
      bin.install "rdhpf"
    test: |
      system "#{bin}/rdhpf", "version"
```

### GitHub Actions Secret

The `HOMEBREW_TAP_TOKEN` secret must be added to the rdhpf repository:

1. **Generate token**: https://github.com/settings/tokens/new
   - Scopes: `repo`, `workflow`
   - Expiration: 1 year (or no expiration)

2. **Add to repository**:
   - Go to: https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/settings/secrets/actions
   - Name: `HOMEBREW_TAP_TOKEN`
   - Value: `ghp_...` (token from step 1)

## Best Practices

### For Maintainers

1. **Always test releases locally first**:
   ```bash
   goreleaser release --snapshot --clean
   ```

2. **Verify formula generation** before pushing tags:
   ```bash
   ls dist/*.rb
   cat dist/homebrew/Formula/rdhpf.rb
   ```

3. **Monitor release workflow** after pushing tags

4. **Test installation** after release:
   ```bash
   brew update
   brew reinstall rdhpf
   rdhpf version
   ```

### For Users

1. **Keep tap updated**:
   ```bash
   brew update  # Run regularly
   ```

2. **Use specific versions** when needed:
   ```bash
   brew info rdhpf  # See available versions
   ```

3. **Report issues** with formula to:
   - Tap issues: https://github.com/tomaszpeksa/homebrew-tap/issues
   - App issues: https://github.com/tomaszpeksa/remote-docker-host-port-forwarder/issues

## Resources

- **Homebrew Documentation**: https://docs.brew.sh
- **GoReleaser Brew Documentation**: https://goreleaser.com/customization/homebrew/
- **Formula Cookbook**: https://docs.brew.sh/Formula-Cookbook
- **Tap Repository**: https://github.com/tomaszpeksa/homebrew-tap
- **Main Repository**: https://github.com/tomaszpeksa/remote-docker-host-port-forwarder

## See Also

- Main README: ../README.md
- User Guide: ./user-guide.md
- Release Checklist: ./RELEASE_CHECKLIST.md
- CI/CD Documentation: ./ci-cd.md