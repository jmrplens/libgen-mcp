// Command audit_install_buttons checks the one-click install buttons against
// what the page around them claims.
//
// A button's configuration travels inside its URL: percent-encoded JSON for VS
// Code, its Insiders build and Kiro, base64 for Cursor and LM Studio. None of
// it is readable in review, and no text search reaches it — an argument inside
// a button does not appear as those characters anywhere in the file, so a sweep
// that fixes the prose leaves every button exactly as it was, and the README
// then documents one command while installing another.
//
// So this audit decodes rather than searches, and holds the buttons to the
// promise the page makes about them: that every button registers the same
// configuration. They are grouped by the command they launch, because a Docker
// button and an npx button are different configurations on purpose, and within
// a group the arguments have to agree.
//
// It fails rather than passing when it finds nothing at all: an audit that
// quietly matched no button would report a clean run over a page it never read.
package main
