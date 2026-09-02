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
		// TODO(macklebox-resolvers-5iw.4): the appspec/05 enumeration
		// formats -- sorted keys with the count trailer, display name with
		// sorted file paths.
		return notImplemented(inv, streams)
	case cli.CmdBackup, cli.CmdRestore:
		// TODO(macklebox-copy-sync-dpz.3): the one copy operation, run in
		// either direction (appspec/01 section 1).
		return notImplemented(inv, streams)
	case cli.CmdLinkInstall:
		// TODO(macklebox-link-sync-83q.2): move home files into storage,
		// symlink them back.
		return notImplemented(inv, streams)
	case cli.CmdLink:
		// TODO(macklebox-link-sync-83q.3): symlink files already in storage
		// into home, moving nothing out of home.
		return notImplemented(inv, streams)
	case cli.CmdLinkUninstall:
		// TODO(macklebox-link-sync-83q.4): revert links to real files, and
		// refuse to clobber a file the user substituted.
		return notImplemented(inv, streams)
	default:
		streams.Sayf(ui.Fatal, "mackup: unhandled command: %v", inv.Cmd)
		return ExitFailure
	}
}

func notImplemented(inv cli.Invocation, streams *ui.IO) int {
	streams.Sayf(ui.Fatal, "Error: %s is not implemented yet.", inv.Cmd)
	return ExitFailure
}
