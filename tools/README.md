# Tools

## Standalone CSV viewer

`viewer.html` is an optional browser-based CSV viewer for advanced users.

It loads `paping-go` CSV files locally in the browser. No data is uploaded.

For normal usage, prefer:

```bash
paping-go report results.csv -o report.html
```

## Third-party code

`viewer.html` loads Plotly.js from a CDN for chart rendering. Plotly.js is licensed under the MIT License.

The viewer is optional and is not part of the `paping-go` binary.
