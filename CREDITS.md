# Credits

## Language support

The current Go extractor supports Go, Python, TypeScript, JavaScript, Java,
Rust, PostgreSQL SQL, C, and C++. Community contributions that helped shape
the structural language layer include:

- **@jhouserizer** (#53) and **@dbianco** (#83) — Java.
- **@qoole** (#59) — Rust; (#58) — PowerShell; (#40) — the tree-sitter 0.25 runtime bump.
- **@edrethardo** (#67) — C/C++ and the "fail loudly on unsupported languages" idea (#66).

Historical language contributions remain recorded in `CHANGELOG.md`. Grammars
and extraction branches for languages outside the current set were removed during
the Go cutover.
