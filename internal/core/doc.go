// Package core holds Friday's domain model: identifiers, money, the task
// lifecycle state machine, domain types, port interfaces, and the event
// trail. It performs no I/O, starts no goroutines, and reads no environment.
//
// Dependency rule: core may import only the standard library,
// github.com/google/uuid, and internal/redact. Adapters (config, providers,
// sandboxes, stores) depend on core, never the reverse.
package core
