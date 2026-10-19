// A module of its own, so golang.org/x/vuln and what it brings (x/tools,
// x/mod, x/telemetry) stay out of the server's go.mod. See doc.go.
module github.com/jmrplens/libgen-mcp/v2/cmd/audit_binary_vulns

go 1.27.2

require (
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/vuln v1.9.0
)

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
)
