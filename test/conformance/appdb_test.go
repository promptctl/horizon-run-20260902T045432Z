//go:build conformance

// The application-database half of the suite: appspec/05-application-database.md
// observed at the program's boundary.
//
// One channel carries almost every case here, for the same reason the config
// file's header gives for its own: nothing this program can do yet PRINTS the
// database. list and show are macklebox-resolvers-5iw.4's. What the boundary
// does expose is appspec/05's own absolute-path rejection -- a fatal error that
// names the offending path and aborts every command -- so a definition file
// carrying "/etc/passwd" reports itself when it is read, and stays silent when
// it is not. That makes "this directory is read", "this file wins", and "that
// file was not read at all" directly observable without inventing any output
// the specification does not describe.
//
// The silent half is asserted through ExpectNotImplemented, which is this
// suite's placeholder for a command whose ticket has not landed: a run that
// reaches the dispatch stub is a run that got past database assembly. Those uses
// become assertions on real list output with macklebox-resolvers-5iw.4, the same
// replacement the README describes for every other use of it.
//
// What that leaves out, said rather than glossed: the CONTENTS of the assembled
// database -- that a dropped definition adds a listable key, that display names
// and file sets read back as written, that an XDG entry is stored home-relative
// -- are pinned by internal/appdb's own tests today, because no command prints
// them yet. The cases below assert what is observable now: which directories are
// read, which file wins, and that the two rejections abort every command alike.

package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

// poisonedDefinition is a definition file holding an absolute path, which
// appspec/05 makes a fatal error naming that path. Reading one is loud; not
// reading one is silent. See this file's header.
const poisonedDefinition = "[application]\nname = Poisoned\n\n[configuration_files]\n/etc/passwd\n"

// cleanDefinition is a definition file that assembles without complaint.
func cleanDefinition(name string) string {
	return "[application]\nname = " + name + "\n"
}

// absolutePathRefusal is the sentence appspec/05 gives the rejection literally:
// "a fatal (uncaught) error naming the offending path (Unsupported absolute
// path: <path>)". appspec/07's table repeats it in the unguarded column.
const absolutePathRefusal = "Unsupported absolute path: /etc/passwd"

// expectDatabaseRefusal runs every gated command and asserts each dies at
// database assembly with the same diagnostic, changing nothing.
//
// Every command, not one: appspec/01 section 4 puts assembly at step 3 of a
// pipeline "every command flows through", and appspec/07 says a failure there
// "aborts every command uniformly". A gate that ran for the sync commands and
// was skipped for the two that touch no storage would satisfy a single-command
// case and miss the contract.
func expectDatabaseRefusal(t *testing.T, world *World, mentions string) {
	t.Helper()
	before := world.Snapshot()

	first := world.Run(gatedCommands[0]...).ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(first.StderrText(), mentions) {
		t.Errorf("mackup %s wrote %q to stderr, want %q inside it",
			strings.Join(first.Args, " "), first.Stderr, mentions)
	}
	for _, argv := range gatedCommands[1:] {
		result := world.Run(argv...).ExpectFailureExit().ExpectSilentStdout()
		if result.Stderr != first.Stderr {
			t.Errorf("mackup %s wrote %q to stderr, want the same diagnostic mackup %s wrote: %q",
				strings.Join(argv, " "), result.Stderr, strings.Join(first.Args, " "), first.Stderr)
		}
	}

	// The post-condition both regimes of appspec/01 section 6 share: "no
	// stdout, no filesystem change, non-zero exit".
	world.ExpectUnchanged(before)
}

func TestADefinitionHoldingAnAbsolutePathAbortsEveryCommand(t *testing.T) {
	// appspec/07's startup table, step 3: "An absolute path inside a
	// definition, or $XDG_CONFIG_HOME outside the home directory, terminates
	// here with an uncaught error and a nonzero exit (unguarded)."
	//
	// appspec/05 calls this rejection "load-bearing for the sync engine's
	// safety" rather than input hygiene: appspec/06 never re-checks that a path
	// is home-relative, so a database that accepted this one would hand the
	// engine a path outside the home directory to write to.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/myapp.cfg", poisonedDefinition, 0o600)

	expectDatabaseRefusal(t, world, absolutePathRefusal)
}

