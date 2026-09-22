/// <reference types="astro/client" />

/**
 * Starlight's per-component virtual modules.
 *
 * A component override is supposed to reach its neighbours through
 * `virtual:starlight/components/<Name>` rather than through the package path,
 * so that a second override further down the chain still composes. Starlight
 * declares three of its virtual modules in `virtual.d.ts` — user-config,
 * plugin-translations and project-context — but not this family, and Astro does
 * not generate them into `.astro/types.d.ts` either, so `astro check` cannot
 * resolve the import that the documented pattern requires.
 *
 * Declaring it here keeps the documented import and satisfies the checker. The
 * props are `any` because each component in the family has a different shape
 * and none of them is exported; what this buys is module resolution, not
 * prop checking, and claiming otherwise would be worse than claiming nothing.
 */
declare module "virtual:starlight/components/*" {
	const component: (props: Record<string, unknown>) => unknown;
	export default component;
}

/**
 * This site's own UI strings, registered with Starlight's translation system.
 *
 * `Astro.locals.t` is typed against the keys Starlight knows about, so a key
 * this site adds is a type error until it is declared here. The key set is read
 * off `content/i18n/en.json` rather than restated: a list written twice drifts,
 * and the drift would be a label that renders as its own key name in one
 * language. English is the reference because `scripts/check-facts.mjs` already
 * reads that file, so a key removed there fails two checks rather than none.
 */
declare namespace StarlightApp {
	interface I18n extends Record<
		keyof typeof import("./content/i18n/en.json"),
		string
	> {}
}
