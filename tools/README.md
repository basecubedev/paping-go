# Tools

## Standalone CSV viewer

`viewer.html` is an optional browser-based CSV viewer for advanced users.

It loads `paping-go` CSV files locally in the browser. No data is uploaded.

For normal usage, prefer:

```bash
paping-go report results.csv -o report.html
```

The viewer uses the locally vendored Plotly.js file in `tools/vendor/plotly.min.js`, so it works offline from a repository checkout. The bundled Plotly license is stored next to it in `tools/vendor/plotly.min.js.LICENSE.txt`.

It also provides display toggles for the latency line, data points, and failed checks. Its filter controls which CSV rows are included, while the display controls choose how those rows are drawn.

To update the vendored Plotly file:

```bash
npm install
npm run vendor
```

## Third-party code

`viewer.html` uses a locally vendored Plotly.js browser bundle for chart rendering. Plotly.js is licensed under the MIT License.

The viewer is optional and is not part of the `paping-go` binary.

## Source archive helper

`make-source-archive.sh` creates a clean source tarball with `git archive` and checks that common working-tree artifacts are absent:

```bash
tools/make-source-archive.sh HEAD paping-go.tar.gz
```
