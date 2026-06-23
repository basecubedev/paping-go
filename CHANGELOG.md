# Changelog

## Unreleased

- Prepared project for initial public release.
- Added TCP reachability checks with CSV output.
- Added built-in CSV-to-HTML report generation.
- Added offline report visualization using an embedded template.
- Added localhost demo CSV and README report preview screenshot.
- Added CI, tests, Dependabot, and release build workflow.
- Build Linux release binaries as static pure-Go binaries to avoid GLIBC runtime compatibility issues on older distributions.
- Add a linux-amd64-legacy release artifact built with Go 1.23.x for very old Linux systems.
