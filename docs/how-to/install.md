# Install ynr

Get `ynr` onto a machine and on its `PATH`. There are two builds ([The two builds](../explanation/the-two-builds.md)):
the slim build has no DuckDB, and the full build has it. Homebrew installs the full build. Each release
carries both builds as separate archives, and the full build can also be made from a clone.

## From Homebrew (full)

```bash
brew install eyelock/tap/ynr
ynr version
```

Expected:

```text
0.2.2 (full)
```

## From a release (slim or full)

Each release has two archives for each platform: `ynr_<version>_<os>_<arch>.tar.gz` is the slim
build (what ynh's base image and a tool's CI download) and `ynr-full_<version>_<os>_<arch>.tar.gz`
is the full build (what Homebrew installs). The slim name is stable. Each is an archive for `darwin` and `linux` on `amd64` and `arm64`; the release also has a `checksums.txt`
signed with cosign, and SBOMs. Download, check against the checksums, and unpack:

```bash
V=0.2.2
ynr="ynr-full_${V}_darwin_arm64.tar.gz"      # or ynr_${V}_... for the slim build
gh release download "v$V" -R eyelock/ynr -p "$ynr" -p checksums.txt
grep -E " ${ynr}\$" checksums.txt > wanted.txt
shasum -a 256 -c wanted.txt          # sha256sum --check on Linux
tar -xzf "$ynr" ynr
```

The binary inside either archive is called `ynr`. Move it to a folder on your `PATH`. To verify the signature on the checksum file as well:

```bash
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp 'https://github.com/eyelock/ynr/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
```

The stub vendor, `ynr-stub-vendor_${V}_<os>_<arch>.tar.gz`, is a separate archive from the same
release.

## From a clone (full)

You need Go at the version in `go.mod` and a C toolchain, because DuckDB is linked through cgo.

```bash
git clone https://github.com/eyelock/ynr
cd ynr
make install
export PATH="$HOME/.ynr/bin:$PATH"     # in your shell profile
ynr version
```

`make install` builds `bin/ynr-full` and copies it to `~/.ynr/bin/ynr`; `INSTALL_DIR=<dir>` puts it
elsewhere. To build without installing, `make full` makes `bin/ynr-full` and `make build` makes the
slim `bin/ynr`.

Expected, for the full build:

```text
v0.2.2 (full)
```

## From Go (slim)

```bash
go install github.com/eyelock/ynr/cmd/ynr@v0.2.2
```

This builds the slim build.

## Check what you have

`ynr version` ends with `(slim)` or `(full)`, and `ynr info --format json` reports the same as
`"build"`. `ynr doctor` also says whether the `ynr` on your `PATH` is the one you are running.
