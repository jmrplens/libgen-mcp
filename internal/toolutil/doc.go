// Package toolutil holds small pieces of tool-registration infrastructure
// shared across the tool and prompt surfaces. Today that is only the icon set
// below; it exists as its own package so cmd/server, internal/tools and
// internal/prompts can all reach it without an import cycle.
package toolutil
