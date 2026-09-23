// Package antibot tells an anti-bot interstitial from an ordinary refusal.
//
// Both arrive as a 403, and they call for opposite handling. An ordinary
// refusal is a host declining one request, and the next mirror or the next
// attempt may well answer. An interstitial is a policy in front of the whole
// host: it asks for a browser to run a script, this server is not one, and the
// same request is refused again a minute later. Retrying it spends time and
// requests against a host that has already said no.
//
// Search (internal/discovery) and download (internal/libgen) both meet the same
// interstitial on Anna's Archive, so the rule lives here once rather than in
// each of them, where two copies of a marker list would drift.
package antibot
