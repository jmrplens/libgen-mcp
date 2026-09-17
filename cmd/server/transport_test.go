// transport_test.go drives the transport decision through the two stat seams,
// which is the only way to reach the shapes that matter: a container's
// /dev/null stdin, a client's pipe, and a person's terminal are three answers
// from one file descriptor, and a test process has exactly one of them.

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeStdin is a stat answer a case builds, so a shape the test process does not
// have can still be asked about.
type fakeStdin struct {
	name string
	mode fs.FileMode
}

// Name reports the fixture's name, which is also what os.SameFile compares on
// through the sys value below.
func (f fakeStdin) Name() string { return f.name }

// Size reports zero: nothing here reads the fixture.
func (f fakeStdin) Size() int64 { return 0 }

// Mode reports the file mode the case chose, which is what inferTransport reads.
func (f fakeStdin) Mode() fs.FileMode { return f.mode }

// ModTime reports the zero time.
func (f fakeStdin) ModTime() time.Time { return time.Time{} }

// IsDir reports whether the fixture is a directory.
func (f fakeStdin) IsDir() bool { return f.mode.IsDir() }

// Sys returns the identity os.SameFile compares.
//
// os.SameFile is documented to report false for a FileInfo from anything but
// this package, which is precisely what makes a fake usable for the "not
// /dev/null" cases and unusable for the "is /dev/null" one. That case uses the
// real null device instead, which is the honest way to ask the question anyway.
func (f fakeStdin) Sys() any { return nil }

// stubStdin replaces the stdin stat for one test.
func stubStdin(t *testing.T, info fs.FileInfo, err error) {
	t.Helper()
	previous := stdinStat
	t.Cleanup(func() { stdinStat = previous })
	stdinStat = func() (os.FileInfo, error) { return info, err }
}

// stubDevNull replaces the null-device stat for one test.
func stubDevNull(t *testing.T, info fs.FileInfo, err error) {
	t.Helper()
	previous := devNullStat
	t.Cleanup(func() { devNullStat = previous })
	devNullStat = func() (os.FileInfo, error) { return info, err }
}

// realDevNull stats the actual null device, for the one case that must be the
// same file rather than merely look like it.
func realDevNull(t *testing.T) fs.FileInfo {
	t.Helper()
	info, err := os.Stat(os.DevNull)
	if err != nil {
		t.Skipf("this platform has no %s to compare against: %v", os.DevNull, err)
	}
	return info
}

// TestInferTransportReadsTheShapeOfStdin covers every answer file descriptor 0
// can give.
//
// The /dev/null row is the whole point and the only one that means HTTP: a
// terminal and the null device are both character devices, so "is this a
// terminal" cannot separate a person from a container started without -i. Every
// other shape means somebody may be speaking to this process, and choosing stdio
// for an unrecognized one is the safer error — a stdio server that should have
// been HTTP is obvious in seconds, while an HTTP listener that should have been
// stdio is a client hanging with no output at all.
func TestInferTransportReadsTheShapeOfStdin(t *testing.T) {
	devNull := realDevNull(t)

	tests := []struct {
		name     string
		stdin    fs.FileInfo
		stdinErr error
		wantHTTP bool
		wantWhy  string
	}{
		{name: "a pipe is a client", stdin: fakeStdin{name: "pipe", mode: os.ModeNamedPipe}, wantWhy: "stdin is a pipe"},
		{name: "a regular file is a replayed session", stdin: fakeStdin{name: "session.jsonl"}, wantWhy: "stdin is a regular file"},
		{name: "a socket is a supervisor", stdin: fakeStdin{name: "sock", mode: os.ModeSocket}, wantWhy: "stdin is "},
		{name: "a terminal is a person", stdin: fakeStdin{name: "tty", mode: os.ModeCharDevice | os.ModeDevice}, wantWhy: "stdin is "},
		{name: "the null device is nobody", stdin: devNull, wantHTTP: true, wantWhy: os.DevNull},
		{
			// Not a reason to refuse to start: a stdin that cannot be stat'ed is
			// not a stdin anybody is speaking to.
			name:     "a stdin that cannot be examined",
			stdinErr: errors.New("bad file descriptor"),
			wantWhy:  "stdin could not be examined",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubStdin(t, tt.stdin, tt.stdinErr)
			stubDevNull(t, devNull, nil)

			gotHTTP, why := inferTransport()
			if gotHTTP != tt.wantHTTP {
				t.Errorf("inferTransport() = %v, want %v (reason %q)", gotHTTP, tt.wantHTTP, why)
			}
			if !strings.Contains(why, tt.wantWhy) {
				t.Errorf("reason = %q, want it to contain %q", why, tt.wantWhy)
			}
			if why == "" {
				t.Error("the inference is silent, so an operator cannot tell why the transport was chosen")
			}
		})
	}
}

