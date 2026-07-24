module github.com/mdcfrancis/flow

go 1.26.4

require (
	github.com/alecthomas/participle/v2 v2.1.4
	github.com/tetratelabs/wazero v1.12.0
	go.etcd.io/bbolt v1.5.0
)

require golang.org/x/sys v0.45.0 // indirect

// Shared memories require the reference-types core feature.
