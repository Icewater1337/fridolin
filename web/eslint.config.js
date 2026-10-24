// ESLint for the dashboard (`make lint-web`), run through npx at the version
// the Makefile pins: nothing is installed, so this config can import no
// plugin or shared config and names its rules itself. They catch what a
// browser would only show once that code runs: a name that is not defined
// (a typo, a missing import), code that is never used, and mistakes no
// test may happen to reach.

// The browser's globals the modules use, and node's the tests use. A new
// one goes here once it is really meant.
const common = [
  'console', 'setTimeout', 'clearTimeout', 'setInterval', 'structuredClone',
  'URL', 'URLSearchParams', 'AbortController', 'AbortSignal', 'DOMException', 'Event', 'fetch',
  'TextDecoder', 'TextEncoder', 'atob', 'btoa', 'FormData', 'File',
];
const browser = [...common, 'window', 'document', 'location', 'navigator', 'localStorage', 'Node'];
// The tests also have the fake DOM's document (installDom), as the page has,
// and build their stand-in fetch's responses with Headers.
const node = [...common, 'setImmediate', 'document', 'Headers'];
const globals = (names) => Object.fromEntries(names.map((n) => [n, 'readonly']));

const rules = {
  'no-undef': 'error',
  // An unused catch binding is how `catch (e) { /* ignore */ }` reads in
  // the classic theme.js, which predates optional catch bindings.
  'no-unused-vars': ['error', { args: 'after-used', caughtErrors: 'none' }],
  'no-unused-private-class-members': 'error',
  'no-unreachable': 'error',
  'no-unsafe-finally': 'error',
  'no-unsafe-negation': 'error',
  'no-dupe-keys': 'error',
  'no-dupe-args': 'error',
  'no-dupe-else-if': 'error',
  'no-duplicate-case': 'error',
  'no-duplicate-imports': 'error',
  'no-self-assign': 'error',
  'no-self-compare': 'error',
  'no-const-assign': 'error',
  'no-import-assign': 'error',
  'no-func-assign': 'error',
  'no-redeclare': 'error',
  'no-shadow-restricted-names': 'error',
  // A const read before its line in the same scope throws; a function
  // that reads it later, once it is set, does not.
  'no-use-before-define': ['error', { functions: false, classes: false, variables: false }],
  'no-unmodified-loop-condition': 'error',
  'no-constant-binary-expression': 'error',
  'no-cond-assign': 'error',
  'no-fallthrough': 'error',
  'no-sparse-arrays': 'error',
  'use-isnan': 'error',
  'valid-typeof': 'error',
  eqeqeq: 'error',
};

export default [
  { ignores: ['**/node_modules/'] },
  {
    files: ['assets/**/*.js'],
    languageOptions: { ecmaVersion: 'latest', sourceType: 'module', globals: globals(browser) },
    rules,
  },
  // Runs before the modules, as a classic script.
  { files: ['assets/theme.js'], languageOptions: { sourceType: 'script' } },
  {
    files: ['test/**/*.js', 'eslint.config.js'],
    languageOptions: { ecmaVersion: 'latest', sourceType: 'module', globals: globals(node) },
    rules,
  },
];
