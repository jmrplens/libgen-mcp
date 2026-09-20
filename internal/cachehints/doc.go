// Package cachehints stamps SEP-2549 cache hints onto the catalog results this
// server returns, so clients and intermediaries can stop re-fetching a listing
// that only ever changes with a release.
package cachehints
