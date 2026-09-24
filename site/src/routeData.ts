/**
 * Starlight route middleware.
 *
 * Every change here is made to the route's data rather than rendered beside
 * Starlight's own output, because Starlight dedupes `<head>` entries by name and
 * property only inside its own pipeline: a tag written after <Default /> in
 * Head.astro would sit next to Starlight's instead of replacing it.
 *
 * 1. The mobile menu on the splash pages. Starlight builds a `splash` page with
 *    `hasSidebar: false`, and it renders the mobile menu button only when the
 *    route has a sidebar — so on a phone the landing pages would be the only ones
 *    on the site with no way to reach the navigation, and the 404 would be a dead
 *    end. Only the flag is touched. Starlight has already computed the full tree
 *    by this point and simply declines to show it; an earlier version of this
 *    file also assigned `sidebar = []`, which left the menu button opening an
 *    empty drawer. The desktop side is handled in CSS: splash-menu.css collapses
 *    the navigation column on a page with a hero.
 *
 * 2. The share titles. Starlight's og:title is the bare page title ("Tools"),
 *    which is what a link preview or an engine summarising the page shows, with
 *    nothing naming the product. They now carry the document's full <title>.
 *
 * 3. The page's Markdown copy. scripts/emit-page-markdown.mjs writes one beside
 *    every page (`/tools/index.md`), and nothing in the page said so; a
 *    `rel="alternate"` link is how an agent that reads Markdown finds it.
 *
 * 4. The 404 page's canonical and hreflang links. Starlight gives it both, and
 *    both name `/404/` and `/es/404/`, which answer 404: a canonical pointing at
 *    a URL that does not exist is a claim no crawler can honour.
 */
import {
	defineRouteMiddleware,
	type StarlightRouteData,
} from "@astrojs/starlight/route-data";

type HeadEntry = StarlightRouteData["head"][number];

/** Sets the content of the meta tag keyed by `key`, adding it if absent. */
function setMeta(
	head: HeadEntry[],
	key: "property" | "name",
	value: string,
	content: string,
) {
	const entry = head.find((e) => e.tag === "meta" && e.attrs?.[key] === value);
	if (entry?.attrs) entry.attrs.content = content;
	else head.push({ tag: "meta", attrs: { [key]: value, content } });
}

export const onRequest = defineRouteMiddleware((context) => {
	const route = context.locals.starlightRoute;
	if (route.entry?.data?.template === "splash") {
		route.hasSidebar = true;
	}

	const head = route.head;
	if (/\/404\/?$/.test(context.url.pathname)) {
		route.head = head.filter(
			(e) =>
				!(
					e.tag === "link" &&
					(e.attrs?.rel === "canonical" ||
						(e.attrs?.rel === "alternate" && e.attrs?.hreflang))
				),
		);
		return;
	}

	// The last <title> wins, as it does in Starlight's own dedupe: a page that
	// overrides its title in frontmatter `head` appends one after the default.
	const documentTitle = head.findLast((e) => e.tag === "title")?.content;
	if (documentTitle) {
		setMeta(head, "property", "og:title", documentTitle);
		setMeta(head, "name", "twitter:title", documentTitle);
	}

	const path = context.url.pathname.endsWith("/")
		? context.url.pathname
		: `${context.url.pathname}/`;
	head.push({
		tag: "link",
		attrs: { rel: "alternate", type: "text/markdown", href: `${path}index.md` },
	});
});
