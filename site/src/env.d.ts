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