// TestInferTransportWithNoNullDeviceServesStdio pins the fallback for a platform
// or sandbox where the null device cannot be stat'ed.
//
// stdio is the answer for the same reason it is the answer to every unrecognized
// shape: it fails visibly. Defaulting to HTTP there would hand a hanging client
// to whoever was least equipped to diagnose it.
func TestInferTransportWithNoNullDeviceServesStdio(t *testing.T) {
	stubStdin(t, fakeStdin{name: "tty", mode: os.ModeCharDevice | os.ModeDevice}, nil)
	stubDevNull(t, nil, errors.New("no such device"))

	gotHTTP, why := inferTransport()
	if gotHTTP {
		t.Errorf("inferTransport() = true with no null device to compare against (reason %q)", why)
	}
	if !strings.Contains(why, "null device could not be examined") {
		t.Errorf("reason = %q, want it to say the null device could not be examined", why)
	}
}

// TestResolveTransportSettlesTheAddressAsWellAsTheTransport is the table the
// rest of the program depends on.
//
// Two flags answer two different questions here, which is what makes
// `--transport auto --http 127.0.0.1:8080` a sentence rather than a
// contradiction: --transport decides which transport, --http supplies the
// address if it turns out to be HTTP. The address in the decision is what every
// later step reads — the socket mode, the listener, the remote-download mode and
// the private-address hatch — so a row getting it wrong is not a cosmetic
// failure.
func TestResolveTransportSettlesTheAddressAsWellAsTheTransport(t *testing.T) {
	devNull := realDevNull(t)

	tests := []struct {
		name     string
		selector string
		httpAddr string
		// pipedStdin makes auto see a client rather than a container.
		pipedStdin bool
		wantHTTP   bool
		wantAddr   string
		wantWarn   string
	}{
		// The historical rule, which every existing invocation relies on.
		{name: "no selector and no address is stdio", wantAddr: ""},
		{name: "no selector and an address is HTTP there", httpAddr: "127.0.0.1:9000", wantHTTP: true, wantAddr: "127.0.0.1:9000"},
		{name: "no selector and a socket path is HTTP there", httpAddr: "/run/mcp.sock", wantHTTP: true, wantAddr: "/run/mcp.sock"},

		{name: "stdio is stdio", selector: "stdio"},
		{name: "http with no address takes the default", selector: "http", wantHTTP: true, wantAddr: defaultHTTPAddr},
		{name: "http with an address takes it", selector: "http", httpAddr: "127.0.0.1:9000", wantHTTP: true, wantAddr: "127.0.0.1:9000"},

		// Case and surrounding space are an operator's, not a syntax error.
		{name: "the selector is read case-insensitively", selector: "  HTTP  ", wantHTTP: true, wantAddr: defaultHTTPAddr},

		{name: "auto with a null stdin is HTTP", selector: "auto", wantHTTP: true, wantAddr: defaultHTTPAddr},
		{name: "auto with a null stdin and an address binds it", selector: "auto", httpAddr: "127.0.0.1:9000", wantHTTP: true, wantAddr: "127.0.0.1:9000"},
		{name: "auto with a piped stdin is stdio", selector: "auto", pipedStdin: true},

		// The one combination where the operator asked for something the process
		// will not do. Warned rather than ignored: a listener that never appears
		// is the hardest kind of thing to notice is missing.
		{
			name: "stdio with an address warns", selector: "stdio", httpAddr: "127.0.0.1:9000",
			wantWarn: "127.0.0.1:9000",
		},
		{
			name: "auto resolving to stdio with an address warns", selector: "auto", httpAddr: "127.0.0.1:9000",
			pipedStdin: true, wantWarn: "127.0.0.1:9000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pipedStdin {
				stubStdin(t, fakeStdin{name: "pipe", mode: os.ModeNamedPipe}, nil)
			} else {
				stubStdin(t, devNull, nil)
			}
			stubDevNull(t, devNull, nil)

			got, err := resolveTransport(tt.selector, tt.httpAddr)
			if err != nil {
				t.Fatalf("resolveTransport(%q, %q) error = %v", tt.selector, tt.httpAddr, err)
			}
			if got.HTTP != tt.wantHTTP {
				t.Errorf("HTTP = %v, want %v", got.HTTP, tt.wantHTTP)
			}
			if got.Addr != tt.wantAddr {
				t.Errorf("Addr = %q, want %q", got.Addr, tt.wantAddr)
			}
			if tt.wantWarn == "" {
				if got.Override != "" {
					t.Errorf("Override = %q, want none", got.Override)
				}
				return
			}
			if !strings.Contains(got.Override, tt.wantWarn) {
				t.Errorf("Override = %q, want it to name %q", got.Override, tt.wantWarn)
			}
		})
	}
}