func TestAnAbsoluteXDGPathIsRefusedLikeAnyOther(t *testing.T) {
	// appspec/05: "A listed XDG path starting with '/' is rejected exactly like
	// an absolute [configuration_files] path." Both sections, because a reader
	// that validated one of them passes every fixture written against the other.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/myapp.cfg",
		"[application]\nname = Poisoned\n\n[xdg_configuration_files]\n/etc/passwd\n", 0o600)

	expectDatabaseRefusal(t, world, absolutePathRefusal)
}

func TestAnXDGConfigHomeOutsideHomeAbortsEveryCommand(t *testing.T) {
	// appspec/05: "If $XDG_CONFIG_HOME resolves to a location NOT within the
	// home directory, database assembly fails with a fatal (uncaught) error
	// stating that $XDG_CONFIG_HOME must be somewhere within the home directory,
	// nonzero exit. (This check fires while assembling the database, so it
	// blocks every command.)"
	//
	// The value is outside home but inside the scratch root, so ExpectUnchanged
	// still sees it: a run that "helpfully" created the directory it was told
	// about would be caught rather than invisible.
	world := NewWorld(t)
	world.UseResolvableStorage()
	outside := filepath.Join(world.Root, "outside-home")
	world.Setenv("XDG_CONFIG_HOME", outside)

	expectDatabaseRefusal(t, world, outside)
}

func TestTheXDGBaseIsCheckedBeforeAnyDefinitionIsRead(t *testing.T) {
	// appspec/05 states the consequence unconditionally -- "this check fires
	// while assembling the database, so it blocks every command" -- which holds
	// only if the check does not wait for the first definition that happens to
	// carry an [xdg_configuration_files] section. Some shipped definition always
	// does, so a lazy check still refuses and looks conformant under every
	// fixture but this one.
	//
	// The other rejection is what makes the order visible: ~/.mackup holds a
	// definition with an absolute path, named so that it sorts before every
	// built-in key. A lazy check reports that one; an up-front check reports the
	// $XDG_CONFIG_HOME the user actually set, which is the diagnostic they can
	// act on.
	world := NewWorld(t)
	world.UseResolvableStorage()
	outside := filepath.Join(world.Root, "outside-home")
	world.Setenv("XDG_CONFIG_HOME", outside)
	world.WriteFile(".mackup/000-sorts-first.cfg", poisonedDefinition, 0o600)

	text := world.Run("list").ExpectFailureExit().ExpectSilentStdout().StderrText()
	if !strings.Contains(text, outside) {
		t.Errorf("stderr = %q, want the $XDG_CONFIG_HOME refusal naming %q to come first", text, outside)
	}
	if strings.Contains(text, absolutePathRefusal) {
		t.Errorf("stderr = %q, want the base checked before any definition file is read", text)
	}
}

func TestTheDatabaseRefusalsTakeTheUnguardedShape(t *testing.T) {
	// appspec/07's table puts both database rows in the unguarded column, and
	// appspec/01 section 6 says "which cases fall in which regime is itself
	// contract as observed". This program keeps the two regimes apart rather
	// than taking the permission both specifications give to collapse them --
	// the reasoning is in internal/fault -- so the check here is the same one
	// TestTheTwoConfigFailureRegimesAreDistinguishable makes of the config rows,
	// applied to the two rows that belong to this stage.
	for _, c := range []struct {
		what  string
		build func(*World)
	}{
		{"an absolute path in a definition", func(w *World) {
			w.WriteFile(".mackup/myapp.cfg", poisonedDefinition, 0o600)
		}},
		{"an $XDG_CONFIG_HOME outside home", func(w *World) {
			w.Setenv("XDG_CONFIG_HOME", filepath.Join(w.Root, "outside-home"))
		}},
	} {
		world := NewWorld(t)
		world.UseResolvableStorage()
		c.build(world)

		text := world.Run("list").ExpectFailureExit().ExpectSilentStdout().StderrText()
		if strings.HasPrefix(text, "Error: ") {
			t.Errorf("%s: stderr = %q reads as a guarded row, want the unguarded shape appspec/07 gives it", c.what, text)
		}
	}
}

