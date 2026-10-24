// What the dashboard's scripts put on window for each other. Never served:
// tsc reads it (make typecheck-web), the browser never does.

/** How theme.js (a classic script, which modules cannot import from) lets the shell change the theme. */
interface FridolinTheme {
  /** The theme in effect: light, dark, or auto (the system's). */
  current(): string;
  /** Applies a theme (light, dark, anything else for auto) and saves it for the next visit. */
  set(theme: string): void;
}

interface Window {
  fridolinTheme: FridolinTheme;
}
