// Package acceptance holds the milestone B acceptance test suite for the
// Odonian engine. Each test verifies one or more acceptance items from
// docs/design/v4-delta-engine-tui.md section 8. Tests run the engine,
// hub, server and client together over a Unix socket using fake.Adapter
// participants and a fake clock.
//
// The suite is deterministic: no sleeps for synchronization, no dependence
// on wall-clock time, all waiting done on events from the subscription.
// The whole package runs in under two minutes on CI.
//
// See coverage_test.go for a table mapping every section 8 acceptance item
// to the test function that covers it.
package acceptance