func TestEveryDatabaseFailureIsBrightRedOnEveryLine(t *testing.T) {
	// appspec/07: fatal errors are bright red, and "every colored string is
	// terminated with a reset". Asserted for this stage's rows too, so that a
	// diagnostic added here later cannot bypass the routing every other fatal
	// goes through.
	world := NewWorld(t)
	world.UseResolvableStorage()
	world.WriteFile(".mackup/myapp.cfg", poisonedDefinition, 0o600)

	world.Run("list").
		ExpectFailureExit().
		ExpectStderrColor("91").
		ExpectSilentStdout()
}

func TestHelpAndVersionSkipTheDatabaseGateToo(t *testing.T) {
	// appspec/07 step 1: "--help / --version print and exit 0 here, before
	// anything else." A definition the database would refuse must not reach
	// them, exactly as a broken config does not.
	world := NewWorld(t)
	world.WriteFile(".mackup/myapp.cfg", poisonedDefinition, 0o600)

	world.Run("--help").ExpectExit(0).ExpectStdout(usageMarker).ExpectSilentStderr()
	world.Run("--version").ExpectExit(0).ExpectVersionLine().ExpectSilentStderr()
	world.Run().ExpectExit(0).ExpectStdout(usageMarker).ExpectSilentStderr()
}

func TestTheConfigIsResolvedBeforeTheDatabaseIsAssembled(t *testing.T) {
	// The pipeline order of appspec/01 section 4: config load is step 2 and
	// database assembly is step 3. With both broken, the config's diagnostic is
	// the one the user sees -- and it is the useful one, since a config naming a
	// storage engine that does not exist makes every later stage's work moot.
	//
	// Observable only because the two stages fail with different words, which is
	// what makes this an ordering claim rather than a restatement of either row.
	world := NewWorld(t)
	writeConfig(world, unknownEngine("from-config"))
	world.WriteFile(".mackup/myapp.cfg", poisonedDefinition, 0o600)

	text := world.Run("list").ExpectFailureExit().ExpectSilentStdout().StderrText()
	if !strings.Contains(text, "from-config") {
		t.Errorf("stderr = %q, want the config failure: appspec/01 section 4 loads the config first", text)
	}
	if strings.Contains(text, absolutePathRefusal) {
		t.Errorf("stderr = %q, want the database not to have been assembled at all", text)
	}
}

func TestBothUserDefinitionDirectoriesAreRead(t *testing.T) {
	// appspec/05 "Discovery" tiers 1 and 2: "~/.mackup/" and
	// "$XDG_CONFIG_HOME/mackup/applications/" (default
	// "~/.config/mackup/applications/"). A program that read only the legacy
	// directory -- the one most fixtures use -- passes every other case in this
	// file.
	for _, directory := range []string{".mackup", ".config/mackup/applications"} {
		world := NewWorld(t)
		world.UseResolvableStorage()
		world.WriteFile(filepath.Join(directory, "myapp.cfg"), poisonedDefinition, 0o600)

		result := world.Run("list").ExpectFailureExit().ExpectSilentStdout()
		if !strings.Contains(result.StderrText(), absolutePathRefusal) {
			t.Errorf("a definition in %s was not read: stderr = %q", directory, result.Stderr)
		}
	}
}

