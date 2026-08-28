const uiLibPreset = require("@metap/ui/tailwind-preset").default;

/** `@metap/ui/style.css` already ships the compiled Tailwind base/reset layer for its own CSS
 * variables (see that package's `build:css` script) — `preflight: false` here so this app's own
 * Tailwind pass only adds the `components`/`utilities` layers for classNames actually used in
 * this app's own pages and in `@metap/platform-ui`'s (which this app consumes as raw TS source,
 * not a pre-bundled package — see that package's README — so its utility classNames need this
 * app's own Tailwind content scan too, not just `@metap/ui`'s). */
module.exports = {
  presets: [uiLibPreset],
  corePlugins: { preflight: false },
  content: ["./index.html", "./src/**/*.{ts,tsx}", "../../../platform-ui/src/**/*.{ts,tsx}"],
};