// TestResolveTransportRefusesAnUnknownSelector pins that a typo fails at startup
// rather than being read as one of the three.
//
// Silently treating "htpp" as the historical rule would serve stdio to a
// deployment that asked for HTTP, which is the hanging-client failure this whole
// file exists to close, arriving through the flag meant to prevent it.
func TestResolveTransportRefusesAnUnknownSelector(t *testing.T) {
	_, err := resolveTransport("htpp", "")
	if err == nil {
		t.Fatal("resolveTransport() accepted an unknown selector")
	}
	for _, want := range []string{"htpp", transportStdio, transportHTTP, transportAuto} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestStdinIsTerminalSeparatesAPersonFromAContainer is what keeps the guidance
// message out of a container's logs.
//
// Both answers are character devices, so the test cannot be "is a character
// device" — it has to be "is a character device and is not the null device".
// Getting this wrong prints a paragraph of human prose on every start of every
// container, on the stream an operator reads records from.
func TestStdinIsTerminalSeparatesAPersonFromAContainer(t *testing.T) {
	devNull := realDevNull(t)
	stubDevNull(t, devNull, nil)

	t.Run("a terminal", func(t *testing.T) {
		stubStdin(t, fakeStdin{name: "tty", mode: os.ModeCharDevice | os.ModeDevice}, nil)
		if !stdinIsTerminal() {
			t.Error("a character device that is not the null device was not read as a terminal")
		}
	})
	t.Run("the null device", func(t *testing.T) {
		stubStdin(t, devNull, nil)
		if stdinIsTerminal() {
			t.Error("the null device was read as a person, so every container start prints guidance")
		}
	})
	t.Run("a pipe", func(t *testing.T) {
		stubStdin(t, fakeStdin{name: "pipe", mode: os.ModeNamedPipe}, nil)
		if stdinIsTerminal() {
			t.Error("a client's pipe was read as a person")
		}
	})
	t.Run("a stdin that cannot be examined", func(t *testing.T) {
		stubStdin(t, nil, errors.New("bad file descriptor"))
		if stdinIsTerminal() {
			t.Error("an unreadable stdin was read as a person")
		}
	})
	t.Run("no null device to compare against", func(t *testing.T) {
		// Without the comparison there is no way to tell a terminal from a
		// container's null stdin, and guessing "person" would print guidance
		// into a container's logs on every start.
		stubStdin(t, fakeStdin{name: "tty", mode: os.ModeCharDevice | os.ModeDevice}, nil)
		stubDevNull(t, nil, errors.New("no such device"))
		if stdinIsTerminal() {
			t.Error("a character device was read as a person with nothing to compare it against")
		}
	})
}

// TestTerminalGuidanceSaysWhatTheSilenceMeans pins the message's content, which
// is the only thing it is for.
//
// Someone who ran this from a shell sees a process that prints nothing and does
// nothing. The message has to say that is expected, what is actually waiting for
// input, and the two ways out — an MCP client, or --http — or it is decoration.
func TestTerminalGuidanceSaysWhatTheSilenceMeans(t *testing.T) {
	var out bytes.Buffer
	writeTerminalGuidance(&out, "9.9.9")
	text := out.String()

	for _, want := range []string{
		"9.9.9",
		"Model Context Protocol",
		"standard input",
		"--http",
		"getting-started",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the guidance does not mention %q:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("the guidance does not end in a newline, so it runs into whatever is logged next")
	}
}

// TestTransportDecisionExplainsItselfOnlyWhenThereIsSomethingToSay keeps the log
// quiet for the ordinary case.
//
// An operator who typed --transport http told the process what to do and does
// not need to be told back; the lines exist for the two cases where the process
// decided something they did not state, or ignored something they did.
func TestTransportDecisionExplainsItselfOnlyWhenThereIsSomethingToSay(t *testing.T) {
	var captured bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&captured, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })

	// An operator who typed --transport http told the process what to do and
	// does not need to be told back.
	transportDecision{HTTP: true, Addr: defaultHTTPAddr}.explain()
	if captured.Len() != 0 {
		t.Errorf("a stated transport was explained back to the operator: %s", captured.String())
	}

	// Both lines the other two cases owe: what was inferred, and what was
	// dropped. Each is the only record of a decision the operator did not make.
	transportDecision{
		Inference: "stdin is a pipe",
		Override:  "--http 127.0.0.1:9000 was given but this process is serving stdio",
	}.explain()
	logged := captured.String()
	for _, want := range []string{
		`"level":"WARN"`, "127.0.0.1:9000",
		`"level":"INFO"`, "transport inferred from stdin", `"transport":"stdio"`, "stdin is a pipe",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("the explanation does not carry %q:\n%s", want, logged)
		}
	}

	if got := transportName(true); got != transportHTTP {
		t.Errorf("transportName(true) = %q, want %q", got, transportHTTP)
	}
	if got := transportName(false); got != transportStdio {
		t.Errorf("transportName(false) = %q, want %q", got, transportStdio)
	}
}
