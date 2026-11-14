# internal/actions

Allowlisted action model. The LLM only proposes actions; this package
validates them, requires confirmation for destructive ones, and writes the
audit log with undo. Phase 4+.
