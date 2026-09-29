# Third-party notices

This module redistributes the files below so that it is self-contained: no
Node.js headers and no `node-gyp` have to be installed to build it. They are
included unmodified.

## Node-API headers

- `include/node/node_api.h`
- `include/node/node_api_types.h`
- `include/node/js_native_api.h`
- `include/node/js_native_api_types.h`

## Windows import stub

- `windows/node.lib` — a symbol-only import library for the Node.js
  executable, used when linking on Windows.

## License

All of the files above originate from the Node.js project,
<https://github.com/nodejs/node>, and are distributed under the MIT license,
Copyright Node.js contributors. The license text is identical to this
project's [LICENSE](LICENSE).
