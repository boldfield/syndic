package engine

import (
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/boldfield/syndic/internal/workspace"
)

// attachmentEgressRefusal is the policy step for a submit that carries
// attachments (v6 section 4, step 4), run before any file is opened. Every
// participant of the preset receives the attachment, so each must be on an
// endpoint that may receive file content, and a preset that turns web with
// file content off may not give any participant web, whatever tools it
// grants and whether or not the conversation was tainted. It applies the
// roster's tainted-conversation rules, which reach every participant, and
// names the route "attachment".
func attachmentEgressRefusal(r *config.Roster, eff config.Effective) *wire.Error {
	we := egressRefusal(r, eff, true)
	if we != nil {
		we.Route = strPtr(config.EgressRouteAttachment)
	}
	return we
}

// readSubmitAttachments opens the turn's workspace with the roster's
// protected patterns and reads the attachments. A submit that names
// attachments in a conversation with no workspace has nothing to read them
// from.
func (e *Engine) readSubmitAttachments(root string, paths []string) ([]journal.AttachmentContent, *wire.Error, error) {
	if len(paths) == 0 {
		return nil, nil, nil
	}
	if root == "" {
		return nil, attachmentRefusal(paths[0], attachNotFound), nil
	}
	var patterns []string
	if e.roster.Tools != nil {
		patterns = e.roster.Tools.ProtectedPaths
	}
	fs, err := workspace.OpenConfined(root, patterns)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = fs.Close() }()
	atts, werr := ReadAttachments(fs, paths)
	return atts, werr, nil
}

// seedAttachmentCanary adds every attachment's bytes to the turn's canary
// corpus, so the first web call of any role is screened against them.
func seedAttachmentCanary(s *tools.URLScreener, atts []journal.AttachmentContent) {
	for _, a := range atts {
		s.AddBytes(tools.RouteSnapshotRead, a.Content)
	}
}
