// A module of its own, so golang.org/x/vuln and what it brings (x/tools,
// x/mod, x/telemetry) stay out of the server's go.mod. See doc.go.
module github.com/jmrplens/libgen-mcp/v2/cmd/audit_binary_vulns

go 1.27.1

require (
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/vuln v1.8.0
)

require (
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/telemetry v0.0.0-20260924152758-ed294f943157 // indirect
	golang.org/x/tools v0.51.0 // indirect
)
