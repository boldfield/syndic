package tui

// Mouse event handling for the TUI.
//
// Mouse support is opt-in via /mouse on|off or --mouse flag.
// The mouse is never captured under TERM=dumb or with colour off.
//
// Mouse interactions:
// - Tab bar clicks switch tabs (chat, transcript, participants).
// - Review count clicks in the chat tab open the drawer on that turn.
// - Round header clicks in the drawer header select that round.
//
// updateMouse in app.go routes mouse messages to the appropriate handlers
// based on button and action. These handlers (handleTabClick,
// handleReviewCountClick, handleDrawerRoundClick) perform the actual work.
