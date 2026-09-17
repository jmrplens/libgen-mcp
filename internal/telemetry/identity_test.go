package telemetry

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// attrMap renders an attribute slice as a map, for a comparison that does not
// depend on the order they were appended in.
func attrMap(t *testing.T, attrs []attribute.KeyValue) map[string]string {
	t.Helper()

	got := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		key := string(attr.Key)
		if _, seen := got[key]; seen {
			t.Errorf("attribute %q appears twice", key)
		}
		got[key] = attr.Value.String()
	}
	return got
}

// testKeyring builds a keyring from a fixed secret, so a digest is reproducible
// within a test without being reproducible outside one.
func testKeyring(t *testing.T) *Keyring {
	t.Helper()

	keys, err := NewKeyring("a-fixed-test-secret-for-derivation", 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return keys
}

// TestParseIdentityPolicy covers the values an operator may write and the one
// they may not.
func TestParseIdentityPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value string
		want  IdentityPolicy
		bad   bool
	}{
		{name: "unset is none, which is the default this server promises", value: "", want: IdentityNone},
		{name: "none", value: "none", want: IdentityNone},
		{name: "pseudonymous", value: "pseudonymous", want: IdentityPseudonymous},
		{name: "full", value: "full", want: IdentityFull},
		{name: "spaced and cased as a person writes it", value: " Pseudonymous ", want: IdentityPseudonymous},
		{name: "a value nobody defined", value: "anonymous", bad: true},
		// The dangerous misreading: "true" is not a policy, and accepting it as
		// one would have to mean something, which would be a guess about how much
		// to export.
		{name: "a boolean is not a policy", value: "true", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseIdentityPolicy(tc.value)
			if tc.bad {
				if err == nil {
					t.Fatalf("ParseIdentityPolicy(%q) = %q, nil, want a refusal", tc.value, got)
				}
				for _, name := range []string{string(IdentityNone), string(IdentityPseudonymous), string(IdentityFull)} {
					if !strings.Contains(err.Error(), name) {
						t.Errorf("the refusal does not name %q, so it does not say what to write instead: %v", name, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIdentityPolicy(%q) error = %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("ParseIdentityPolicy(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestAttributesPerPolicy is the whole of what each policy exports, stated as
// the set rather than as a presence check.
//
// A presence check is what lets a fourth attribute appear beside the three that
// were asserted, which is how an identity leaks under a policy that forbids it.
func TestAttributesPerPolicy(t *testing.T) {
	keys := testKeyring(t)
	caller := Caller{
		Address:       "203.0.113.7",
		ClientName:    "claude-code",
		ClientVersion: "1.4.0",
	}

	t.Run("none exports nothing at all", func(t *testing.T) {
		got := NewRedactor(IdentityNone, keys).Attributes(caller)
		if len(got) != 0 {
			t.Errorf("the none policy exported %v", attrMap(t, got))
		}
	})

	t.Run("pseudonymous exports one digest and no address", func(t *testing.T) {
		got := attrMap(t, NewRedactor(IdentityPseudonymous, keys).Attributes(caller))
		if len(got) != 1 {
			t.Fatalf("the pseudonymous policy exported %v, want only %s", got, AttrUserHash)
		}
		digest := got[AttrUserHash]
		if len(digest) != 16 {
			t.Errorf("%s = %q, want sixteen hex characters", AttrUserHash, digest)
		}
		if strings.Contains(digest, caller.Address) {
			t.Errorf("the address survived into the digest: %q", digest)
		}
	})

	t.Run("full exports the address and the client", func(t *testing.T) {
		got := attrMap(t, NewRedactor(IdentityFull, keys).Attributes(caller))
		want := map[string]string{
			AttrClientAddress:    "203.0.113.7",
			AttrMCPClientName:    "claude-code",
			AttrMCPClientVersion: "1.4.0",
		}
		for key, value := range want {
			if got[key] != value {
				t.Errorf("%s = %q, want %q", key, got[key], value)
			}
		}
		if len(got) != len(want) {
			t.Errorf("the full policy exported %v, want exactly %v", got, want)
		}
		// Not user.id: this server authenticates nobody, and putting an address
		// there would tell a backend that a person was identified when an
		// address was observed.
		if _, named := got["user.id"]; named {
			t.Error("the full policy exported user.id; there is no user here to name")
		}
	})
}

// TestAttributesOnStdioAreNothingUnderEveryPolicy covers the transport where
// there is no caller to tell apart.
//
// One process, one person, started by them. A policy that emitted a digest of
// the empty string there would publish a constant that looks like an identity
// and means "nobody", which is worse than silence in a backend that groups by it.
func TestAttributesOnStdioAreNothingUnderEveryPolicy(t *testing.T) {
	keys := testKeyring(t)

	for _, policy := range []IdentityPolicy{IdentityNone, IdentityPseudonymous, IdentityFull} {
		t.Run(string(policy), func(t *testing.T) {
			// The client name is set and the address is not, which is exactly
			// what a stdio session knows: initialize still names the client.
			got := NewRedactor(policy, keys).Attributes(Caller{ClientName: "claude-desktop"})
			if len(got) != 0 {
				t.Errorf("a caller with no address exported %v", attrMap(t, got))
			}
		})
	}
}

// TestPseudonymTellsCallersApartAndIsStable is the property the pseudonymous
// policy exists for, and the one it would be useless without.
func TestPseudonymTellsCallersApartAndIsStable(t *testing.T) {
	redactor := NewRedactor(IdentityPseudonymous, testKeyring(t))
	digest := func(caller Caller) string {
		attrs := redactor.Attributes(caller)
		if len(attrs) != 1 {
			t.Fatalf("Attributes(%+v) = %v, want one digest", caller, attrs)
		}
		return attrs[0].Value.String()
	}

	first := digest(Caller{Address: "203.0.113.7"})
	if again := digest(Caller{Address: "203.0.113.7"}); again != first {
		t.Errorf("one address produced two digests, %q and %q: a burst would read as two callers", first, again)
	}
	if other := digest(Caller{Address: "203.0.113.8"}); other == first {
		t.Errorf("two addresses produced one digest %q: the policy distinguishes nobody", first)
	}
	// A session joins the address when there is one, which is the only thing
	// that tells two sessions from one address apart under --stateless=false.
	withSession := digest(Caller{Address: "203.0.113.7", Session: "s-1"})
	if withSession == first {
		t.Error("a session made no difference to the digest")
	}
	if second := digest(Caller{Address: "203.0.113.7", Session: "s-2"}); second == withSession {
		t.Error("two sessions from one address produced one digest")
	}
}

// TestPseudonymIsAConstantWhenEveryCallerLooksTheSame is the configuration
// symptom, pinned so it is documented rather than discovered.
//
// Behind a proxy without --trusted-proxies and --trusted-proxy-header, every
// caller is charged to the proxy's own address, so user.hash is one value for
// the whole deployment. That is not the policy working unusually well — it is
// the identity underneath it being absent — and a reader who sees one digest for
// everybody should reach for those two flags.
func TestPseudonymIsAConstantWhenEveryCallerLooksTheSame(t *testing.T) {
	redactor := NewRedactor(IdentityPseudonymous, testKeyring(t))

	// What clientIP returns on a wildcard-bound container behind a bridge when
	// no proxy is trusted: the gateway, for everybody.
	const gateway = "172.19.0.1"
	first := redactor.Attributes(Caller{Address: gateway})
	second := redactor.Attributes(Caller{Address: gateway})

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("Attributes = %v and %v, want one digest each", first, second)
	}
	if first[0].Value.String() != second[0].Value.String() {
		t.Fatal("two calls charged to one address produced two digests")
	}
	// And the same redactor does tell real addresses apart, so the constant
	// above is the input and not the policy.
	if distinct := redactor.Attributes(Caller{Address: "203.0.113.7"}); distinct[0].Value.String() == first[0].Value.String() {
		t.Error("a different address produced the same digest")
	}
}

// TestAttributesWithoutAKeyringExportNothingUnderPseudonymous is the honest
// answer to a broken keyring.
//
// An empty user.hash would read as one anonymous caller while meaning "the
// keyring is broken", and a backend grouping by it would merge the whole
// deployment into a single row with nothing to say why.
func TestAttributesWithoutAKeyringExportNothingUnderPseudonymous(t *testing.T) {
	t.Parallel()

	got := NewRedactor(IdentityPseudonymous, nil).Attributes(Caller{Address: "203.0.113.7"})
	if len(got) != 0 {
		t.Errorf("a redactor with no keyring exported %v", attrMap(t, got))
	}
	// The zero value redacts everything, so a caller that never configured one
	// cannot leak by forgetting.
	var unset *Redactor
	if leaked := unset.Attributes(Caller{Address: "203.0.113.7"}); len(leaked) != 0 {
		t.Errorf("a nil redactor exported %v", attrMap(t, leaked))
	}
	if unset.Policy() != DefaultIdentityPolicy {
		t.Errorf("a nil redactor reports policy %q, want the default %q", unset.Policy(), DefaultIdentityPolicy)
	}
}

// TestItemAttributesPerPolicy pins the one place this server's treatment differs
// from the identity treatment on purpose: the digest is the floor rather than
// nothing.
func TestItemAttributesPerPolicy(t *testing.T) {
	keys := testKeyring(t)
	const md5 = "9f2b7c1e4a5d6083f1c2b3a4d5e6f708"

	for _, policy := range []IdentityPolicy{IdentityNone, IdentityPseudonymous} {
		t.Run(string(policy)+" digests the identifier", func(t *testing.T) {
			got := attrMap(t, NewItemRedactor(policy, keys).ItemAttributes(md5))
			digest := got[AttrItemRef]
			if len(got) != 1 || digest == "" {
				t.Fatalf("ItemAttributes = %v, want only %s", got, AttrItemRef)
			}
			if strings.Contains(digest, md5) || digest == md5 {
				t.Errorf("%s = %q, which still carries the identifier", AttrItemRef, digest)
			}
		})
	}

	t.Run("full records the identifier itself", func(t *testing.T) {
		got := attrMap(t, NewItemRedactor(IdentityFull, keys).ItemAttributes(md5))
		if got[AttrItemID] != md5 {
			t.Errorf("%s = %q, want the identifier", AttrItemID, got[AttrItemID])
		}
		if _, digested := got[AttrItemRef]; digested {
			t.Errorf("the full policy emitted both the identifier and a digest: %v", got)
		}
	})

	t.Run("an absent identifier is absent, not a digest of nothing", func(t *testing.T) {
		if got := NewItemRedactor(IdentityNone, keys).ItemAttributes(""); len(got) != 0 {
			t.Errorf("an empty identifier produced %v, which would look like a real item", attrMap(t, got))
		}
		var unset *ItemRedactor
		if got := unset.ItemAttributes(md5); len(got) != 0 {
			t.Errorf("a nil redactor produced %v", attrMap(t, got))
		}
	})
}

// TestItemDigestIsStableAndTellsItemsApart is what makes "this one book fails
// everywhere" distinguishable from "everything fails".
func TestItemDigestIsStableAndTellsItemsApart(t *testing.T) {
	redactor := NewItemRedactor(IdentityNone, testKeyring(t))
	digest := func(id string) string {
		attrs := redactor.ItemAttributes(id)
		if len(attrs) != 1 {
			t.Fatalf("ItemAttributes(%q) = %v, want one digest", id, attrs)
		}
		return attrs[0].Value.String()
	}

	first := digest("10.1101/2020.01.01.900000")
	if again := digest("10.1101/2020.01.01.900000"); again != first {
		t.Errorf("one DOI produced two digests, %q and %q", first, again)
	}
	if other := digest("10.1101/2020.01.01.900001"); other == first {
		t.Errorf("two DOIs produced one digest %q", first)
	}
}

// TestTheTwoKeysAreDifferent is the property HKDF is there for.
//
// One operator secret yields an identity key and an item key, and a digest of a
// caller must not be comparable against a digest of an item. Derived with the
// same info string they would be the same key, and an address that happened to
// equal an identifier would produce one digest — which is not a realistic
// collision so much as a silent removal of the separation the info strings buy.
func TestTheTwoKeysAreDifferent(t *testing.T) {
	t.Parallel()

	keys := testKeyring(t)
	const value = "the-same-input-through-both-keys"

	identity := keys.IdentityPseudonym(value)
	item := keys.ItemPseudonym(value)

	if identity == "" || item == "" {
		t.Fatalf("a digest came back empty: identity=%q item=%q", identity, item)
	}
	if identity == item {
		t.Errorf("one input produced the same digest under both keys (%q); the info strings are not separating them", identity)
	}
}

// TestKeyringFromOneSecretIsReproducible is what a multi-replica deployment
// depends on: every replica deriving the same keys from the configured secret,
// so one caller has one digest across the fleet.
func TestKeyringFromOneSecretIsReproducible(t *testing.T) {
	t.Parallel()

	const secret = "a-shared-fixture-secret-for-replicas"
	first, err := NewKeyring(secret, 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	second, err := NewKeyring(secret, 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	if !first.Configured() || !second.Configured() {
		t.Fatal("a keyring built from a secret does not report itself configured")
	}
	if a, b := first.IdentityPseudonym("203.0.113.7"), second.IdentityPseudonym("203.0.113.7"); a != b {
		t.Errorf("two replicas produced %q and %q for one caller", a, b)
	}
	// And a different secret does not, so the equality above is the derivation
	// rather than a constant.
	third, err := NewKeyring(secret+"-other", 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if a, c := first.IdentityPseudonym("203.0.113.7"), third.IdentityPseudonym("203.0.113.7"); a == c {
		t.Error("two different secrets produced one digest")
	}
}

// TestGeneratedKeyringsDifferPerProcess is the other lifetime, and its cost.
//
// Nothing is written down, so nothing can leak — and the pseudonym dies with the
// process, which means two replicas with no configured key report one caller as
// two. That is the trade, and it is why a fleet configures a key.
func TestGeneratedKeyringsDifferPerProcess(t *testing.T) {
	t.Parallel()

	first, err := NewKeyring("", 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	second, err := NewKeyring("", 0)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	if first.Configured() || second.Configured() {
		t.Error("a generated keyring reports itself configured")
	}
	if a, b := first.IdentityPseudonym("203.0.113.7"), second.IdentityPseudonym("203.0.113.7"); a == b {
		t.Error("two generated keyrings produced the same digest; the key is not random")
	}
}

// TestRotationChangesTheDigestWhenItIsDue drives the clock rather than sleeping.
func TestRotationChangesTheDigestWhenItIsDue(t *testing.T) {
	keys, err := NewKeyring("", time.Hour)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	now := time.Now()
	keys.mu.Lock()
	keys.now = func() time.Time { return now }
	keys.rotatedAt = now
	keys.mu.Unlock()

	before := keys.IdentityPseudonym("203.0.113.7")
	if before == "" {
		t.Fatal("no digest before the rotation")
	}

	// Short of the interval, nothing moves: a pseudonym that churned early would
	// split one caller across the window the operator asked for.
	now = now.Add(59 * time.Minute)
	if got := keys.IdentityPseudonym("203.0.113.7"); got != before {
		t.Errorf("the digest changed before the interval elapsed: %q then %q", before, got)
	}

	now = now.Add(2 * time.Minute)
	after := keys.IdentityPseudonym("203.0.113.7")
	if after == before {
		t.Error("the digest did not change after the rotation interval elapsed")
	}
	if after == "" {
		t.Error("the rotation left no key at all")
	}
}

// TestRotationIsIgnoredForAConfiguredKey keeps an operator's own secret theirs.
//
// Rotating a key somebody supplied would destroy the correlation they
// configured it for, which is the only reason to configure one.
func TestRotationIsIgnoredForAConfiguredKey(t *testing.T) {
	keys, err := NewKeyring("a-configured-secret-for-this-case", time.Hour)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if keys.Rotation() != 0 {
		t.Errorf("Rotation() = %s for a configured key, want zero", keys.Rotation())
	}

	now := time.Now()
	keys.mu.Lock()
	keys.now = func() time.Time { return now }
	keys.mu.Unlock()

	before := keys.IdentityPseudonym("203.0.113.7")
	now = now.Add(365 * 24 * time.Hour)
	if after := keys.IdentityPseudonym("203.0.113.7"); after != before {
		t.Errorf("a configured key rotated after a year: %q then %q", before, after)
	}
}

// TestRotationOutOfRangeIsRefused keeps a typo a startup error.
//
// A value out of range is refused rather than clamped, for the reason every
// other bounded duration on this surface is: a month of telemetry keyed under a
// mistake is discovered a month later.
func TestRotationOutOfRangeIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		rotation time.Duration
	}{
		{name: "negative", rotation: -time.Second},
		{name: "past the maximum", rotation: MaxKeyRotation + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			keys, err := NewKeyring("", tc.rotation)
			if err == nil {
				t.Fatalf("NewKeyring(%s) = %v, nil, want a refusal", tc.rotation, keys)
			}
			if !strings.Contains(err.Error(), MaxKeyRotation.String()) {
				t.Errorf("the refusal does not name the maximum: %v", err)
			}
		})
	}

	// The boundary itself is accepted, so the refusal is a bound rather than an
	// off-by-one.
	if _, err := NewKeyring("", MaxKeyRotation); err != nil {
		t.Errorf("NewKeyring(%s) = %v, want the maximum accepted", MaxKeyRotation, err)
	}
}

// TestStripExportedRemovesTheGovernedFieldsAtAnyDepth is the export-side rule,
// and the three ways a flat name check misses.
func TestStripExportedRemovesTheGovernedFieldsAtAnyDepth(t *testing.T) {
	t.Parallel()

	const query = "the thing somebody searched for"
	attrs := []slog.Attr{
		slog.String("tool", "search"),
		slog.String(LogFieldQuery, query),
		slog.Group("call",
			slog.Int("results", 3),
			slog.String(LogFieldTitle, "a book nobody else needs to know about"),
			slog.Group("credentials",
				slog.String(LogFieldAnnasKey, "not-a-credential-only-a-test-fixture"),
				slog.String(LogFieldUnpaywallEmail, "somebody@example.org"),
			),
		),
		slog.Any("lazy", lazyQuery(query)),
	}

	rendered := renderAttrs(StripExported(attrs))

	for _, gone := range []string{
		query,
		"a book nobody else needs to know about",
		"not-a-credential-only-a-test-fixture",
		"somebody@example.org",
	} {
		if strings.Contains(rendered, gone) {
			t.Errorf("%q survived the strip:\n%s", gone, rendered)
		}
	}
	// What is not governed is kept: a strip that removed everything would pass
	// every assertion above and leave the export useless.
	for _, kept := range []string{"tool", "search", "results", "3"} {
		if !strings.Contains(rendered, kept) {
			t.Errorf("%q was removed, and nothing asked for that:\n%s", kept, rendered)
		}
	}
}

// lazyQuery is a [slog.LogValuer] that only becomes the query when asked, which
// is how a hot path avoids formatting a value it may not log — and how an
// unresolved value slips past a name check that inspects before resolving.
type lazyQuery string

// LogValue renders the value the attribute stands for.
func (q lazyQuery) LogValue() slog.Value {
	return slog.GroupValue(slog.String(LogFieldQuery, string(q)))
}

// renderAttrs flattens an attribute tree into one string, so a case can assert
// that a value is nowhere in it at any depth.
func renderAttrs(attrs []slog.Attr) string {
	var b strings.Builder
	var walk func([]slog.Attr)
	walk = func(attrs []slog.Attr) {
		for _, attr := range attrs {
			b.WriteString(attr.Key)
			b.WriteString("=")
			if attr.Value.Kind() == slog.KindGroup {
				b.WriteString("{")
				walk(attr.Value.Group())
				b.WriteString("}")
				continue
			}
			b.WriteString(attr.Value.String())
			b.WriteString(" ")
		}
	}
	walk(attrs)
	return b.String()
}

// TestExportStrippedFieldsNamesEverySensitiveField is the drift gate.
//
// The failure this prevents is a field added to a log line that the export-side
// redactor had no reason to know about: it survives every policy untouched,
// because no policy has heard of it. The list is written out rather than derived
// from the constants, so adding a constant without deciding whether it is
// exportable fails here.
func TestExportStrippedFieldsNamesEverySensitiveField(t *testing.T) {
	t.Parallel()

	want := []string{"annas_key", "charged_address", "query", "title", "unpaywall_email"}
	got := make([]string, 0, len(ExportStrippedFields))
	for name := range ExportStrippedFields {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("ExportStrippedFields = %q, want %q", got, want)
	}
}

// TestPolicyDescriptionSaysWhatLeaves keeps the startup line a sentence rather
// than a mode name.
//
// "pseudonymous" means nothing to somebody who did not write this package; the
// line exists so an operator who misconfigured it notices at startup rather than
// from a backend three weeks later.
func TestPolicyDescriptionSaysWhatLeaves(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		policy IdentityPolicy
		want   string
	}{
		{policy: IdentityNone, want: "nothing"},
		{policy: IdentityPseudonymous, want: "digest"},
		{policy: IdentityFull, want: "address"},
		// An unset policy describes the default rather than the empty string,
		// which is what a zero-value redactor reports.
		{policy: "", want: "nothing"},
	} {
		t.Run(string(tc.policy), func(t *testing.T) {
			t.Parallel()

			got := PolicyDescription(tc.policy)
			if !strings.Contains(got, tc.want) {
				t.Errorf("PolicyDescription(%q) = %q, want it to mention %q", tc.policy, got, tc.want)
			}
			if got == string(tc.policy) {
				t.Errorf("PolicyDescription(%q) returned the mode name rather than a description", tc.policy)
			}
		})
	}
}
