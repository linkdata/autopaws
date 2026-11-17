# Release procedure

The repository stays private. Creating its GitHub release does not change
repository visibility; downloads require an account/token with access.

1. Complete every required Linux core check in #43 and record actual hardware,
   host/target architecture/OS, browser versions, negotiated media settings,
   limits and optional coverage not run. Resolve failures in required/shared
   behavior; a new follow-up issue cannot waive them.
2. Validate the README installation path, device permissions and optional
   udev/service setup on the release setup. Preserve the CLI/audio/OCR/access
   documentation and link the canonical `alsa/README.md`
   WirePlumber instructions rather than copying another recipe. Have another
   person follow README to a working local or SSH-tunneled viewer.
3. Confirm that only production replacements and their retained ABI/fixture
   evidence remain; the feasibility prototypes have been removed.
4. Replace `docs/release-notes.md` with completed evidence and known limits,
   removing `RELEASE_VALIDATION_PENDING`. The release workflow refuses to publish
   while that marker remains. Keep runtime-tested and build-only platforms
   distinct. Include optional environments not run.
   Physical unplug/replug recovery and the available Firefox checks are still
   pending; do not infer a pass from unit tests or crossbuilds.
5. Run actionlint and the complete final README local CI gate. Run the eight
   release builds from a clean tree with a new output directory:

   ```sh
   bash .github/scripts/build-release.sh v0.1.0 dist
   ```

   Substitute the chosen version. Check eight binaries plus `SHA256SUMS`, the
   native KVM's `version` output, MCP's initialization `serverInfo.version`, and
   the checksums. Linux binaries should be static; macOS binaries still link
   `libSystem`. Cross-building the Windows/macOS/ARM64 artifacts is not a claim
   that those runtimes or hardware were exercised.
6. Finish two consecutive clean reviews after the final edits, push/merge the
   reviewed work and confirm CI. Only then create and push the selected `v*`
   release tag.

The tag workflow uses the Go version in `go.mod`, builds with `CGO_ENABLED=0` and
`-trimpath`, and injects the tag through `-X main.version=TAG` for both commands.
It publishes the eight raw binaries and `SHA256SUMS` using the job's own
`GITHUB_TOKEN` with `contents: write`. `gh release create --verify-tag` requires
the tag to exist. The reviewed validation notes precede GitHub's generated
change notes. Current GitHub CLI uploads the assets through a temporary draft
before publishing; inspect an incomplete draft if upload fails. A workflow rerun
does not overwrite an existing release silently: resolve that draft explicitly
before retrying.

The workflow is tag-triggered only. Test builds can run locally without release
permissions; never use a tag push as a substitute for local validation.

References: [GitHub CLI release creation](https://cli.github.com/manual/gh_release_create),
[authenticated asset downloads](https://cli.github.com/manual/gh_release_download).
