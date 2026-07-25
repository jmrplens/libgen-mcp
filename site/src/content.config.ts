import { defineCollection, z } from "astro:content";
import { docsLoader, i18nLoader } from "@astrojs/starlight/loaders";
import { docsSchema, i18nSchema } from "@astrojs/starlight/schema";

export const collections = {
	docs: defineCollection({
		loader: docsLoader(),
		// `datePublished` feeds the TechArticle node in src/components/Head.astro.
		// Git can only supply a last-modified date, so first publication is opt-in
		// per page (ISO 8601 date, e.g. "2026-07-19").
		schema: docsSchema({
			extend: z.object({ datePublished: z.string().optional() }),
		}),
	}),
	i18n: defineCollection({ loader: i18nLoader(), schema: i18nSchema() }),
};
