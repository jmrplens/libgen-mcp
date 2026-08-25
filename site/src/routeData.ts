/**
 * Starlight route middleware.
 *
 * One mutation, and it exists for the mobile layout rather than for the desktop
 * one. Starlight builds a `splash` page with `hasSidebar: false`, and it renders
 * the mobile menu button only when the route has a sidebar — so on a phone the
 * landing pages would be the only ones on the site with no way to reach the
 * navigation, and the 404 would be a dead end.
 *
 * Only the flag is touched. Starlight has already computed the full tree by
 * this point — the same three groups, translated for the locale — and simply
 * declines to show it. An earlier version of this file also assigned
 * `sidebar = []`, which threw that tree away and left the menu button opening
 * an empty drawer: a button that opens onto nothing is worse than no button.
 *
 * The desktop side is handled in CSS rather than here: splash-menu.css collapses
 * the navigation column on a page with a hero, so the landing keeps its full
 * width while the drawer behind the menu button stays populated.
 */
import { defineRouteMiddleware } from "@astrojs/starlight/route-data";

export const onRequest = defineRouteMiddleware((context) => {
	const route = context.locals.starlightRoute;
	if (route.entry?.data?.template === "splash") {
		route.hasSidebar = true;
	}
});
