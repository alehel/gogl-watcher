# Releasing

Releases are cut by tagging a commit on `main`:

```bash
git checkout main && git pull
git tag v1.2.3
git push origin v1.2.3
```

The `Release` workflow (`.github/workflows/release.yml`) refuses tags whose commit is not on
`main`, runs the test suite, builds the multi-arch image with the tag as the embedded version,
pushes it to `ghcr.io/alehel/gogl-watcher` and creates a GitHub release with generated notes.
A tag containing a hyphen (`v1.3.0-rc.1`) is published as a pre-release; it only gets its
exact tag and never moves `latest`. Release tags `1.2.3`, `1.2`, `1` and `latest` all point at
the newest release in their range.
