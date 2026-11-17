# Vendored third-party assets

No npm, no CDN: these files are copied verbatim from the published npm tarballs and embedded in the binary.

| File | Package | Version | License |
|---|---|---|---|
| `xterm.js`, `xterm.css` | `@xterm/xterm` (`lib/xterm.js`, `css/xterm.css`) | 6.0.0 | MIT, `LICENSE-xterm` |
| `addon-fit.js` | `@xterm/addon-fit` (`lib/addon-fit.js`) | 0.11.0 | MIT, `LICENSE-addon-fit` |

Both JS files are the minified UMD builds; loaded with classic `<script>` tags they expose the globals `Terminal` and `FitAddon.FitAddon`.

To upgrade: download the new tarballs from registry.npmjs.org into an empty directory, copy the same files over, and update the versions above.
