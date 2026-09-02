package app

import (
	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/ui"
)

// dispatch runs the subcommand argv selected. Each arm is filled in by the
// ticket named beside it; until then a subcommand reports that it is not
// implemented on stderr and exits non-zero, so no caller can mistake an
// unimplemented verb for a completed action.
func dispatch(inv cli.Invocation, streams *ui.IO) int {
	switch inv.Cmd {
	case cli.CmdList, cli.CmdShow:
		// TODO(macklebox-resolvers-5iw.4)
		return notImplemented(inv, streams)
	case cli.CmdBackup, cli.CmdRestore:
		// TODO(macklebox-copy-sync-dpz.3)
		return notImplemented(inv, streams)
	case cli.CmdLinkInstall:
		// TODO(macklebox-link-sync-83q.2)
		return notImplemented(inv, streams)
	case cli.CmdLink:
		// TODO(macklebox-link-sync-83q.3)
		return notImplemented(inv, streams)
	case cli.CmdLinkUninstall:
		// TODO(macklebox-link-sync-83q.4)
		return notImplemented(inv, streams)
	default:
		streams.Errf("mackup: unhandled command: %v\n", inv.Cmd)
		return ExitFailure
	}
}

func notImplemented(inv cli.Invocation, streams *ui.IO) int {
	streams.Errf("Error: %s is not implemented yet.\n", inv.Cmd)
	return ExitFailure
}
