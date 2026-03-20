# Arbiter Language for VS Code

Minimal editor support for `.arb` files:

- syntax highlighting
- `arbiter check` diagnostics on open/save
- `Arbiter: Check Current File` command
- comment toggling with `#`
- bracket pairing and folding
- starter snippets for rules, expert rules, segments, flags, and includes

## Local install

1. Install `vsce` if needed: `npm install -g @vscode/vsce`
2. Package the extension from this directory: `vsce package`
3. Install the generated `.vsix`: `code --install-extension arbiter-language-0.4.1.vsix`
4. Ensure the `arbiter` CLI is on `PATH`, or set `arbiter.cliPath` in VS Code settings

Diagnostics come from `arbiter check`, so included files report their real source paths and line numbers. This package is still intentionally small: it does not provide hover docs, completions, or live evaluation.
