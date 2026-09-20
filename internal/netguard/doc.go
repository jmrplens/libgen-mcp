// Package netguard builds HTTP clients that refuse to connect to addresses only
// this machine can reach.
//
// Every download source and every discovery provider fetches a URL it was handed
// by a third party: a publisher's link deposited with Crossref, a repository's
// download URL republished by Unpaywall, OpenAlex or CORE, a citation_pdf_url
// scraped off a publisher page. Nothing about that pipeline stops one of those
// URLs from naming the loopback interface, the operator's LAN, or a cloud
// instance-metadata endpoint — and if it does, the server becomes a proxy into a
// network the depositor could never reach directly. That is server-side request
// forgery (CWE-918), and this package is where it is stopped.
//
// The check runs in the dialer's Control hook rather than on the URL, because
// only the dialer sees what was actually resolved. A URL check is defeated by a
// public hostname with a private A record (localtest.me resolves to 127.0.0.1)
// and by DNS rebinding, where the name resolves differently between validation
// and connection; the Control hook is handed the concrete IP microseconds before
// connect, so neither is possible. Installing it on the shared Transport also
// means every source, probe, mirror lookup and redirect hop inherits it at once,
// rather than each caller having to remember.
package netguard
