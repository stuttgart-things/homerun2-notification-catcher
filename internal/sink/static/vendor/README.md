# Vendored page assets

Served by the sink under `<BASE_PATH>/static/vendor/` so the HTML view works
in clusters without internet access. Update by replacing the file and the
version in its name (and in `internal/sink/index.html`).

| File | Source | License |
|---|---|---|
| `pico-2.1.1.min.css` | npm `@picocss/pico@2.1.1`, `css/pico.min.css` | MIT, `LICENSE-pico.md` |
| `adaptivecards-3.0.4.min.js` | npm `adaptivecards@3.0.4`, `dist/adaptivecards.min.js` | MIT, `LICENSE-adaptivecards.txt` |
| `press-start-2p-latin.woff2`, `fonts.css` | Google Fonts, Press Start 2P v16, latin subset | SIL OFL 1.1, `LICENSE-press-start-2p.txt` |

The npm tarballs were checked against the registry's `dist.integrity`.