func TestTheLegacyDirectoryShadowsTheXDGOneByFilename(t *testing.T) {
	// appspec/05: a *.cfg in the XDG apps directory is taken "only if a file of
	// the same name was not already taken from the legacy directory", and the
	// loser "is not read at all for that key".
	//
	// Both directions, because each alone is satisfied by a program that reads
	// only one of the two directories: the shadowing case would pass for a
	// program blind to the XDG directory, and the shadowed case for one blind to
	// ~/.mackup.
	shadowing := NewWorld(t)
	shadowing.UseResolvableStorage()
	shadowing.WriteFile(".config/mackup/applications/myapp.cfg", poisonedDefinition, 0o600)
	shadowing.WriteFile(".mackup/myapp.cfg", cleanDefinition("From Legacy"), 0o600)
	shadowing.Run("list").ExpectNotImplemented("list")

	shadowed := NewWorld(t)
	shadowed.UseResolvableStorage()
	shadowed.WriteFile(".config/mackup/applications/myapp.cfg", cleanDefinition("From XDG"), 0o600)
	shadowed.WriteFile(".mackup/myapp.cfg", poisonedDefinition, 0o600)
	result := shadowed.Run("list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), absolutePathRefusal) {
		t.Errorf("the ~/.mackup definition did not win its filename: stderr = %q", result.Stderr)
	}

	// A DIFFERENT filename in the same two directories is not shadowed, which is
	// what makes the rule "by filename" rather than "the legacy directory wins".
	both := NewWorld(t)
	both.UseResolvableStorage()
	both.WriteFile(".config/mackup/applications/other.cfg", poisonedDefinition, 0o600)
	both.WriteFile(".mackup/myapp.cfg", cleanDefinition("From Legacy"), 0o600)
	result = both.Run("list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), absolutePathRefusal) {
		t.Errorf("a differently-named XDG definition was shadowed anyway: stderr = %q", result.Stderr)
	}
}

func TestOnlyCfgFilesDirectlyInADefinitionDirectoryAreRead(t *testing.T) {
	// appspec/05: "Every *.cfg file DIRECTLY in this directory is taken ... Only
	// files ending in .cfg are considered; other files are ignored."
	//
	// Each name below holds a definition the database would refuse, so a reader
	// that took any one of them aborts the run rather than quietly adding a key.
	world := NewWorld(t)
	world.UseResolvableStorage()
	for _, name := range []string{
		".mackup/notes.txt",
		".mackup/nested/myapp.cfg",
		".mackup/upper.CFG",
		".mackup/.hidden.cfg",
		".config/mackup/applications/notes.txt",
		".config/mackup/applications/nested/myapp.cfg",
	} {
		world.WriteFile(name, poisonedDefinition, 0o600)
	}

	world.Run("list").ExpectNotImplemented("list")
}

func TestAMissingDefinitionDirectoryIsSkipped(t *testing.T) {
	// appspec/05: "A user directory that does not exist is simply skipped." The
	// ordinary state of a machine nobody has customized, and the one where a
	// program that treated an unreadable directory listing as fatal would fail
	// every command.
	world := NewWorld(t)
	world.UseResolvableStorage()

	world.Run("list").ExpectNotImplemented("list")
}

func TestTheXDGAppsDirectoryFollowsXDGConfigHome(t *testing.T) {
	// appspec/05 writes the second directory as "$XDG_CONFIG_HOME/mackup/
	// applications/ (default ~/.config/mackup/applications/)", so the variable
	// moves it. Both halves are asserted: the moved directory is read, and the
	// default location is not read once the variable points elsewhere -- the
	// second is what a program that simply added a fourth directory would fail.
	moved := NewWorld(t)
	moved.UseResolvableStorage()
	moved.Setenv("XDG_CONFIG_HOME", moved.Path("xdg"))
	moved.WriteFile("xdg/mackup/applications/myapp.cfg", poisonedDefinition, 0o600)
	result := moved.Run("list").ExpectFailureExit().ExpectSilentStdout()
	if !strings.Contains(result.StderrText(), absolutePathRefusal) {
		t.Errorf("the apps directory under $XDG_CONFIG_HOME was not read: stderr = %q", result.Stderr)
	}

	abandoned := NewWorld(t)
	abandoned.UseResolvableStorage()
	abandoned.Setenv("XDG_CONFIG_HOME", abandoned.Path("xdg"))
	abandoned.WriteFile(".config/mackup/applications/myapp.cfg", poisonedDefinition, 0o600)
	abandoned.Run("list").ExpectNotImplemented("list")
}
