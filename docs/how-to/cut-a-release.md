# Cut a release

Releases follow Gitflow ([CONTRIBUTING.md](https://github.com/eyelock/ynr/blob/develop/CONTRIBUTING.md)):
changes land on `develop` through feature pull requests, and a pull request carries them to `main`,
where the tag is cut. `main` takes pull requests only from `develop`, `release/*` and `hotfix/*`.

1. **Make sure `develop` is what you want to release.** Its CI is green, and `make check` passes on
   it locally:

   ```bash
   git switch develop && git pull
   make check
   ```

   `make check` takes a few minutes: formatting, generated templates, vet, lint, the slim-build
   guard, the npm exporter's build and tests, and the Go tests with the race detector, each for both
   builds.

2. **Open a pull request into `main`,** from `develop`, or from `release/vX.Y.Z` cut from it if
   the release needs a change of its own. Put the release notes in the description. The checks
   **All Clear** and **Verify PR source branch** must pass. Merge it with **Create a merge commit**
   (not squash), so the back-merge into `develop` is clean. GitHub deletes a `release/*` branch when
   its pull request merges.

3. **Tag `vX.Y.Z` on `main` and push the tag.**

   ```bash
   git switch main && git pull
   git tag -a vX.Y.Z -m vX.Y.Z
   git push origin vX.Y.Z
   ```

   The `release` workflow then runs on the tag:

   - **check:** runs `make check` again on the tag;
   - **release:** GoReleaser builds the slim `ynr` and `ynr-stub-vendor` for macOS and Linux on
     `amd64` and `arm64`, writes `checksums.txt`, signs it with cosign (keyless, so the signature is
     bound to the workflow), attaches SBOMs, publishes the GitHub release, and pushes
     `Formula/ynr.rb` to `eyelock/homebrew-tap` with `RELEASE_TOKEN`.

4. **Check what it published.** The release has two archives for each of the four platforms, their
   SBOMs, `checksums.txt` and `checksums.txt.sigstore.json`. Verify the signature:

   ```bash
   cosign verify-blob --bundle checksums.txt.sigstore.json \
     --certificate-identity-regexp 'https://github.com/eyelock/ynr/' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
   ```

   Check the formula in `eyelock/homebrew-tap`, and `brew install eyelock/tap/ynr && ynr version`.
   GoReleaser writes a commit list as the release's description; replace it with the release notes:

   ```bash
   gh release edit vX.Y.Z --notes-file <notes>
   ```

5. **Back-merge into `develop`** once the release works, with a pull request from `main` into
   `develop`, merged with **Create a merge commit**.

The version is the tag: `ynr version` prints it, and a build from anything but a clean tag says
`<tag>-<commits>-g<sha>`, with `-dirty` if the tree has changes.

## The spool exporters

The Go and npm spool exporters are released separately, by tags of the form `spoolexporter/vX.Y.Z`;
a `v*` tag releases ynr and a `spoolexporter/*` tag does not. Pushing the tag versions the Go
module, and the `publish-spoolexporter` workflow publishes `@eyelock/otel-spool-exporter` to GitHub
Packages at the same version. After releasing one, raise the `spoolexporter` requirement in ynr's
`go.mod`, because `go install github.com/eyelock/ynr/cmd/ynr@<version>` builds against that.

## A hotfix

Branch `hotfix/<what>` from the release tag, fix, and open a pull request into `main`. Merge it with
**Create a merge commit**, tag the next patch version on `main`, and back-merge `main` into `develop`
once that release works.

## The docs site

The site at https://eyelock.github.io/ynr/ is built from `/docs` on `main`, so the published docs are
the released ones; changes on `develop` appear with the next release.
