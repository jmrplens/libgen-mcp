// scan.go runs govulncheck over one binary at module grain and reads the JSON
// stream it prints.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/vuln/scan"
)

// finding is one advisory govulncheck matched against one module of a binary.
type finding struct {
	osv     string
	module  string
	version string
	fixed   string
}

// scanResult is what one govulncheck run said about one binary.
//
// The scanner's version is not kept: govulncheck reports the version of the
// main module of the process it runs in, which here is this repository, so
// the field reads v0.0.0. The version that scanned is the one go.mod pins.
type scanResult struct {
	db         string
	dbModified string
	goVersion  string
	modules    int
	findings   []finding
	// summaries holds the one-line summary of every advisory the run printed,
	// by id, which is how a finding is described without a second lookup.
	summaries map[string]string
	// imports holds, by declaration key, the packages each advisory names for
	// each module it is filed against: what a not-linked declaration claims
	// the binaries do not link, read from the same record the scan matched.
	imports map[string][]string
	// sawConfig and sawSBOM record that the stream said what it scanned with
	// and what it scanned, without which it is not a scan at all.
	sawConfig, sawSBOM bool
}

// scanMessage is one object of govulncheck's JSON stream, reduced to the
// fields this command reads. The stream's schema is the Message type of
// golang.org/x/vuln/internal/govulncheck, protocol v1.0.0.
type scanMessage struct {
	Config *struct {
		DB             string `json:"db"`
		DBLastModified string `json:"db_last_modified"`
	} `json:"config"`
	SBOM *struct {
		GoVersion string            `json:"go_version"`
		Modules   []json.RawMessage `json:"modules"`
	} `json:"SBOM"`
	OSV *struct {
		ID       string `json:"id"`
		Summary  string `json:"summary"`
		Affected []struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
			EcosystemSpecific struct {
				Imports []struct {
					Path string `json:"path"`
				} `json:"imports"`
			} `json:"ecosystem_specific"`
		} `json:"affected"`
	} `json:"osv"`
	Finding *struct {
		OSV          string `json:"osv"`
		FixedVersion string `json:"fixed_version"`
		Trace        []struct {
			Module  string `json:"module"`
			Version string `json:"version"`
		} `json:"trace"`
	} `json:"finding"`
}

// scanBinary runs govulncheck over one binary at module grain.
//
// It runs in this process rather than as a child, so the version is the one
// go.mod pins and nothing on PATH can stand in for it. Start is joined with
// Wait because Start's only failure is being called twice, which a fresh
// command never is, and both go through the one error the caller sees.
func scanBinary(ctx context.Context, db, path string) (scanResult, error) {
	var stdout, stderr bytes.Buffer
	cmd := scan.Command(ctx, "-mode=binary", "-scan=module", "-format=json", "-db="+db, path)
	cmd.Stdin = strings.NewReader("")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := errors.Join(cmd.Start(), cmd.Wait()); err != nil {
		return scanResult{}, fmt.Errorf("govulncheck on %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return parseScan(&stdout)
}

// parseScan reads govulncheck's JSON stream.
//
// A stream without its configuration or without the module list of the binary
// is refused rather than read as a clean scan: empty output and a scan that
// found nothing would otherwise look the same, and only one of them is a
// binary somebody checked.
func parseScan(r io.Reader) (scanResult, error) {
	result := scanResult{summaries: map[string]string{}, imports: map[string][]string{}}
	decoder := json.NewDecoder(r)
	for {
		var msg scanMessage
		err := decoder.Decode(&msg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return scanResult{}, fmt.Errorf("reading govulncheck's output: %w", err)
		}
		result.read(&msg)
	}
	if !result.sawConfig || !result.sawSBOM {
		return scanResult{}, errors.New("govulncheck's output carries no configuration or no module list, so it is not a scan of a binary")
	}
	return result, nil
}

// read folds one message of the stream into the result. A message carries one
// kind of object, but each kind is read independently, so one that carried
// two would lose neither.
func (r *scanResult) read(msg *scanMessage) {
	if msg.Config != nil {
		r.sawConfig = true
		r.db, r.dbModified = msg.Config.DB, msg.Config.DBLastModified
	}
	if msg.SBOM != nil {
		r.sawSBOM = true
		r.goVersion, r.modules = msg.SBOM.GoVersion, len(msg.SBOM.Modules)
	}
	if msg.OSV != nil {
		r.summaries[msg.OSV.ID] = msg.OSV.Summary
		for _, affected := range msg.OSV.Affected {
			key := declarationKey(msg.OSV.ID, affected.Package.Name)
			for _, imp := range affected.EcosystemSpecific.Imports {
				r.imports[key] = append(r.imports[key], imp.Path)
			}
		}
	}
	if msg.Finding != nil {
		r.findings = append(r.findings, findingOf(msg))
	}
}

// findingOf reads a finding message. The module and version are the first
// frame of the trace, which at module grain is the module itself; a finding
// without a trace keeps both empty rather than being dropped.
func findingOf(msg *scanMessage) finding {
	f := finding{osv: msg.Finding.OSV, fixed: msg.Finding.FixedVersion}
	if len(msg.Finding.Trace) > 0 {
		f.module, f.version = msg.Finding.Trace[0].Module, msg.Finding.Trace[0].Version
	}
	return f
}
