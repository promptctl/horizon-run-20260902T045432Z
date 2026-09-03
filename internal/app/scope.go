package app

import (
	"github.com/promptctl/macklebox/internal/appdb"
	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/config"
	"github.com/promptctl/macklebox/internal/ui"
)

// The selector of appspec/01-architecture.md section 1: "scope = (allowlist or
// all-keys) minus denylist, overridable to a single key by the CLI argument.
// This is the join between 'what the user configured' and 'what definitions
// exist.'"
//
// The two halves live in different places on purpose. The list arithmetic is
// config.Scope's -- appspec/03 owns the combined precedence, including the rule
// a reimplementation is most likely to get backwards, that an application in
// BOTH lists is ignored -- and the argv override is here, because
// internal/config does not know argv and must not learn it. What this file
// contributes is the join and the ORDER: appspec/01 section 3 requires the
// named key to be validated before the environment gate, so the validation is
// part of selecting the scope rather than part of acting on it.
//
// This is the first non-test caller of config.Scope. Three earlier tickets
// recorded that `list` would be it; that was wrong, and the reason is written
// where it belongs, on enumerate.go's list -- appspec/03 says of
// [applications_to_sync] that "this section does not affect `list` output".
// The lists select what a SYNC command acts on, and this is where a sync
// command asks.

// selectApplications resolves the application keys one sync command acts on.
//
// It reports false when an application was NAMED and the database does not
// hold it. The caller writes appspec/07's literal token for that condition and
// exits 1; it is returned rather than printed here so that the one place the
// token is spelled stays the one place -- see UnsupportedApplicationPrefix.
//
// A named application REPLACES the configured scope and overrides BOTH lists:
// appspec/01 section 3, "a named app replaces the configured scope with exactly
// that key and overrides both the allow and ignore lists (an ignored app is
// still acted on when named)". So this does not call Scope at all in that
// branch, rather than calling it and then adding the key back -- an ignored app
// that Scope has already removed cannot be distinguished afterwards from one
// the user never configured, and "filter, then re-add" is how the override
// comes to depend on the filter it is supposed to bypass.
//
// With no application named, the keys come back in the order appdb.Keys hands
// them over, which is sorted ascending, and config.Scope preserves that order.
// That is appspec/01's "applications in sorted (ascending, byte/lexicographic)
// key order", and it is a whole-program guarantee rather than a per-command
// one: nothing here re-sorts, because a second sort is a second implementation
// of an ordering two places would then have to agree on.
func selectApplications(inv cli.Invocation, cfg *config.Config, apps *appdb.Database) ([]string, bool) {
	if inv.Application == "" {
		return cfg.Scope(apps.Keys()), true
	}
	if _, known := apps.Name(inv.Application); !known {
		return nil, false
	}
	return []string{inv.Application}, true
}

// resolveScope selects the applications one sync command acts on, and writes
// appspec/07's refusal when the run named an application the database does not
// hold.
//
// The refusal is here rather than at each command, because appspec/07's error
// table gives the condition ONE row -- "Named application unknown | stderr | 1 |
// Unsupported application: <name>" -- not one row per command, and because the
// order it enforces is contract: appspec/06 "Environment gate per command" says
// that when an application is named "its validity is checked BEFORE this gate,
// so an unknown app name fails ... before any folder is created or prompt
// shown". A command that called selectApplications and then its gate could get
// that order right; five commands each calling both is five chances to get it
// wrong, and the failure is silent -- the run still refuses the name, just
// after prompting the user about a folder it then does not use.
//
// It reports false when the run must stop, so the caller's whole obligation is
// to return ExitFailure. Nothing further is printed: the token is already out.
func resolveScope(p pipeline) ([]string, bool) {
	keys, known := selectApplications(p.inv, p.cfg, p.apps)
	if !known {
		// The same token, level and stream `show` uses for the same condition.
		p.streams.Say(ui.Fatal, UnsupportedApplicationPrefix+p.inv.Application)
		return nil, false
	}
	return keys, true
}
