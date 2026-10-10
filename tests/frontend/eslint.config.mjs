// Closes the class of bug jsdom suites cannot see: a name used in a module without being imported
// or declared (e.g. escapeAttr in changed_files.js) is a ReferenceError only when that code runs.
// no-undef is the only rule enabled; nothing here is about style.
import globals from "globals";

export default [
  { ignores: ["static/vendor/**"] }, // third-party bundles, not ours to lint
  {
    files: ["static/**/*.js"], // relative to coral-go/pkg/frontend, where the lint script runs
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: "module",
      globals: {
        ...globals.browser,
        // Libraries loaded by <script> tags before the modules run (see templates/): they are
        // globals by design. Anything else undefined is a bug.
        marked: "readonly", DOMPurify: "readonly", hljs: "readonly",
        Terminal: "readonly", FitAddon: "readonly", WebLinksAddon: "readonly", UnicodeGraphemesAddon: "readonly", CanvasAddon: "readonly",
      },
    },
    linterOptions: { reportUnusedDisableDirectives: "off" },
    rules: { "no-undef": "error" },
  },
];
