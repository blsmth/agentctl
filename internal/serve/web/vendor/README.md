# Vendored third-party assets

No npm, no CDN: these files are copied verbatim from the published npm tarballs and embedded in the binary.

| File | Package | Version | License |
|---|---|---|---|
| `xterm.js`, `xterm.css` | `@xterm/xterm` (`lib/xterm.js`, `css/xterm.css`) | 6.0.0 | MIT, `LICENSE-xterm` |
| `addon-fit.js` | `@xterm/addon-fit` (`lib/addon-fit.js`) | 0.11.0 | MIT, `LICENSE-addon-fit` |
| `pdfjs/pdf.min.mjs`, `pdfjs/pdf.worker.min.mjs` | `pdfjs-dist` (`build/`) | 6.5.438 | Apache-2.0, `pdfjs/LICENSE` |

PDF.js is the artifact viewer's PDF renderer (iOS's own viewer shows only the first page inside an iframe). Source: https://registry.npmjs.org/pdfjs-dist/-/pdfjs-dist-6.5.438.tgz. The modern build is used (iOS 17+ and current desktop browsers); the `legacy` build only adds polyfills for older engines. These are ES modules, loaded only by a dynamic `import()` the first time the viewer opens; no CDN at runtime.

The xterm files are the minified UMD builds; loaded with classic `<script>` tags they expose the globals `Terminal` and `FitAddon.FitAddon`.

To upgrade: download the new tarballs from registry.npmjs.org into an empty directory, copy the same files over, and update the versions above.
