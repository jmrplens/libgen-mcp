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
			extend: z.object({
				datePublished: z.string().optional(),
				// The chip row under the page title. Qualities, never counts —
				// src/data/sources.ts owns the numbers, and a figure typed here
				// would be a second copy of one. Capped at four because the row
				// sits under the largest type on the page and a fifth pill turns
				// a qualifier into a paragraph.
				chips: z
					.array(z.object({ text: z.string(), href: z.string().optional() }))
					.max(4)
					.optional(),
			}),
		}),
	}),
	// The UI strings the documentation components supply for themselves. A label
	// that a component prints — "Corpus", "What it does not cover" — used to be
	// typed into the prose once per source per locale: 284 hand-written labels on
	// the sources page alone, which is 284 chances for two of them to disagree.
	i18n: defineCollection({
		loader: i18nLoader(),
		schema: i18nSchema({
			extend: z.object({
				"lgm.fact.keyedBy": z.string().optional(),
				"lgm.fact.corpus": z.string().optional(),
				"lgm.fact.resolves": z.string().optional(),
				"lgm.fact.notCovered": z.string().optional(),
				"lgm.fact.keys": z.string().optional(),
				"lgm.fact.politeness": z.string().optional(),
				"lgm.fact.symptom": z.string().optional(),
				"lgm.fact.meaning": z.string().optional(),
				"lgm.fact.fixes": z.string().optional(),
				"lgm.actions.copy": z.string().optional(),
				"lgm.actions.copied": z.string().optional(),
				"lgm.actions.failed": z.string().optional(),
				"lgm.actions.more": z.string().optional(),
				"lgm.actions.copyLink": z.string().optional(),
				"lgm.actions.viewMarkdown": z.string().optional(),
				"lgm.actions.openChatgpt": z.string().optional(),
				"lgm.actions.openClaude": z.string().optional(),
				"lgm.actions.prompt": z.string().optional(),
				"lgm.legal.notice": z.string().optional(),
				"lgm.schema.name": z.string().optional(),
				"lgm.schema.type": z.string().optional(),
				"lgm.schema.required": z.string().optional(),
				"lgm.schema.description": z.string().optional(),
			}),
		}),
	}),
};
