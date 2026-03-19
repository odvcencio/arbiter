# Arbiter Language for VS Code

Minimal editor support for `.arb` files:

- syntax highlighting
- comment toggling with `#`
- bracket pairing and folding
- starter snippets for rules, expert rules, segments, flags, and includes

## Local install

1. Install `vsce` if needed: `npm install -g @vscode/vsce`
2. Package the extension from this directory: `vsce package`
3. Install the generated `.vsix`: `code --install-extension arbiter-language-0.1.0.vsix`

This package is intentionally small. It does not provide semantic validation or live evaluation yet; those should layer on top of the Arbiter CLI and gRPC surfaces in a later slice.
