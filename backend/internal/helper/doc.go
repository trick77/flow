// Package helper runs the helper-model tasks (session titles, branch names, sidebar summaries, commit and pull
// request text) through github.com/trick77/toolkit/llmwire, never Anthropic, and derives them without a model when
// no helper provider is configured. Owner: helper-model area (docs/ownership.md).
package helper
