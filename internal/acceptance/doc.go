// Package acceptance holds the milestone B acceptance suite: the paragraphs of
// section 8 of docs/design/v4-delta-engine-tui.md, each item a named test that
// runs the finished engine behind its real control socket with scripted fake
// adapters and a fake clock.
//
// The package has only test files. harness_test.go builds the engine, hub and
// server and the helpers that wait on a client subscription;
// coverage_test.go records which test covers which design item and checks the
// record against the source; one file per paragraph holds the tests.
package acceptance
