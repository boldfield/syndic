//go:build darwin && cgo

package sandbox

// Exported for the enforcement test of package sandbox_test.
var ProfileMembers = profileMembers

const MacAllowTokenName = macAllowTokenName
