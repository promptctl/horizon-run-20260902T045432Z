#!/usr/bin/env python3
"""Mutation battery for the conformance rig.

A rig that shells out to a built binary can report green over a program it no
longer covers, and it has done so on this branch more than once. Injection is
the only thing that proves it can fail: each mutation below is a defect the
suite is supposed to catch, and every one of them MUST fail `make check`. A
mutation that survives is a hole in the rig, not a curiosity.

Run it from anywhere:  python3 test/mutation/battery.py [mutation name ...]

It is deliberately NOT wired into `make check` or CI. It rewrites source files
in place and restores them afterwards, which is not something a gate should do
to a working tree. Run it by hand whenever the rig changes.

Three rules it enforces, each of which it once got wrong:

  * A mutation must COMPILE before its failure counts. `go vet` runs first; a
    mutation that merely breaks the build also fails the gate and proves
    nothing about the rig.

  * A mutation to the program itself is put to `make conformance` separately.
    `make check` runs the unit packages first and stops there on failure, so a
    mutation the unit tests kill never reaches the conformance suite and says
    nothing about whether the RIG can see it. Reported as RIG-BLIND.

  * A mutation may assert the diagnostic it expects, not just a non-zero exit,
    so that a kill by some unrelated case does not count as coverage.

The tree must be committed before running this. It refuses to start on a dirty
one, and restores in a finally, so an interrupt or an exception puts the
sources back rather than leaving a mutation in the tree.
"""

import atexit, os, shutil, signal, subprocess, sys, tempfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BACKUP = tempfile.mkdtemp(prefix="macklebox-mutation-backup-")
# Registered here rather than at the end of the happy path: the dirty-tree
# refusal below exits before the run starts, and an exception can leave through
# the middle, and both used to leak a directory holding copies of every tracked
# source file. Ten aborted runs left ten of them.
atexit.register(lambda: shutil.rmtree(BACKUP, ignore_errors=True))
FILES = ["test/conformance/harness_test.go", "test/conformance/argv_test.go",
         "test/conformance/harness_unix_test.go",
         "internal/cli/usage.go", "internal/app/dispatch.go", "internal/app/app.go",
         "internal/cli/parse.go", "Makefile",
         "internal/version/version.go",
         "internal/ui/color.go",
         "internal/config/config.go", "internal/storage/storage.go",
         "internal/fault/fault.go",
         "internal/appdb/appdb.go", "internal/ini/ini.go",
         "internal/homepath/homepath.go",
         "internal/app/enumerate.go", "internal/app/stages.go",
         "internal/app/sync.go", "internal/app/folder.go",
         # appspec/01 section 1's "one uniform per-file executor", split out of
         # sync.go when link install landed, and link install itself. The
         # per-application verbose header's entry moved with the code; the
         # link entries are new.
         "internal/app/executor.go", "internal/app/link.go",
         # The catalog is DATA, and these two are the first non-.go entries in
         # this list. A mutation to a definition file is only worth writing now
         # that `list` and `show` print what it holds -- see the appspec/05
         # catalog section below, which used to be a deferral banner and is now
         # two entries. gofmt is run over every touched file before the gate;
         # on a .cfg it fails to parse and writes nothing, which is the
         # behavior these rely on rather than tolerate.
         "internal/catalog/catalog.go", "internal/catalog/applications/mackup.cfg",
         # appspec/06's sync primitives, drift detection, the diff detail and
         # the property-list reader, added with their entries rather than with
         # the packages: each file here is edited by at least one mutation now
         # that backup and restore call into all four packages.
         #
         # Two files of those packages are deliberately ABSENT, because the
         # default for a file no mutation edits is to leave it out: it is
         # copied and restored for nothing. internal/syncfs/attributes.go, all
         # four of whose mutations are parked for the reason the appspec/06
         # banner below gives; and internal/plist/plist.go, because the ten
         # plist entries below reach the reader through binary.go, xml.go and
         # format.go and none of them edits the value model itself.
         #
         # It is a default and not a law, and the list holds two standing
         # exceptions so the next reader does not "tidy" one away. Both are
         # files a future entry is expected to edit, kept listed so that entry
         # is backed up on the day it lands rather than a commit later:
         # test/conformance/harness_unix_test.go, whose reason is spelled out
         # at the constants below, and the Makefile, which has been here
         # unedited since the rig landed in #2. Nothing forces the issue either
         # way -- the `untracked` guard further down refuses a mutation naming
         # a file NOT in this list, which is the direction that loses work.
         "internal/syncfs/syncfs.go", "internal/syncfs/state.go",
         "internal/drift/drift.go", "internal/drift/tree.go",
         "internal/drift/diff.go",
         "internal/plist/binary.go", "internal/plist/xml.go",
         "internal/plist/format.go"]

H = "test/conformance/harness_test.go"
C = "internal/ui/color.go"
U = "internal/cli/usage.go"
D = "internal/app/dispatch.go"
A = "internal/app/app.go"
# No constant for test/conformance/harness_unix_test.go, and no mutation edits
# it directly. That file is a safety net over Snapshot -- a bounded call, a recording
# reporter, a timeout arm -- and a net is only observable when the thing it
# catches misbehaves, so it is exercised by injecting into Snapshot rather than
# into itself. "the FIFO guard is removed" drives its bound and timeout arm end
# to end; "Snapshot complains instead of failing" drives its recorder branch.
# Both of those had never executed in this tree before those entries existed.
# It stays in FILES anyway, so an entry that does edit it later is backed up.
P = "internal/cli/parse.go"
V = "internal/version/version.go"
G = "test/conformance/argv_test.go"
CF = "internal/config/config.go"
ST = "internal/storage/storage.go"
FA = "internal/fault/fault.go"
AD = "internal/appdb/appdb.go"
IN = "internal/ini/ini.go"
HP = "internal/homepath/homepath.go"
EN = "internal/app/enumerate.go"
SG = "internal/app/stages.go"
SN = "internal/app/sync.go"
FD = "internal/app/folder.go"
EX = "internal/app/executor.go"
LK = "internal/app/link.go"
CT = "internal/catalog/catalog.go"
MK = "internal/catalog/applications/mackup.cfg"
SY = "internal/syncfs/syncfs.go"
SS = "internal/syncfs/state.go"
DR = "internal/drift/drift.go"
TD = "internal/drift/tree.go"
DF = "internal/drift/diff.go"
# The property-list reader is three files and not four: internal/plist/plist.go
# holds the value model, and no mutation edits it. See the FILES note above.
PB = "internal/plist/binary.go"
PX = "internal/plist/xml.go"
PF = "internal/plist/format.go"

# SURVIVES marks an entry that must NOT break the gate. Most entries are
# defects the suite has to catch; these are the opposite -- correct code that a
# guard once rejected. The doc guard demanded a bare name and so failed on
# "// A World is one throwaway environment", which is idiomatic Go and how the
# standard library writes doc comments. A guard that reddens the gate on
# correct code is a defect too, and nothing expressed it until this existed.
SURVIVES = object()


def repl(f, old, new):
    return ("repl", f, old, new)

def tail(f, marker, new):
    """Replace from `marker` to end of file with `new`.

    Only means what it says while the marker's declaration is the LAST one in
    the file, so apply() checks that rather than trusting it. It was true when
    the one entry using this was written and nothing said so; adding a function
    after ExpectUnchanged would have made that entry delete it too and then
    report either a phantom DOES-NOT-COMPILE or a kill credited to the wrong
    mutation.
    """
    return ("tail", f, marker, new)

def cut(f, start, end):
    """Remove everything from `start` up to, but not including, `end`.

    For deleting a whole switch arm or block. Replacing the BODY of an arm and
    calling it deleted is what left the FIFO branch standing while its entry
    reported a kill, so the shape that removes the arm itself has its own kind
    rather than a hand-written repl each time.

    Both markers are matched at the START OF A LINE, which is what makes the
    end anchor safe rather than lucky. A plain substring search for the end
    "\t\tdefault:" also matches inside a NESTED "\t\t\tdefault:", so an arm
    that later grew an inner switch would have been cut short -- leaving a
    mutation that still compiles but is not the one the entry names, and a kill
    credited to the wrong defect. That is the same failure the tail guard was
    added for one commit earlier, and it was left in place here.

    Line anchoring closes it because gofmt guarantees the indentation: a nested
    construct carries strictly more tabs, so a marker written at one
    indentation cannot match at a deeper one. "\t\tdefault:" already occurs
    twice in harness_test.go, so requiring a globally unique end -- the rule
    repl uses -- would reject the one entry that is correct today.
    """
    return ("cut", f, start, end)

MUTATIONS = [
 ("ExpectUnchanged gutted", [tail(H, "func (w *World) ExpectUnchanged(before Snapshot) {",
   "func (w *World) ExpectUnchanged(before Snapshot) {\n\tw.t.Helper()\n\t_ = before\n}\n")]),

 ("Snapshot content dropped", [repl(H,
   'snapshot[relative] = fmt.Sprintf("file %04o @%d %q", info.Mode().Perm(), stamp, content)',
   '_ = content\n\t\t\tsnapshot[relative] = fmt.Sprintf("file %04o @%d", info.Mode().Perm(), stamp)')]),

 ("Snapshot mtime dropped", [repl(H,
   "stamp := info.ModTime().UnixNano()", "stamp := int64(0)")]),

 ("ExpectUnchanged created branch deleted", [repl(H,
   '\tfor path, got := range after {\n\t\tif _, ok := before[path]; !ok {\n\t\t\tw.t.Errorf("%s was created; it holds %s", path, got)\n\t\t}\n\t}\n', '')]),

 ("ExpectUnchanged removed branch deleted", [repl(H,
   '\t\tgot, ok := after[path]\n\t\tif !ok {\n\t\t\tw.t.Errorf("%s was removed; it held %s", path, want)\n\t\t\tcontinue\n\t\t}\n',
   '\t\tgot, ok := after[path]\n\t\tif !ok {\n\t\t\tcontinue\n\t\t}\n')]),

 ("ExpectUnchanged changed branch deleted", [repl(H,
   '\t\tif got != want {\n\t\t\tw.t.Errorf("%s changed:\\n  before: %s\\n   after: %s", path, want, got)\n\t\t}\n',
   '\t\t_ = got\n')]),

 ("reaper Lstat -> Stat", [repl(H, "info, err := os.Lstat(path)", "info, err := os.Stat(path)")]),

 ("reaper no-op", [repl(H, "\t\tos.RemoveAll(path)\n", "\t\t_ = path\n")]),

 ("reaper ReadDir -> Glob", [repl(H,
   '\tentries, err := os.ReadDir(within)\n\tif err != nil {\n\t\treturn\n\t}\n\tfor _, entry := range entries {\n\t\tif !strings.HasPrefix(entry.Name(), buildDirPrefix) {\n\t\t\tcontinue\n\t\t}\n\t\tpath := filepath.Join(within, entry.Name())\n',
   '\tmatches, err := filepath.Glob(filepath.Join(within, buildDirPrefix+"*"))\n\tif err != nil {\n\t\treturn\n\t}\n\tfor _, path := range matches {\n')]),

 ("cmd.Env assignment deleted", [repl(H, "\tcmd.Env = w.environ()\n", "\t_ = w.environ()\n")]),

 ("Result copies a stale reporter", [
   repl(H, "\tw    *World\n\tArgs []string\n", "\tw    *World\n\tt    reporter\n\tArgs []string\n"),
   repl(H, "\treturn Result{\n\t\tw:    w,\n\t\tArgs: args,\n", "\treturn Result{\n\t\tw:    w,\n\t\tt:    w.t,\n\t\tArgs: args,\n"),
   repl(H, 'func (r Result) ExpectExit(code int) Result {\n\tr.w.t.Helper()\n\tif r.ExitCode != code {\n\t\tr.w.t.Errorf(',
           'func (r Result) ExpectExit(code int) Result {\n\tr.t.Helper()\n\tif r.ExitCode != code {\n\t\tr.t.Errorf('),
 ]),

 ("TMPDIR dropped from world env", [repl(H, '\t\t\t"TMPDIR": tmp,\n', '')]),

 ("doc reattached to a function", [repl(H,
   "func moduleRoot() (string, error) {", "func inserted() {}\n\nfunc moduleRoot() (string, error) {")],
   'the doc comment on inserted opens with "moduleRoot"'),

 ("doc reattached to a var block", [repl(H,
   "var readSourcesOnce sync.Once",
   "var (\n\tunrelatedA = 1\n\tunrelatedB = 2\n)\n\nvar readSourcesOnce sync.Once")],
   'opens with "readSourcesOnce", which is the name of the var declared elsewhere'),

 ("usage banner reworded", [repl(U, "\nUsage:\n", "\nUsage!\n")]),

 ("dispatch stub reworded", [repl(D, "is not implemented yet.", "is not implemented.")]),

 ("usage error exits 0", [repl(A,
   "\t\t\tstreams.Errln(cli.Usage)\n\t\t\treturn ExitFailure\n",
   "\t\t\tstreams.Errln(cli.Usage)\n\t\t\treturn ExitOK\n")]),

 ("method doc orphaned onto a var block", [repl(H,
   "// UseBinary switches this world to another of the builds under test.\nfunc (w *World) UseBinary(path string) { w.bin = path }",
   "// UseBinary switches this world to another of the builds under test.\nvar (\n\torphanedMethodDocA = 1\n\torphanedMethodDocB = 2\n)\n\nfunc (w *World) UseBinary(path string) { w.bin = path }")],
   'opens with "UseBinary", which is the name of the method declared elsewhere'),

 ("type doc orphaned onto a var block", [repl(H,
   "// the battery's \"Snapshot mtime dropped\" entry kills a record without it.\ntype Snapshot map[string]string",
   "// the battery's \"Snapshot mtime dropped\" entry kills a record without it.\nvar (\n\torphanedTypeDocA = 1\n\torphanedTypeDocB = 2\n)\n\ntype Snapshot map[string]string")],
   'opens with "Snapshot", which is the name of the type declared elsewhere'),

 ("Snapshot failure surfaces as a failure, not a timeout", [repl(H,
   '\t\t\tsnapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)',
   '\t\t\treturn fmt.Errorf("synthetic harness failure on %s", relative)')],
   'the snapshot failed: snapshotting the scratch root: synthetic harness failure'),

 ("config-file stops taking an argument", [repl(P,
   '\t"config-file": true,', '\t"config-file": false,')],
   None),

 ("--version with extra argv prints usage", [repl(A,
   "\tcase inv.Opts.Version:\n\t\tstreams.Say(ui.Progress, version.Banner())\n\t\treturn ExitOK\n",
   "\tcase inv.Opts.Version:\n\t\tif len(argv) > 1 {\n\t\t\tstreams.Outln(cli.Usage)\n\t\t\treturn ExitOK\n\t\t}\n\t\tstreams.Say(ui.Progress, version.Banner())\n\t\treturn ExitOK\n")],
   None),

 ("the argv scan does not stop at --help", [repl(A,
   "\tcase inv.Opts.Help:\n\t\tstreams.Outln(cli.Usage)\n\t\treturn ExitOK\n",
   "\tcase inv.Opts.Help:\n\t\tif len(argv) > 1 {\n\t\t\tstreams.Sayf(ui.Fatal, \"mackup: unrecognized option: %s\", argv[len(argv)-1])\n\t\t}\n\t\tstreams.Outln(cli.Usage)\n\t\treturn ExitOK\n")],
   None),

 ("declaration slid under a spec-level doc comment", [repl(H,
   "\t// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.\n\tmackupVCSBuildErr error",
   "\t// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.\n\tinsertedUnderTheComment error\n\n\tmackupVCSBuildErr error")],
   'the doc comment on insertedUnderTheComment opens with "mackupVCSBuildErr"'),

 # `ok && bi != nil`, and the nil check is the whole point of the entry rather
 # than defensive noise. Without it this mutation dereferenced a nil
 # *debug.BuildInfo in TestStringFallsBackWhenTheBuildCarriesNoBuildInfo --
 # which feeds readBuildInfo exactly (nil, true) -- and PANICKED the
 # internal/version test binary. A panic aborts the run, so
 # TestAnExplicitStampSurvivesAWorkingTreeBuild, the case written for this
 # regression and declared after it in the file, never executed. The entry
 # reported "killed" on a crash in an unrelated case: gutting the case this is
 # supposed to prove still left it green. That is rule 3 of the docstring
 # above, broken by the entry meant to exercise it, which is why the expected
 # diagnostic is no longer None.
 ("working-tree provenance outranks the linker stamp", [repl(V,
   "\tif value != \"\" {\n\t\treturn normalize(value)\n\t}\n",
   "\tif bi, ok := readBuildInfo(); ok && bi != nil {\n\t\tfor _, setting := range bi.Settings {\n\t\t\tif setting.Key == \"vcs.revision\" {\n\t\t\t\treturn Fallback\n\t\t\t}\n\t\t}\n\t}\n\tif value != \"\" {\n\t\treturn normalize(value)\n\t}\n")],
   'want the stamped "0.11.1" even though the build came from a working tree'),

 ("Snapshot mode dropped", [repl(H,
   'snapshot[relative] = fmt.Sprintf("file %04o @%d %q", info.Mode().Perm(), stamp, content)',
   'snapshot[relative] = fmt.Sprintf("file @%d %q", stamp, content)')],
   None),

 ("touchBuildDir no-op", [repl(H,
   "func touchBuildDir(dir string) {\n\tnow := time.Now()\n\tos.Chtimes(dir, now, now)\n}",
   "func touchBuildDir(dir string) {\n\t_ = dir\n}")],
   None),

 ("idiomatic article before the name is accepted", [repl(H,
   "// World is one throwaway environment: a home directory, an environment",
   "// A World is one throwaway environment: a home directory, an environment")],
   SURVIVES),

 ("a one-name parenthesized block may carry a collective comment", [repl(H,
   "// buildDirPrefix names this suite's build directories, so that a later run can\n// recognize one an earlier run abandoned.\nconst buildDirPrefix = \"macklebox-conformance-bin-\"",
   "// Naming of this suite's build directories, so that a later run can\n// recognize one an earlier run abandoned.\nconst (\n\tbuildDirPrefix = \"macklebox-conformance-bin-\"\n)")],
   SURVIVES),

 # The doc guard holds a test entry point to a weaker rule than everything else
 # -- it may open with any word the file does not declare -- because demanding
 # the function's own name reddened the gate on idiomatic Go. These two pin
 # both directions of that, because the obvious way to fix the false positive
 # (exempt test functions, or skip _test.go) would have made the first one
 # invisible, and all three defects the guard was written for were in a
 # _test.go file.
 # Both injected functions call NewWorld, and the empty bodies they had before
 # are why: TestEveryCaseThatBuildsNoWorldIsAccountedFor holds every case in
 # this package to building a world or carrying an allowlist entry, so an
 # injected `func TestOrdinaryComment(t *testing.T) {}` is not the "correct
 # code" this SURVIVES entry claims it is -- it reddened the gate, and the
 # entry reported FALSE-POSITIVE the first time both existed together. The
 # stranded-comment entry below it had the opposite problem, still killed but
 # now by two guards at once, which is the attribution rule 3 exists for. A
 # body that builds a world makes both injections ordinary cases again and
 # leaves each entry testing the one guard it names.
 ("doc stranded on an inserted test function", [repl(H,
   "func moduleRoot() (string, error) {",
   "func TestInserted(t *testing.T) { NewWorld(t) }\n\nfunc moduleRoot() (string, error) {")],
   'the doc comment on the test TestInserted opens with "moduleRoot"'),

 ("a test function may carry an ordinary explanatory comment", [repl(H,
   "// moduleRoot is the directory holding go.mod, found by walking up from the",
   "// Regression for the round-9 argv scan bug.\nfunc TestOrdinaryComment(t *testing.T) { NewWorld(t) }\n\n// moduleRoot is the directory holding go.mod, found by walking up from the")],
   SURVIVES),

 # The other half of the same record: recorded, but recorded RAW. The target
 # sits at the end of the record, which is where the blindness scan matches
 # contentsUnreadable with HasSuffix, so an unquoted target ending in that
 # marker turns every ExpectUnchanged in the world into a spurious "make the
 # fixture readable" over a file that already is.
 # --- appspec/07 stream routing and colour (macklebox-foundation-waw.3) ------
 #
 # Every entry here is killable by the CONFORMANCE suite, not only by the unit
 # tests, which is the bar an internal/ mutation has to clear: `make check` runs
 # the unit packages first and stops there, so a mutation the unit tests catch
 # says nothing about whether the rig can see it, and the battery reports
 # RIG-BLIND for one that cannot.
 #
 # One appspec/07 property is deliberately NOT here: reset-safety, the
 # re-application of a colour after an embedded reset. No message the program
 # emits today contains an embedded reset -- the composed shapes arrive with
 # the diff detail and the per-app verbose header in later tickets -- so the
 # rig genuinely cannot observe it yet, and an entry for it would report
 # RIG-BLIND accurately. internal/ui's unit tests carry it until a composed
 # message exists; add the entry with that message.

 # Each `expect` here names the UNIT diagnostic, not the conformance one, and
 # that is not a compromise -- it is what the string is matched against. The
 # battery matches expect against `make check`, and `make check` runs the unit
 # packages FIRST and stops at the first failure, so a mutation to internal/
 # that the unit tests kill never produces a line of conformance output for an
 # expect to match. Every one of these six was first written with the wording
 # of the conformance case it was really aimed at and every one of them
 # reported WRONG-DIAGNOSTIC on a mutation the gate had killed correctly. The
 # rig half of the claim is not lost by writing it this way: it is carried by
 # the separate `make conformance` run below, which is what turns a
 # unit-tests-only kill into RIG-BLIND, and the [rig: ...] note on a kill line
 # names the conformance case that saw it.

 ("colour is not emitted at all", [repl(C,
   '\topen := escape + spec.sgr + "m"\n\treturn open + embeddedReset.ReplaceAllString(text, reset+open) + reset',
   '\t_ = spec\n\treturn text')],
   'with no colour to a non-terminal stream'),

 ("a coloured string is not terminated with a reset", [repl(C,
   '\treturn open + embeddedReset.ReplaceAllString(text, reset+open) + reset',
   '\treturn open + embeddedReset.ReplaceAllString(text, reset+open)')],
   'want the newline after the reset'),

 # appspec/07 gives fatal errors bright red and non-fatal copy failures red,
 # and colour alone conveys the level -- so flattening the two erases the only
 # thing that says whether the program stopped.
 ("a fatal error takes the non-fatal colour", [repl(C,
   'Fatal:       {sgr: "91"', 'Fatal:       {sgr: "31"')],
   'appspec/07 distinguishes bright red 91 from red 31'),

 # The stream is contract, not cosmetics. This is that defect in its purest
 # form: the message is right, the colour is right, the stream is wrong.
 ("a fatal diagnostic is routed to stdout", [repl(C,
   'Fatal:       {sgr: "91", on: toStderr', 'Fatal:       {sgr: "91", on: toStdout')],
   'Fatal stream = 0, want 1'),

 ("the version banner loses its colour", [repl(A,
   'streams.Say(ui.Progress, version.Banner())', 'streams.Outln(version.Banner())')],
   'want the banner coloured even though the stream is not a terminal'),

 # The opposite mistake to the one above, and the reason the decision is
 # written down in the program and in the case: appspec/07 lists no level for
 # the argument parser's usage text, so colouring it invents one.
 ("the usage block is coloured", [repl(A,
   '\tcase inv.Opts.Help:\n\t\tstreams.Outln(cli.Usage)',
   '\tcase inv.Opts.Help:\n\t\tstreams.Say(ui.Progress, cli.Usage)')],
   'carries colour; the usage block has no level in appspec/07'),

 # appspec/07: the program does not condition colour on whether stdout is a
 # TTY. The rig runs every process down a pipe, so a program that consulted a
 # terminal would emit nothing here.
 ("colour is conditioned on a terminal", [repl(A,
   'func runArgv(argv []string, streams *ui.IO) int {',
   'func stdoutIsTerminal() bool {\n\tinfo, err := os.Stdout.Stat()\n\treturn err == nil && info.Mode()&os.ModeCharDevice != 0\n}\n\nfunc runArgv(argv []string, streams *ui.IO) int {'),
   repl(A, 'import (\n\t"errors"', 'import (\n\t"errors"\n\t"os"')],
   'names "ModeCharDevice"'),

 ("the symlink target is recorded unquoted", [repl(H,
   'snapshot[relative] = fmt.Sprintf("symlink %04o @%d -> %q", info.Mode().Perm(), stamp, target)',
   'snapshot[relative] = fmt.Sprintf("symlink %04o @%d -> %s", info.Mode().Perm(), stamp, target)')],
   'nothing here is blind'),

 ("the symlink target is not recorded", [repl(H,
   '\t\t\ttarget, err := os.Readlink(path)\n\t\t\tif err != nil {\n\t\t\t\treturn err\n\t\t\t}\n',
   '\t\t\ttarget := "constant"\n\t\t\t_ = os.Readlink\n')],
   # The tail of that Errorf rather than its %q-rendered want, which now
   # carries Go's own escaping of the quoted target and would have to be
   # re-encoded here every time the record's quoting changes. This clause is
   # unique to the one assertion the mutation has to trip.
   'without the target a re-pointed link is indistinguishable from an untouched one'),

 # Removes the arm, not its body. An earlier entry replaced the body and so
 # left the branch itself standing, which is why the 30s bound, the recording
 # reporter and the timeout arm in harness_unix_test.go had never once
 # executed. This is the defect that whole file exists for: Snapshot opens a
 # FIFO nobody is writing to and BLOCKS, and the kill is the bound firing.
 # Slow on purpose -- it takes the full 30s -- and the only entry that does.
 ("the FIFO guard is removed", [cut(H,
   "\t\tcase !info.Mode().IsRegular():", "\t\tdefault:")],
   'so it blocked rather than failed'),

 ("Snapshot complains instead of failing", [repl(H,
   '\t\t\tsnapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)',
   '\t\t\tw.t.Errorf("snapshot: unexpected file type at %s", relative)\n\t\t\tsnapshot[relative] = fmt.Sprintf("%s %04o @%d", info.Mode().Type(), info.Mode().Perm(), stamp)')],
   'the snapshot returned but reported'),

 # readImplementationSources is the rig's primary cache-honesty mechanism and
 # the one harness_test.go's header says must never be lost, and nothing
 # injected at it. Both of its recorded past failures are injectable: the walk
 # narrowed back to a guess at where the program lives, and the call moved out
 # of a case to where cmd/go does not record its reads.
 ("the source walk is narrowed to internal", [repl(H,
   "\tread := map[string]bool{}",
   '\troot = filepath.Join(root, "internal")\n\tread := map[string]bool{}')],
   "so the cache key does not track the program"),

 # The suite compiles on the host on every run, and the host is unix, so a case
 # in the untagged half reaching into the unix-tagged half was invisible to the
 # whole gate until a contributor on another GOOS met it. That is what the
 # second GOOS in the vet target is for, and this is what proves it fires. The
 # host vet this battery runs first passes, exactly as the gate's first vet
 # did; the kill comes from the cross-GOOS one.
 ("an untagged case reaches into the unix-only harness", [repl(G,
   '\tlink := world.Path("link")',
   '\t_ = snapshotBound\n\tlink := world.Path("link")')],
   "undefined: snapshotBound"),

 ("the forced-stamp GOFLAGS drops what go env carries", [repl(H,
   '\treturn strings.TrimSpace(strings.TrimSpace(string(reported)) + " -buildvcs=true"), nil',
   '\t_ = reported\n\treturn "-buildvcs=true", nil')],
   "dropped -mod=mod from `go env GOFLAGS`"),

 # Two mechanisms, one contract, and until these existed only one of them was
 # falsifiable: touchBuildDir had a case, the loop and the wiring had none.
 ("the build directory refresher is never started", [repl(H,
   "\tkeepBuildDirFresh(dir)\n", "\t_ = keepBuildDirFresh\n")],
   "want the directory holding its binaries"),

 ("the build directory refresher touches once and stops", [repl(H,
   "\tfor {\n\t\tselect {\n\t\tcase <-stop:\n\t\t\treturn\n\t\tcase <-time.After(interval):\n\t\t\ttouchBuildDir(dir)\n\t\t}\n\t}\n",
   "\tselect {\n\tcase <-stop:\n\tcase <-time.After(interval):\n\t\ttouchBuildDir(dir)\n\t}\n")],
   "it is not looping"),

 ("the version banner gains a suffix", [repl(V,
   'return "Mackup " + String()', 'return "Mackup " + String() + "-extra"')],
   None),

 # The two halves of runningUnderCI, separately. Before it existed this was two
 # inline switches over the raw string, so CI=False and CI=no hard-failed the
 # gate the "false" case had been added to keep green -- a defect that lived in
 # code nothing drove, since neither switch is reachable on a machine that has
 # a VCS-stamped build. Narrowing either half must now be caught.
 ("runningUnderCI stops normalizing its value", [repl(H,
   "strings.ToLower(strings.TrimSpace(value))", "value")],
   "was read as a CI run"),

 ("runningUnderCI narrows its falsy set", [repl(H,
   '\tcase "", "false", "0", "no", "off":', '\tcase "", "false", "0":')],
   "was read as a CI run"),

 # casesThatBuildNoWorld replaced a comment that named which cases skip
 # readImplementationSources, and went stale twice doing it. Both directions of
 # the guard that replaced it are pinned, because a list that only rejects
 # additions rots the same way the comment did.
 # The key is renamed rather than the line deleted, deliberately: gofmt pads
 # the map's keys to align its values, so an anchor carrying that padding
 # breaks -- loudly, but for a reason having nothing to do with what the entry
 # tests -- the next time an entry is added. A key substring carries no
 # padding. Renaming trips both directions at once; the expected diagnostic
 # names the one this entry is for.
 ("the no-world allowlist loses an entry", [repl(G,
   '"TestTheReaperJudgesTheEntryItRemoves":',
   '"TestTheReaperJudgesTheEntryItRemovedOnce":')],
   "never calls NewWorld"),

 # Re-anchored on macklebox-copy-sync-dpz.3: the scan no longer tests the
 # identifier against the literal "NewWorld". It resolves the set of helpers
 # that themselves build a world (newSyncWorld and friends) into worldBuilders
 # and tests membership, so the mutation that makes the scan see a world
 # everywhere is a predicate that is true of every identifier. The entry is
 # unchanged in what it claims; only the line it points at moved.
 ("the no-world scan sees a world in every case", [repl(G,
   "isIdentifier && builders[identifier.Name]", 'isIdentifier && identifier.Name != ""')],
   "no longer exists or now calls NewWorld"),

 # Both of these are killed only by the SECOND conformance run, the -trimpath
 # one. Under the plain run they pass, which is exactly how the guard's own
 # runtime.Caller shipped: moduleRoot has refused runtime.Caller by name since
 # before either entry existed, and nothing checked. The pair covers both
 # sites, because the claim is about the suite finding its files, not about one
 # function.
 # Two edits each: the import as well as the body, because neither file
 # imports runtime any more and a mutation that does not compile proves
 # nothing (rule 1).
 ("the no-world guard finds its directory by runtime.Caller", [
   repl(G, '\t"path/filepath"\n', '\t"path/filepath"\n\t"runtime"\n'),
   repl(G, '\tdir, err := os.Getwd()\n\tif err != nil {\n\t\tt.Fatalf("locating the package directory: %v", err)\n\t}',
      '\t_, thisFile, _, _ := runtime.Caller(0)\n\tdir := filepath.Dir(thisFile)')],
   "github.com/promptctl/macklebox/test/conformance"),

 # Snapshot's permission branch had no case at all, so ExpectUnchanged
 # compared two records of an unlistable directory that were equal because
 # both were blind. Dropping the report leaves the collection loop standing
 # and says nothing, which is what the defect actually looked like.
 #
 # Written first as Errorf -> Logf, which is the obvious spelling and does
 # not compile: w.t is the reporter interface captureReport swaps in, and it
 # carries Helper, Errorf and Fatalf but no Logf. Rule 1 caught it as
 # DOES-NOT-COMPILE, which is what rule 1 is for.
 ("the blind-directory report is dropped", [repl(H,
   'w.t.Errorf("%s", message)', '_ = message')],
   "no report names home/locked"),

 # Snapshot's third permission shape: a 0400 parent, whose children ReadDir
 # lists and lstat cannot touch. This branch did not exist -- the walk
 # returned the error, WalkDir aborted, and the harness fataled -- so the
 # mutation is the code as it was, and the kill is the case that shape now
 # has. The two entries are separate because they fail differently: dropping
 # the branch fatals the harness, dropping the report leaves it silent.
 ("Snapshot aborts on an entry it cannot stat", [repl(H,
   '\t\t\tif errors.Is(err, fs.ErrPermission) {\n\t\t\t\tsnapshot[relative] = entryUnstatable\n\t\t\t\treturn nil\n\t\t\t}\n\t\t\treturn err',
   '\t\t\treturn err')],
   "snapshotting the scratch root"),

 # Anchored on the predicate rather than the whole row: the row carries a
 # long message and a closure, both of which have already been rewritten once
 # (BROKEN-ANCHOR, the next full run after they were), while the predicate is
 # the part that decides anything. `false` disables this shape alone and
 # leaves the other one reporting, which is what separates this entry from
 # "the blindness scan matches anywhere in the record".
 ("the unstatable-entry blindness goes unreported", [repl(H,
   'strings.HasPrefix(record, entryUnstatable)', 'false')],
   "no report names home/sealed/cfg"),

 # The unreadable-file record's size, and the anchoring of the blindness
 # scan. Both were added because mode-and-stamp alone, and Contains, are the
 # obvious spellings -- so both are the shape a later edit drifts back to.
 ("the unreadable-file record drops its size", [repl(H,
   'fmt.Sprintf("file %04o %dB @%d <unreadable>", info.Mode().Perm(), info.Size(), stamp)',
   'fmt.Sprintf("file %04o @%d <unreadable>", info.Mode().Perm(), stamp)')],
   "no report names home/secret"),

 ("the blindness scan matches anywhere in the record", [
   repl(H, 'strings.HasPrefix(record, entryUnstatable)', 'strings.Contains(record, entryUnstatable)'),
   repl(H, 'strings.HasSuffix(record, contentsUnreadable)', 'strings.Contains(record, contentsUnreadable)')],
   "over a readable file whose CONTENT holds the marker text"),

 ("moduleRoot walks up from runtime.Caller", [
   repl(H, '\t"path/filepath"\n', '\t"path/filepath"\n\t"runtime"\n'),
   repl(H, '\tdir, err := os.Getwd()\n\tif err != nil {\n\t\treturn "", fmt.Errorf("locating the module root: %v", err)\n\t}',
      '\t_, thisFile, _, _ := runtime.Caller(0)\n\tdir := filepath.Dir(thisFile)')],
   "no go.mod above"),

 # --- appspec/03 config and appspec/04 storage (macklebox-resolvers-5iw.2) ---
 #
 # Entries here, and not a deferral banner like the catalog one below, because
 # config load is the one stage that IS observable today: appspec/02 puts every
 # subcommand except --help and --version behind it, so a config defect aborts
 # `list` at the boundary with a diagnostic the rig reads. Each of the five
 # below was applied in a copied tree and put to `make conformance` separately
 # before it was written down; none reported RIG-BLIND.
 #
 # Each `expect` names the diagnostic `make check` actually prints for that
 # mutation, which for the first three is a UNIT one -- internal/config and
 # internal/storage run before the conformance suite and stop it -- and for the
 # last two is the conformance one, because there the unit packages pass. That
 # asymmetry is not a slip; it is the rule the appspec/07 banner above states,
 # applied per entry rather than assumed. The [rig: ...] note on each kill line
 # is what carries the conformance half of the claim.
 #
 # One property is deliberately NOT here: Config.Scope's "the denylist wins
 # over the allowlist". It was deferred to macklebox-resolvers-5iw.4 on the
 # expectation that wiring `list` to the catalog would make Scope observable.
 # THAT EXPECTATION WAS WRONG, and .4 is where it was found out, so the
 # deferral is corrected here rather than satisfied.
 #
 # `list` does not call Scope and must not. appspec/03 says of
 # `[applications_to_sync]`, in as many words, that "this section does not
 # affect `list` output", and appspec/05 defines what `list` prints as "the set
 # of all keys assembled by the discovery rules" -- so narrowing it would be
 # the defect, not the fix. Two cases now pin that reading: the unit
 # TestListIsNotNarrowedByTheConfigApplicationLists and the conformance
 # TestListAndShowAreNarrowedByNothingInTheConfig. The mutation that ADDS the
 # Scope call to `list` is written in the .4 section below, which is the
 # observable half of this property that .4 could honestly supply.
 #
 # That left the property owed to whichever ticket gave Scope its first
 # non-test caller, and macklebox-copy-sync-dpz.3 is it: internal/app/scope.go
 # narrows the sync fan-out with cfg.Scope(apps.Keys()), so the rule is now
 # observable as which files a `backup` with no named application writes. The
 # entry below is that debt paid.
 #
 # The mutation is an ADDITION, like the appspec/03 trap further down, because
 # the two checks in Scope both `continue` and exchanging them changes nothing:
 # what makes the denylist lose is an allowlist entry excusing a key from it.
 # That is precisely the reading appspec/03 forbids -- "an app appearing in
 # BOTH lists is ignored" -- and it is the one a reimplementation reaches for,
 # since an explicit allowlist entry looks like the more specific instruction.
 ("an allowlist entry excuses a key from the denylist", [repl(CF,
   "\t\tif c.ignore[key] {", "\t\tif c.ignore[key] && !c.allow[key] {")],
   "FAIL: TestTheDenylistWinsOverTheAllowlist"),
   # rig: TestTheDenylistWinsOverTheAllowlist

 # appspec/03's environment table, and the one rule in this tree that TWO
 # stages read from a single implementation: config discovery and database
 # assembly both resolve home through homepath.Require. That is what makes the
 # relative arm worth an entry of its own -- an absolute HOME is what every
 # fixture and every developer machine has, so the arm is dead weight until
 # something states it, and deleting it changes nothing a passing suite would
 # notice unless a case supplies a relative one.
 ("a relative HOME is accepted", [repl(HP,
   '\tif !filepath.IsAbs(home) {\n'
   '\t\treturn "", fault.Unguardedf("HOME is %q, which is not an absolute path", home)\n'
   '\t}\n', '')],
   "FAIL: TestAnUnsetOrRelativeHomeIsRefusedInTheUnguardedRegime"),

 # appspec/03 "Home-directory containment". Deleting the check is the shape a
 # reimplementation reaches for when the check looks like a redundant guard on
 # a path the program itself computed -- it is not, because two of the three
 # discovery candidates come from the environment.
 ("the home-containment check is deleted", [repl(CF,
   '\tif !homepath.Inside(path, home) {\n'
   '\t\t// appspec/03 "Home-directory containment": checked at construction\n'
   '\t\t// and applied to a discovered path as much as to an explicitly named\n'
   '\t\t// one, which is why this sits outside configPath\'s two branches\n'
   '\t\t// rather than inside the explicit one.\n'
   '\t\treturn nil, fault.Guardedf("The config file \'%s\' is not in your home directory. Aborting.", path)\n'
   '\t}\n', '')],
   'Load succeeded, want "Error: The config file '),

 # appspec/03 "Discovery and precedence": ~/.mackup.cfg "always wins if
 # present". Prepending rather than appending is a one-word slip that leaves
 # all three candidates in play and every single-candidate case passing, so it
 # is only visible to a case that sets TWO of them at once.
 ("MACKUP_CONFIG is checked before the home config", [repl(CF,
   "\t\tcandidates = append(candidates, homepath.Absolute(homepath.Expand(env.MackupConfig, home)))",
   "\t\tcandidates = append([]string{homepath.Absolute(homepath.Expand(env.MackupConfig, home))}, candidates...)")],
   'the config read was "from-mackup-config", want the home directory\'s ~/.mackup.cfg'),

 # The appspec/04 trap, and the only entry here for a mutation that ADDS
 # correct-looking code rather than removing it. appspec/04 clause 2 says the
 # file_system engine must NOT check that its path exists, because the uniform
 # "Unable to find the storage folder: <path>" belongs to the environment gate
 # -- so a stat here does not break a postcondition, it moves a message to the
 # wrong stage. Nothing in the tree but the two tests this kills says so.
 #
 # It went RIG-BLIND for one commit, and the reason is worth keeping: once the
 # gate existed, a stat in the engine produced the gate's OWN line, so the
 # conformance case asserting that line could not tell the two stages apart.
 # Two stages are distinguishable only where they disagree, and between these
 # two stands database assembly --
 # TestTheStorageRootIsCheckedAfterTheDatabaseIsAssembled is what restored the
 # rig's sight, and it is still the only case that has it.
 ("the file_system engine gains an existence check", [repl(ST,
   '\tif filepath.IsAbs(f.path) {\n'
   '\t\treturn f.path, nil\n'
   '\t}\n'
   '\treturn filepath.Join(f.home, f.path), nil\n'
   '}',
   '\troot := f.path\n'
   '\tif !filepath.IsAbs(root) {\n'
   '\t\troot = filepath.Join(f.home, root)\n'
   '\t}\n'
   '\tif info, err := os.Stat(root); err != nil || !info.IsDir() {\n'
   '\t\treturn "", fault.Guardedf("Unable to find the storage folder: %s", root)\n'
   '\t}\n'
   '\treturn root, nil\n'
   '}')],
   'want the path itself: appspec/04 forbids an existence check in this engine'),

 # appspec/07 requires every coloured string to end in a reset, and two guarded
 # rows -- the provider block and the legacy-config refusal -- are multi-line.
 # One Say per diagnostic colours the first line and leaves the rest bare, and
 # the collapse is invisible to every single-line failure, which is most of
 # them. The import goes with it because compiling is a precondition of the
 # mutation counting: an unused "strings" is a build break, and a build break
 # proves nothing about the rig.
 ("reportFatal writes a multi-line diagnostic in one Say", [
   repl(A, '\tfor _, line := range strings.Split(fault.Diagnostic(err), "\\n") {\n'
           '\t\tstreams.Say(ui.Fatal, line)\n'
           '\t}',
           "\tstreams.Say(ui.Fatal, fault.Diagnostic(err))"),
   repl(A, '\t"errors"\n\t"strings"\n', '\t"errors"\n')],
   'want it to end in a reset; appspec/07: every colored string is terminated with a reset'),

 # The regime split of appspec/01 section 6 and appspec/02, which both PERMIT
 # collapsing the unguarded rows into clean exits. This program declines that
 # permission and keeps the two shapes apart -- "Error: <sentence>" for
 # guarded, "mackup: <text naming the offending value>" for unguarded -- and
 # the reasoning is written out in internal/fault/fault.go. A split nothing can
 # observe is not a split, so this entry is what makes the decision hold:
 # giving the unguarded regime the guarded opener has to redden the gate.
 ("the unguarded regime takes the guarded opener", [repl(FA,
   '\treturn &Error{Regime: Unguarded, Message: "mackup: " + fmt.Sprintf(format, args...)}',
   '\treturn &Error{Regime: Unguarded, Message: "Error: " + fmt.Sprintf(format, args...)}')],
   'want a shape distinct from the guarded rows'),

 # Raised in review on PR #5, and the entry exists because the reviewer was
 # RIGHT about the hole while wrong about the behaviour. appspec/04 chooses a
 # candidate by "whichever DB file exists", not by which query succeeds, so a
 # preferred file that cannot be parsed ends the resolution. The old test for
 # that guarantee left the fallback path EMPTY, which made both behaviours fail
 # identically -- a vacuous assertion the whole battery exists to find, and one
 # it could not find because no entry injected the fall-through. It does now.
 ("an unreadable preferred database falls through to the fallback", [repl(ST,
   '\tif db == "" {\n'
   '\t\treturn "", unlocatable("Google Drive install")\n'
   '\t}\n'
   '\troot, err := sqlite.Lookup(db, driveTable, driveKeyColumn, driveKey, driveValueColumn)\n'
   '\tif err != nil || !usableText(root) {\n'
   '\t\treturn "", unlocatable("Google Drive install")\n'
   '\t}\n'
   '\treturn root, nil',
   '\t_ = db\n'
   '\tfor _, candidate := range []string{\n'
   '\t\tfilepath.Join(support, drivePreferred),\n'
   '\t\tfilepath.Join(support, driveFallback),\n'
   '\t} {\n'
   '\t\troot, err := sqlite.Lookup(candidate, driveTable, driveKeyColumn, driveKey, driveValueColumn)\n'
   '\t\tif err == nil && usableText(root) {\n'
   '\t\t\treturn root, nil\n'
   '\t\t}\n'
   '\t}\n'
   '\treturn "", unlocatable("Google Drive install")')],
   'want a failure: the preferred database was chosen and could not be read'),

 # --- appspec/05 the application database (macklebox-resolvers-5iw.3) --------
 #
 # Entries here rather than the deferral banner the catalog got below, and the
 # difference is exactly the bar that banner states: the database's two
 # REJECTIONS reach the boundary today even though its contents do not. A
 # definition holding an absolute path aborts every command with a diagnostic
 # naming the path, so `make conformance` can see a discovery or precedence
 # defect through it -- which is what test/conformance/appdb_test.go's header
 # calls the channel. Each of the seven below was applied in a copied tree and
 # put to `make conformance` separately before it was written down; none
 # reported RIG-BLIND.
 #
 # Four of the expects name the failing CASE rather than a sentence from its
 # message, and that is deliberate rather than lazy. The kill for those is a
 # helper's fatal -- "the database assembled with N applications; want a
 # refusal" -- whose text is shared by every refusal case and whose count moves
 # with the catalog. The rule the appspec/07 banner states is that a kill by an
 # unrelated case must not count as coverage; naming the case satisfies it more
 # tightly than a shared sentence would, and does not break when the catalog
 # grows by one application.
 #
 # Two properties were deferred from here to macklebox-resolvers-5iw.4 --
 # definition keys being lowercased, and a definition file read under
 # ini.LowercaseKeys so its paths lose their case -- because neither reaches
 # the boundary until `list` and `show` print keys and paths. Both are WRITTEN
 # NOW, at the end of this section, and neither reports RIG-BLIND any more.
 # They are the case-policy pair of appspec/03 and appspec/05, which after the
 # internal/ini extraction hangs on one argument at two call sites.

 # appspec/05's absolute-path rejection, which it calls "load-bearing for the
 # sync engine's safety" rather than input hygiene: appspec/06 never re-checks
 # that a path is home-relative. Deleting it is the shape a reimplementation
 # reaches for when the check reads as validation of data the project ships --
 # and the project's own 614 definitions never trip it, which is what makes the
 # deletion look free.
 ("the absolute-path rejection is deleted", [repl(AD,
   '\tif strings.HasPrefix(path, "/") {\n'
   '\t\treturn fault.Unguardedf("Unsupported absolute path: %s", path)\n'
   '\t}\n'
   '\treturn nil',
   '\t_ = path\n'
   '\treturn nil')],
   "FAIL: TestAnAbsolutePathIsRefusedInEitherSection"),

 # The other rejection appspec/05 names, in the same load-bearing pair.
 ("the XDG containment check is deleted", [repl(AD,
   '\tbase := homepath.ConfigHome(env.XDGConfigHome, home)\n'
   '\tif !homepath.Inside(base, home) {\n'
   '\t\treturn "", fault.Unguardedf("$XDG_CONFIG_HOME must be somewhere within your home directory: %s", base)\n'
   '\t}\n'
   '\treturn base, nil',
   '\treturn homepath.ConfigHome(env.XDGConfigHome, home), nil')],
   "FAIL: TestAnXDGConfigHomeOutsideTheHomeDirectoryIsRefused"),

 # The same check moved rather than removed, which is the version that looks
 # correct: relativizing an XDG entry is where the base is USED, so checking it
 # there reads as tighter code. It is not, and appspec/05 says why -- "this
 # check fires while assembling the database, so it blocks every command" is
 # unconditional, while a lazy check fires only while some definition still
 # carries an [xdg_configuration_files] section. Every shipped fixture has one,
 # so the mutation survives every case but the two that were written for the
 # ORDER.
 ("the XDG base is checked lazily rather than up front", [
   repl(AD,
     '\tbase := homepath.ConfigHome(env.XDGConfigHome, home)\n'
     '\tif !homepath.Inside(base, home) {\n'
     '\t\treturn "", fault.Unguardedf("$XDG_CONFIG_HOME must be somewhere within your home directory: %s", base)\n'
     '\t}\n'
     '\treturn base, nil',
     '\treturn homepath.ConfigHome(env.XDGConfigHome, home), nil'),
   repl(AD,
     '\tfor _, path := range parsed.Section(xdgConfigurationFiles).Keys() {\n'
     '\t\tif err := refuseAbsolute(path); err != nil {\n'
     '\t\t\treturn application{}, err\n'
     '\t\t}\n',
     '\tfor _, path := range parsed.Section(xdgConfigurationFiles).Keys() {\n'
     '\t\tif !homepath.Inside(xdgBase, home) {\n'
     '\t\t\treturn application{}, fault.Unguardedf("$XDG_CONFIG_HOME must be somewhere within your home directory: %s", xdgBase)\n'
     '\t\t}\n'
     '\t\tif err := refuseAbsolute(path); err != nil {\n'
     '\t\t\treturn application{}, err\n'
     '\t\t}\n'),
   ],
   "want the $XDG_CONFIG_HOME refusal to come first"),

 # appspec/05's three-tier precedence, read backwards: the built-in set taken
 # first means a user's ~/.mackup/vim.cfg never replaces anything. The whole
 # point of the two user directories is the override, and a program with this
 # defect still lists 614 applications and still assembles cleanly.
 ("definition precedence is reversed", [repl(AD,
   '\t\t{files: os.DirFS(legacyPath), origin: legacyPath},\n'
   '\t\t{files: os.DirFS(xdgPath), origin: xdgPath},\n'
   '\t\t{files: catalog.Definitions(), origin: builtinOrigin},',
   '\t\t{files: catalog.Definitions(), origin: builtinOrigin},\n'
   '\t\t{files: os.DirFS(xdgPath), origin: xdgPath},\n'
   '\t\t{files: os.DirFS(legacyPath), origin: legacyPath},')],
   "want the definition from ~/.mackup"),

 # The "*.cfg" of appspec/05 "Discovery" read as a suffix test rather than as a
 # glob. It is the more obvious spelling of the two, and it takes a
 # Finder-dropped "._vim.cfg" as an application called "._vim" -- which is what
 # the definitionFiles doc comment says the dot rule earns itself against.
 ("a dotfile is taken as a definition", [repl(AD,
   '\t\tif strings.HasPrefix(name, ".") || !strings.HasSuffix(name, extension) {',
   '\t\tif !strings.HasSuffix(name, extension) {')],
   "FAIL: TestOnlyCfgFilesDirectlyInADirectoryAreDefinitions"),

 # appspec/05: "Only files ending in .cfg are considered." Folding the
 # comparison is the shape a developer on a case-insensitive filesystem writes
 # without noticing, because on their machine the two readings agree about
 # every file that already exists. It shares its expect with the entry above:
 # both are killed by the same case, which is the case that owns the rule, and
 # nothing else in the tree distinguishes them.
 ("the .cfg suffix is matched case-insensitively", [repl(AD,
   '\t\tif strings.HasPrefix(name, ".") || !strings.HasSuffix(name, extension) {',
   '\t\tif strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), extension) {')],
   "FAIL: TestOnlyCfgFilesDirectlyInADirectoryAreDefinitions"),

 # internal/ini serves BOTH file kinds -- appspec/03's config and appspec/05's
 # definitions -- so one dialect defect now reaches two stages, and this entry
 # is what makes that sharing observable rather than merely tidy.
 #
 # It is also the entry that paid for itself before it was written. Under this
 # mutation `make conformance` exited 0: every comment fixture in
 # TestTheConfigFileFormatIsReadAsAppspec03Describes asserted that stderr
 # CONTAINED "from-config", and an engine read as "from-config ; a comment"
 # contains it. The case that existed to check the comment rule passed over a
 # parser with no comment rule. The case now asserts the whole diagnostic, and
 # this entry is what keeps it honest.
 ("the shared parser stops stripping comments", [repl(IN,
   '\tif at := strings.IndexAny(line, ";#"); at >= 0 {\n'
   '\t\treturn line[:at]\n'
   '\t}\n'
   '\treturn line',
   '\treturn line')],
   "with the comment stripped and the value trimmed"),

 # The case-policy pair, deferred from macklebox-resolvers-5iw.3 and written
 # here now that `list` prints keys and `show` prints paths. appspec/03
 # lowercases application-list keys; appspec/05 does NOT lowercase either a
 # definition's filename or the paths inside it, and states the asymmetry
 # outright: "application-list keys are case-normalized; definition file paths
 # are case-exact."
 #
 # The half that key-lowercasing looks free on is the shipped catalog: all 614
 # keys are already lowercase, so folding them changes nothing about the
 # program's own data and only breaks a user's Mixed.cfg.
 ("definition keys lowercased", [repl(AD,
   '\t\t\tkey := strings.TrimSuffix(name, extension)',
   '\t\t\tkey := strings.ToLower(strings.TrimSuffix(name, extension))')],
   "the key was lowercased; appspec/05 makes the basename the key"),

 # The other half, and the newer risk: one argument at one of two call sites.
 # internal/ini takes the case policy as a parameter precisely so the two file
 # kinds can differ, which means the difference between them is a single
 # identifier -- and a reader tidying the two call sites into agreement makes
 # exactly this mutation. Nothing about the shipped catalog notices: a path
 # like `.vimrc` is unchanged by folding, and it takes a definition holding
 # `.Xresources` or a `Library/Preferences` path to tell the readings apart.
 ("a definition is read with the config case policy", [repl(AD,
   '\tparsed := ini.Parse(string(content), ini.ExactKeys)',
   '\tparsed := ini.Parse(string(content), ini.LowercaseKeys)')],
   "want [.Xresources Library/Preferences/My.plist] with their case preserved"),

 # --- appspec/05 the built-in application catalog (macklebox-resolvers-5iw.1) -
 #
 # This was a deferral banner recording an absence: internal/catalog's 614
 # embedded definitions were pinned against appspec/appendix-application-names.md
 # by unit tests alone, and nothing in the catalog reached the program's
 # boundary, so a mutation to a .cfg was killed by internal/catalog and
 # reported RIG-BLIND -- accurately, and uselessly. macklebox-resolvers-5iw.4
 # wired `list` and `show` to it, which is what the deferral was waiting for,
 # so the two entries that banner named are written below and neither reports
 # RIG-BLIND.
 #
 # They are this file's first mutations to DATA rather than to code, and the
 # distinction is worth naming: a definition file is not a place a defect gets
 # "introduced" by a careless edit so much as a place one arrives by a
 # regeneration, a merge, or a script that rewrites the directory. That is
 # exactly the shape both entries take.

 # appspec/05 gives Mackup a definition of its own, and whole-Mackup mode
 # (appspec/06) syncs the user's config through it: if `.mackup.cfg` is not in
 # the file set, `mackup backup` silently stops carrying the user's own
 # settings. The `show mackup` claim is the observable half, and it is the only
 # thing standing between that path and a one-line data edit.
 ("the mackup definition loses its own config file",
  [repl(MK, "\n.mackup\n.mackup.cfg\n", "\n.mackup\n")],
  "the mackup self-definition does not list .mackup.cfg"),

 # A definition file going missing from the shipped set -- what a regenerating
 # script that mishandles a glob does, and what a partial merge does. The
 # mutation narrows the embed pattern rather than deleting a file, because an
 # embed pattern is the one edit that removes many definitions at once while
 # leaving every file in the tree for `git status` to show as clean.
 #
 # `applications/[a-y]*` drops the z* keys and the digit-prefixed ones. The
 # expect deliberately does NOT name the count: "606 definitions ship" moves
 # every time the catalog grows, and an expect that moves with the data is an
 # expect that will be edited to whatever the next run prints.
 ("definition files go missing from the shipped set",
  [repl(CT, "//go:embed applications", "//go:embed applications/[a-y]*")],
  "and no definition ships for it"),

 # --- appspec/01 section 4 and appspec/05 Enumeration (macklebox-resolvers-5iw.4)
 #
 # `list` and `show` are the first commands that reach the boundary with
 # something to SAY, so they are the first entries whose kill is a claim about
 # printed output rather than about a refusal. Each was applied in a copied
 # tree and put to `make conformance` separately before it was written down.
 #
 # ONE PROPERTY OF THIS TICKET HAS NO ENTRY, deliberately: the superuser guard
 # of appspec/07. The conformance suite runs as an ordinary user -- that is
 # what makes it runnable in CI at all -- so it cannot take the refusing arm,
 # and deleting the guard is killed by internal/app alone and reports
 # RIG-BLIND. appspec/07 marks that path UNVERIFIED for the same reason, and
 # internal/app drives both arms through the effectiveUID seam instead. This is
 # the same call the catalog banner above used to record; it is written down
 # here so the absence stays a decision.

 # appspec/04 clause 2: the file_system engine "returns the path without any
 # existence check", and appspec/01 section 4 level 1 is where that deferred
 # check finally fires. Deleting the stat is the shape a reimplementation
 # reaches for when the gate looks like it is re-checking a path the config
 # already resolved -- it is not; nothing before it looks at the filesystem at
 # all, which is precisely what clause 2 arranges.
 #
 # Three repls since the round-5 review split the check in two: the stat, both
 # arms, and the two imports the inspect arm is the only user of. Deleting only
 # the arms would leave `info` and `err` declared and unused, which is a
 # DOES-NOT-COMPILE rather than a program that answers wrongly.
 ("the storage-root existence check is deleted", [
   repl(SG, '\t"errors"\n\t"io/fs"\n\t"os"\n', '\t"os"\n'),
   repl(SG, '\tinfo, err := os.Stat(root)\n', ''),
   repl(SG,
   '\tif err != nil && !errors.Is(err, fs.ErrNotExist) {\n'
   '\t\treturn fault.Guardedf("Unable to inspect the storage folder: %s", err)\n'
   '\t}\n'
   '\tif err != nil || !info.IsDir() {\n'
   '\t\treturn fault.Guardedf("Unable to find the storage folder: %s", root)\n'
   '\t}\n'
   '\treturn nil',
   '\t_ = root\n'
   '\treturn nil')],
   'want exactly "Error: Unable to find the storage folder: '),

 # The appspec/03 trap, and the only entry in this file whose mutation ADDS
 # code that looks MORE correct than what is there. A reader who knows the
 # config has application lists and sees `list` print all 614 keys concludes
 # the narrowing was forgotten, and wiring Config.Scope in is a four-line
 # change that reads like a fix.
 #
 # It is the defect. appspec/03 says of `[applications_to_sync]` that "this
 # section does not affect `list` output", and appspec/05 defines what `list`
 # prints as "the set of all keys assembled by the discovery rules" -- which is
 # what makes `list` an audit surface at all: a user narrowing their sync scope
 # still needs to see the catalog the narrowing is drawn from. Scope selects
 # what a SYNC command acts on, and internal/app/scope.go is now its one caller
 # -- which is what makes the appspec/03 entry above writable, and makes this
 # one sharper rather than weaker: both calls exist in the tree, and the defect
 # is putting the second one here.
 #
 # Same shape as "the file_system engine gains an existence check" above: a
 # mutation that a green suite would welcome, kept honest by a case that says
 # the absence is the contract.
 # Re-anchored on macklebox-copy-sync-dpz.3, and SHORTER than it was, which is
 # the point worth recording. dispatch used to take four parameters and the
 # config was not among them, so wiring Scope into `list` meant threading a
 # *config.Config through app.go and dispatch.go as well -- four of the six
 # edits below were that plumbing. dispatch now takes one pipeline value that
 # already carries cfg, because the two sync arms need it, so the mutation is
 # the three edits that are actually about `list`. A mutation that has to add
 # a parameter to two functions is a mutation nobody would make by accident;
 # this one is a four-line change that reads like a fix, which is the whole
 # reason the entry exists.
 ("list is narrowed by the configured scope", [
   repl(D, "\t\treturn list(streams, apps)", "\t\treturn list(streams, p.cfg, apps)"),
   repl(EN, '\t"github.com/promptctl/macklebox/internal/appdb"\n',
            '\t"github.com/promptctl/macklebox/internal/appdb"\n'
            '\t"github.com/promptctl/macklebox/internal/config"\n'),
   repl(EN, "func list(streams *ui.IO, apps *appdb.Database) int {\n\tkeys := apps.Keys()",
            "func list(streams *ui.IO, cfg *config.Config, apps *appdb.Database) int {\n\tkeys := cfg.Scope(apps.Keys())"),
 ],
   "list under an allowlist and a denylist printed"),

 # appspec/05's observed effect for a dropped definition is ONE claim about TWO
 # lines of output: the key "appears in list" AND the trailer increments. A
 # count derived some other way than from what was printed satisfies neither
 # half honestly, and the reference build's own number -- appspec/05 writes
 # "Reference build: N = 614" -- is the constant a reader reaches for.
 #
 # The shipped catalog is exactly 614 applications, so this mutation is
 # invisible to every fixture that does not add or shadow a definition. It is
 # the entry that says the trailer counts the keys it just printed.
 #
 # The only entry in this section whose `make check` kill comes from the
 # CONFORMANCE suite rather than a unit package -- every unit fixture runs
 # against the shipped catalog alone, where 614 is the right answer -- so its
 # expect is a conformance diagnostic, per the rule the appspec/07 banner
 # states. It names the owning case rather than the helper's sentence, which
 # four cases share and which carries the catalog's count in its text.
 ("the list trailer counts the catalog instead of what it printed",
  [repl(EN, '"%d applications supported in Mackup v%s", len(keys), version.String()',
            '"%d applications supported in Mackup v%s", 614, version.String()')],
  "FAIL: TestADroppedDefinitionAppearsInListAndIncrementsTheCount"),

 # --- appspec/06 sync primitives (macklebox-copy-sync-dpz.1) ----------------
 #
 # The banner that stood here was a deferral, and this is it being paid rather
 # than restated. internal/syncfs shipped whole -- copy, delete, link, the
 # recursive clamp, the attribute cleanup, LinkState and the already-linked
 # predicate -- with NOTHING CALLING ANY OF IT, so every entry written then
 # would have been killed by that package's own unit tests and reported
 # RIG-BLIND by the `make conformance` step, which was the accurate verdict.
 # macklebox-copy-sync-dpz.3 wires backup and restore to Copy, Delete and
 # AlreadyLinked, and the seven entries below are exactly the ones the rig
 # can now watch. TWO of that banner's nine were written, injected and found
 # RIG-BLIND, and they are parked below with what makes them so.
 #
 # They were transcribed from that banner's prose and RE-ANCHORED against the
 # current source rather than pasted: the banner recorded each mutation as a
 # sentence, not as an anchor, and two of the case names it carried no longer
 # exist under those spellings. The conformance case each entry leans on is
 # named beside it, and is what the battery's [rig: ...] note has to report.
 #
 # Each `expect` names a UNIT diagnostic, because `make check` runs the unit
 # packages first and stops there -- the rule the appspec/07 banner above
 # states, applied per entry. The conformance half of the claim is carried by
 # the separate `make conformance` run, not by expect.
 #
 # internal/syncfs/attributes.go is still deliberately absent from FILES. All
 # four attribute mutations stay parked below, so no entry edits that file, and
 # a file in FILES that no mutation edits is backed up and restored for nothing.

 # The clamp is a post-condition of the path that was COPIED, never of the
 # ancestors created to reach it. Getting this backwards makes the first file
 # copied into a fresh Mackup folder narrow the folder itself to 0700 -- a
 # directory the program was never asked to manage, inside the user's Dropbox.
 ("the parents of a destination are clamped too", [repl(SY,
   "\tparentMode = 0o777", "\tparentMode = 0o700")],
   "FAIL: TestCopyCreatesMissingParentsWithoutClampingThem"),
   # rig: TestTheDirectoriesCreatedOnTheWayToADestinationAreNotClamped

 # The whole IsDir/else block is the anchor, down to the closing "})", because
 # os.Chmod(path, fileMode) occurs twice in this file -- once in Clamp's own
 # regular-file arm and once here. A repl naming just the line would resolve to
 # neither, since apply() requires exactly one occurrence.
 ("the clamp gives regular files the directory mode", [repl(SY,
   "\t\tif info.IsDir() {\n\t\t\treturn os.Chmod(path, dirMode)\n\t\t}\n"
   "\t\treturn os.Chmod(path, fileMode)\n\t})",
   "\t\tif info.IsDir() {\n\t\t\treturn os.Chmod(path, dirMode)\n\t\t}\n"
   "\t\treturn os.Chmod(path, dirMode)\n\t})")],
   "FAIL: TestACopiedDirectoryTreeIsClamped0700And0600Recursively"),
   # rig: TestACopiedTreeIsClampedTo0700DirectoriesAnd0600FilesRecursively

 ("a directory copy does not recurse", [repl(SY,
   "\t\t\terr = copyTree(from, to)", "\t\t\terr = nil")],
   "FAIL: TestACopiedDirectoryTreeIsClamped0700And0600Recursively"),
   # rig: TestACopiedTreeIsClampedTo0700DirectoriesAnd0600FilesRecursively

 # The asymmetry between the two walks in this one file: copyTree classifies
 # each entry with a FOLLOWING stat, so a directory symlink is descended into
 # and written to storage as real content, while clampTree reads the entry's
 # own type and stops. Adding the clamp's guard to the copy is the tidy-up a
 # reader who noticed the difference reaches for.
 ("a directory copy stops at a symlinked directory", [repl(SY,
   "\t\tcase statErr == nil && info.IsDir():",
   "\t\tcase statErr == nil && info.IsDir() && entry.Type()&fs.ModeSymlink == 0:")],
   "FAIL: TestCopyDescendsIntoASymlinkedDirectoryInsideTheSourceTree"),
   # rig: TestASymlinkedDirectoryInsideTheSourceTreeIsCopiedAsRealContent

 ("the copy classifies with Lstat", [repl(SY,
   "\tinfo, err := os.Stat(src)", "\tinfo, err := os.Lstat(src)")],
   "FAIL: TestCopyFollowsASymlinkedSourceAndWritesARealFile"),
   # rig: TestASymlinkedSourceIsCopiedAsTheRealFileItPointsAt

 # appspec/01 section 2's one predicate, which four operations are promised to
 # share. Both mutations below keep it compiling and answering true on the
 # arrangement every ordinary fixture has; what they lose is the two conditions
 # that only an unusual arrangement can see.
 ("the predicate accepts any live home symlink", [repl(SS,
   "\treturn os.SameFile(home, mackup)",
   "\t_, _ = home, mackup\n\treturn true")],
   "FAIL: TestTheAlreadyLinkedPredicateIsTrueOnlyForALiveLinkToTheMackupCopy"),
   # rig: TestAHomeSymlinkPointingSomewhereElseIsBackedUpAsItsContents

 ("the predicate compares link text instead of identity", [repl(SS,
   "\treturn os.SameFile(home, mackup)",
   "\t_, _ = home, mackup\n"
   "\ttext, readErr := os.Readlink(homePath)\n"
   "\tif readErr != nil {\n\t\treturn false\n\t}\n"
   "\treturn text == mackupPath")],
   "FAIL: TestTheLinkedAnswerSurvivesAStorageRootReachedThroughASymlink"),
   # rig: TestTheLinkSkipHoldsWhenTheStorageRootIsReachedThroughASymlink

 # STILL PARKED, and the reasons are structural rather than a missing fixture.
 # Fourteen of the old banner's twenty-two mutations cannot honestly be entries
 # today, and each would report RIG-BLIND if written -- which is a battery
 # failure dressed as a finding. Written down so the next reader does not
 # rediscover them one `make conformance` at a time.
 #
 #   Link's two entries -- the Clamp/Symlink order swapped, and a relative
 #   target -- and both StateOf entries. NOTHING CALLS Link OR StateOf. The
 #   three link arms of dispatch still report "not implemented"; they are
 #   macklebox-link-engine-83q.2's, and these four go with it.
 #
 #   "delete of an absent path is an error" (Delete's IsNotExist arm removed).
 #   The executor only reaches Delete after a successful Lstat of the
 #   destination, so the absent arm has no caller that can supply an absent
 #   path. It is reachable in principle through appspec/07's interruption
 #   residue and unreachable through the rig.
 #
 #   "the clamp fails on a broken symlink" and "the clamp descends through
 #   symlinked directories". The tree the clamp walks was created by the copy
 #   moments earlier, and the copy follows symlinks -- so that tree holds none
 #   for the clamp to meet. Conformance cases for both were written on this
 #   branch, watched to fail for that reason, and deleted. Do not re-add them.
 #
 #   "a directory copy clears the destination first" (copyTree gains an
 #   os.RemoveAll). syncfs.Copy is only ever called with dst absent -- either
 #   its Lstat failed or Delete has just run -- so the RemoveAll is a no-op on
 #   every path the rig can drive. This is the same fact copyTree's own merge
 #   comment records, from the other side.

 #   "copy does not clamp the destination" (Copy's `return Clamp(dst)` ->
 #   `return nil`) and "the clamp does not recurse" (Clamp's directory arm ->
 #   `return nil`). Both were entries on this ticket, both reported
 #   WRONG-DIAGNOSTIC, and probing them by hand found the rig does not kill
 #   either. THE REASON IS THE SENTENCE DIRECTLY ABOVE, from a third side, so
 #   read it before writing a fixture: dst is ALWAYS ABSENT when Copy runs.
 #   copyFile creates with O_CREATE|fileMode and copyTree with MkdirAll(dst,
 #   dirMode), so a freshly copied tree ALREADY LANDS at 0600/0700 and the
 #   clamp only re-applies what the create modes gave. There is nothing for a
 #   conformance case to see because there is no observable difference to see.
 #
 #   Do NOT try to unpark these with a "backup over an existing loose-moded
 #   destination" case. That fixture was designed on this ticket before the
 #   above was worked out, and it proves nothing: the executor deletes the
 #   destination before copying, so the loose mode is gone by the time Copy is
 #   reached. (The umask angle does work in principle -- under umask 0200 the
 #   create gives 0400 and only the clamp gets to 0600 -- but a test process's
 #   umask is process-global across the whole suite, which is a large hazard to
 #   accept for two entries.) What DOES unpark them is
 #   macklebox-link-engine-83q.x: Link clamps a target that may already exist,
 #   so the clamp stops being a re-application of the create mode.
 #
 #   Their UNIT killers are recorded here because the entries above named the
 #   wrong ones and the next reader should not pay a battery round to
 #   rediscover that. "copy does not clamp the destination" is killed by
 #   TestAnExistingDestinationFileIsClampedTooNotJustANewOne and
 #   TestCopyAndLinkInheritTheAttributeStripThroughTheClamp -- NOT by
 #   TestACopiedFileLandsMode0600, which copies to a fresh destination where
 #   0600 comes from the create. "the clamp does not recurse" is killed by
 #   TestLinkClampsItsTargetBeforeCreatingTheLink,
 #   TestClampSkipsABrokenSymlinkInsteadOfFailing and
 #   TestClampDoesNotDescendThroughASymlinkedDirectory -- NOT by
 #   TestACopiedDirectoryTreeIsClamped0700And0600Recursively, which passes for
 #   the same create-mode reason. Every one of those observed, not predicted.
 #
 #   DO NOT PARK "the clamp gives regular files the directory mode" BY
 #   ASSOCIATION. It sits two entries up and it IS killed, with rig
 #   confirmation, and the create-mode argument does not cover it: clampTree
 #   chmodding a file to 0700 is MORE permissive than the 0600 the create gave,
 #   so it is the one clamp mutation that changes an observable mode. The same
 #   goes for "the parents of a destination are clamped too", which is about
 #   ancestors the create does not touch.
 #
 #   All four attribute entries: the two deferral orders, the loop removal and
 #   the platform table swap. cleanAttributes shells out to /bin/chmod -N and
 #   its three siblings, none of which changes anything observable on an
 #   ordinary fixture, so internal/syncfs owns them through the runCleanup seam
 #   and its own unit tests. This is why attributes.go is not in FILES.
 #
 # Three mutations were injected and are NOT listed anywhere above, because
 # they survived and deserve to. Each is behaviour-preserving, so an entry for
 # it would be a standing false alarm rather than a hole.
 #
 #   * Dropping either existence check from AlreadyLinked. os.SameFile is false
 #     when handed the nil FileInfo a failed stat leaves, so both are implied by
 #     the comparison that follows them.
 #   * Delete's os.Lstat -> os.Stat. os.Remove unlinks a symlink and
 #     os.RemoveAll begins with an os.Remove that succeeds on one, so a symlink
 #     to a non-empty directory is unlinked either way and its target survives.
 #     Verified directly, not reasoned about.
 #   * Removing runCleanupCommand's os.Stat guard. exec's own ENOENT is
 #     discarded to the same effect. Both files carry a comment saying so.

 # --- appspec/06 the two executor defects the adversarial review of PR #10 found
 #
 # Both are in internal/app/sync.go, both were REPRODUCED at the boundary before
 # being fixed, and both are invisible to every unit package -- so unlike almost
 # every other entry here, the `expect` of each is the CONFORMANCE diagnostic.
 # That is not a slip: `make check` runs the unit packages first and stops there
 # ON A FAILURE, and these do not fail there, so the run carries on to
 # `make conformance` and that is the suite the gate actually stops at. Probed,
 # not assumed -- `go test ./internal/...` is green under both mutations.

 # The eager spelling, which is what the code did before the review. Deferring
 # the header looks like indirection for its own sake until you run the program
 # unscoped: appspec/06's step 1 skips an absent source SILENTLY, so on a real
 # home nearly every one of the ~614 catalog keys prints nothing, and a header
 # per key gave 623 stdout lines of which 614 were headers. This mutation is one
 # line and restores exactly that.
 ("the verbose header is printed eagerly", [repl(EX,
   "func (e *executor) header(key string) {\n\te.pendingHeader = key\n}",
   "func (e *executor) header(key string) {\n\te.pendingHeader = key\n\te.flushHeader()\n}")],
   "FAIL: TestTheVerboseHeaderIsPrintedOnlyForAnApplicationThatPrintsSomething"),
   # rig: TestTheVerboseHeaderIsPrintedOnlyForAnApplicationThatPrintsSomething

 # appspec/06's partial-failure summary, dropped on the one path that does not
 # return through report(). A run that fails a copy and THEN dies at a prompt it
 # cannot answer still owes the summary -- it is the only line naming which file
 # to go back to. Exit is non-zero either way, so nothing about appspec/00
 # promise 9 moves and no case asserting the exit code can see this.
 ("the incomplete summary is dropped at an unanswerable prompt", [repl(SN,
   "\t\trun.summarize()\n\t\treturn reportFatal(p.streams, err)",
   "\t\treturn reportFatal(p.streams, err)")],
   "FAIL: TestTheIncompleteSummaryIsPrintedEvenWhenTheRunEndsAtAnUnanswerablePrompt"),
   # rig: TestTheIncompleteSummaryIsPrintedEvenWhenTheRunEndsAtAnUnanswerablePrompt

 # --- appspec/02 the two argv defects the round-2 review of PR #10 found
 #
 # Both are what test/conformance/argv_test.go's two acceptance cases exist to
 # catch, and until that review neither case could: they asserted exit 0 and a
 # silent stderr over worlds holding no file the named application owns, which
 # is what a run that dropped every option, or reached the wrong arm entirely,
 # also produces. Seeding one file per world and asserting the line the command
 # prints about it is the fix, and these two entries are what says so.

 # The one-word defect appspec/01 section 1 is written to prevent: "any
 # divergence between backup and restore other than {direction, user-facing
 # wording, the one link-skip} is a defect", and the direction record exists so
 # the divergence is ONE argument. Which makes passing the wrong one the whole
 # bug, in one token, with no other symptom -- both arms still run, still gate,
 # still copy, and still exit 0.
 #
 # `expect` is the CONFORMANCE diagnostic, for the reason the section above
 # gives and probed the same way: `go test ./internal/...` is green under this
 # mutation, so `make check` does not stop at the unit stage.
 ("backup runs the restore direction", [repl(D,
   "\t\treturn runSync(p, backupDirection)",
   "\t\treturn runSync(p, restoreDirection)")],
   "FAIL: TestEveryInvocationFormIsAcceptedAndReachesItsCommand"),
   # rig: TestEveryInvocationFormIsAcceptedAndReachesItsCommand

 # The regression TestOptionsAreAcceptedOnEitherSideOfTheSubcommand is NAMED
 # for. appspec/02 requires only that options precede the subcommand; accepting
 # them after it is this implementation's own promise, so it is exactly the
 # half a rewrite of the scan loop would drop without any spec line objecting.
 # The parser has no notion of "after" to break, which is why the mutation adds
 # one: once a positional has been seen, every remaining option is skipped.
 #
 # Here `expect` IS a unit diagnostic -- internal/cli/parse_test.go sees this
 # one and `make check` stops there -- and the [rig: ...] note carries the
 # conformance half. Both observed by injection, neither predicted.
 ("options after the subcommand are dropped", [repl(P,
   "\t\targ := argv[i]\n\t\tswitch {",
   "\t\targ := argv[i]\n\t\tif len(positional) > 0 && strings.HasPrefix(arg, \"-\") {\n\t\t\tcontinue\n\t\t}\n\t\tswitch {")],
   "FAIL: TestParseOptionsBeforeAndAfterSubcommand"),
   # rig: TestOptionsAreAcceptedOnEitherSideOfTheSubcommand

 # --- appspec/06 the guard the round-4 review of PR #10 found keyed on the
 # wrong predicate
 #
 # appspec/06 splits the per-file procedure on whether a copy EXISTS at the
 # destination. The code split on whether os.Lstat returned an error, which is
 # not the same question: a stat that failed for any reason other than ENOENT
 # answered neither branch, and the branch it fell into was step 4 -- no
 # comparison, no diff, no replace prompt, straight to syncfs.Copy, which does
 # not require an absent destination.
 #
 # ONE repl, and it was two until round 7. Deleting the guard used to strand
 # "errors" and "io/fs", so this entry dropped them with it; sourcePresent is a
 # second user of both now, and dropping them is what breaks the build -- the
 # entry reported DOES-NOT-COMPILE, which verifies nothing about the guard it
 # exists for. An import repl is only ever right while the mutated code is the
 # file's ONLY user of what it removes, and that is a fact about the file on the
 # day the entry runs, not the day it was written. --anchors cannot see this:
 # both repls still resolve, and the breakage is at compile time. Re-injecting
 # every entry that names a file you edited is what catches it.
 #
 # `expect` is the CONFORMANCE diagnostic, for the reason the sync.go section
 # above gives and probed the same way: `go test ./internal/...` is green under
 # this mutation, so `make check` carries on to the conformance stage.
 ("an uninspectable destination is read as an absent one", [
   repl(SN, "\tif err != nil && !errors.Is(err, fs.ErrNotExist) {\n\t\tr.fail(src, dst, err)\n\t\treturn nil\n\t}\n\n",
            "")],
   "FAIL: TestADestinationThatCannotBeInspectedIsAFailureAndNotAnAbsence"),
   # rig: TestADestinationThatCannotBeInspectedIsAFailureAndNotAnAbsence

 # --- appspec/01 section 4 and appspec/07: what the round-5 review found the
 # round-4 fix had moved
 #
 # Fixing the per-file guard made fail reachable with no progress line ahead of
 # it, and that in turn made the SAME conflation one file over worth naming: a
 # stat error is not an answer, wherever it is asked.

 # The other half of what the header means. Flushing from fail looks like the
 # obvious repair for "an application that failed got no header" and is the
 # wrong one: the header is a STDOUT grouping and the failure line is stderr,
 # so it prints a header with nothing under it -- which every OTHER application
 # in TestTheVerboseHeaderIsPrintedOnlyForAnApplicationThatPrintsSomething is
 # already forbidden from doing. Only the verbose case can see this; the
 # round-4 case runs unscoped-quiet and flushHeader returns early without
 # --verbose, which is why the two are separate cases.
 ("fail flushes the pending verbose header", [repl(SN,
   "func (r *syncRun) fail(src, dst string, err error) {\n",
   "func (r *syncRun) fail(src, dst string, err error) {\n\tr.flushHeader()\n")],
   "FAIL: TestAnApplicationWhoseOnlyOutputIsAFailureGetsNoVerboseHeader"),
   # rig: TestAnApplicationWhoseOnlyOutputIsAFailureGetsNoVerboseHeader

 # The folder gate before the round-5 review: os.path.isdir's answer, which is
 # what the reference gives and what appspec/07's table has no row for. Restore
 # reported a folder it could not stat as missing, hint and all; backup offered
 # to create one that was already there. Two repls for the reason the entry
 # above it has them -- the guard's removal leaves "errors" and "io/fs" unused.
 #
 # `expect` is the CONFORMANCE diagnostic, probed like the others: no unit
 # package sees this, so `make check` reaches the conformance stage. The case is
 # behind `conformance && unix` and skips as the superuser, so a battery run as
 # root would report this NOT KILLED -- correctly, since the fixture denies
 # nothing then.
 ("the folder gate reads every stat error as absence", [
   repl(FD, "\t\"errors\"\n\t\"fmt\"\n\t\"io/fs\"\n\t\"os\"\n",
            "\t\"fmt\"\n\t\"os\"\n"),
   repl(FD, "\tinfo, err := os.Stat(folder)\n\tif err != nil {\n\t\tif errors.Is(err, fs.ErrNotExist) {\n\t\t\treturn false, nil\n\t\t}\n\t\treturn false, fault.Guardedf(\"Unable to inspect the Mackup folder: %s\", err)\n\t}\n\treturn info.IsDir(), nil",
            "\tinfo, err := os.Stat(folder)\n\treturn err == nil && info.IsDir(), nil")],
   "FAIL: TestAMackupFolderThatCannotBeInspectedIsNotReportedAsMissing"),
   # rig: TestAMackupFolderThatCannotBeInspectedIsNotReportedAsMissing

 # Level 1 of the same lattice, which the round-5 review did not name and which
 # was found by asking the finding's question of the other two gates. Same
 # conflation, same one-line shape, and it had the same consequence: a storage
 # root inside an unsearchable directory was reported as one that is not there.
 ("the storage-root gate reads every stat error as absence", [
   repl(SG, "\t\"errors\"\n\t\"io/fs\"\n\t\"os\"\n", "\t\"os\"\n"),
   repl(SG, "\tif err != nil && !errors.Is(err, fs.ErrNotExist) {\n\t\treturn fault.Guardedf(\"Unable to inspect the storage folder: %s\", err)\n\t}\n",
            "")],
   "FAIL: TestAStorageRootThatCannotBeInspectedIsNotReportedAsMissing"),
   # rig: TestAStorageRootThatCannotBeInspectedIsNotReportedAsMissing

 # --- appspec/06 step 1 and the copy arm: what the round-7 review of PR #10
 # found the round-4 fix had left behind
 #
 # The same stat conflation ONE MORE TIME, on the last side that still had it,
 # and the review found it by asking the round-4 question of the source instead
 # of the destination. This is the worst place it lived: a destination read as
 # absent at least printed a line, and a SOURCE read as absent is skipped
 # silently and the run exits 0 -- the one outcome appspec/01 section 5 states
 # unconditionally ("A partial backup/restore can never exit 0").
 #
 # `expect` is the UNIT diagnostic: internal/app/sync_test.go asks sourcePresent
 # the third-answer question directly, so make check stops before the
 # conformance stage. The [rig: ...] note carries the conformance half, which is
 # behind `unix` because only a permission arranges EACCES on a source. Both
 # halves observed by injection.
 ("an uninspectable source is read as an absent one", [
   repl(SN, "	info, err := os.Stat(path)\n	if errors.Is(err, fs.ErrNotExist) {\n		return false, nil\n	}\n	if err != nil {\n		return false, err\n	}\n",
            "	info, err := os.Stat(path)\n	if err != nil {\n		return false, nil\n	}\n")],
   "FAIL: TestStepOnesTestAdmitsFilesAndDirectoriesAndFollowsSymlinks"),
   # rig: TestASourceThatCannotBeInspectedIsAFailureAndNotAnAbsence

 # The partial-failure contract's ORIGINAL arm, which the round-4 guard made
 # unreachable without anyone noticing. Every case that exercised the contract
 # arranged a regular file where the destination's parent belongs; that stat
 # returns ENOTDIR, so after round 4 the guard reported the failure one branch
 # earlier and syncfs.Copy was never called. This mutation SURVIVED both suites
 # when the review named it -- the arm that records a real copy failure had
 # neither a case nor an entry over it.
 #
 # `expect` is the CONFORMANCE diagnostic, probed rather than predicted: the
 # unit packages are green under this mutation, so make check reaches the
 # conformance stage. The case it names arranges a source directory holding a
 # DANGLING SYMLINK, so copyTree raises its "not a regular file or directory"
 # refusal with a destination that stats cleanly as absent -- the guard does not
 # fire and the copy itself is what fails.
 ("a copy failure inside the copy is swallowed", [repl(SN,
   "	if err := syncfs.Copy(src, dst); err != nil {\n		r.fail(src, dst, err)\n	}",
   "	if err := syncfs.Copy(src, dst); err != nil {\n		_ = err\n	}")],
   "FAIL: TestACopyThatFailsInsideTheCopyItselfIsReportedAndTheRunCarriesOn"),
   # rig: TestACopyThatFailsInsideTheCopyItselfIsReportedAndTheRunCarriesOn

 # --- appspec/06 drift detection and the diff detail (macklebox-copy-sync-dpz.2)
 #
 # The other deferral, paid the same way. internal/drift and internal/plist
 # shipped with every comparison class, the unified diff, the directory summary
 # and the appspec/07 level each detail line is printed at, and nothing called
 # them either. dispatch's backup and restore now do, through the single
 # drift.Compare in internal/app/sync.go, so the comparison classes and the
 # directory summary are observable at the boundary and the twelve entries
 # below are written.
 #
 # The diff SHAPE and the property-list reader waited one more commit and are
 # the section below this one. That deferral said the conformance suite
 # "asserts that a diff appears and what its two sides are" and pinned nothing
 # about the context width, the hunk merge, the range spelling, the per-line
 # levels or a single plist type; compare_test.go, compare_unix_test.go and
 # plist_test.go pin all of it, so those entries are written rather than parked.
 #
 # As above, each `expect` is a UNIT diagnostic and the [rig: ...] note the
 # battery prints carries the conformance half.

 # appspec/06 "Drift detection", the comparison classes.
 #
 # The first two are the same rule from both sides: "if either path is a
 # symlink: treated as differing, with no diff detail". It is asked of the
 # Lstat results, which is what makes it a question about the PATH -- and it is
 # the easy mistake to make here precisely because internal/syncfs.Copy, one
 # package over, deliberately does the opposite with a following stat.
 ("drift classifies with a following stat", [repl(DR,
   "\tsourceInfo, sourceErr := os.Lstat(source)\n\ttargetInfo, targetErr := os.Lstat(target)",
   "\tsourceInfo, sourceErr := os.Stat(source)\n\ttargetInfo, targetErr := os.Stat(target)")],
   "FAIL: TestEitherPathBeingASymlinkIsDifferingWithNoDetail"),
   # rig: TestASymlinkAtTheDestinationGetsThePlainPromptWithNoDiff

 ("the symlink arm reports agreement", [repl(DR,
   "\tif isSymlink(sourceInfo) || isSymlink(targetInfo) {\n\t\treturn differs()\n\t}",
   "\tif isSymlink(sourceInfo) || isSymlink(targetInfo) {\n\t\treturn identical()\n\t}")],
   "FAIL: TestEitherPathBeingASymlinkIsDifferingWithNoDetail"),
   # rig: TestASymlinkAtTheDestinationGetsThePlainPromptWithNoDiff

 # The detail is written source-first, so backup and restore print opposite
 # messages about the same pair of paths. One message for both cases tells half
 # the program's users the wrong thing and passes any single-direction case.
 ("the type mismatch is one message either way", [repl(DR,
   'note("type mismatch: folder vs file")', 'note("type mismatch: file vs folder")')],
   "FAIL: TestATypeMismatchIsOneLineSayingWhichWayRound"),
   # rig: TestATypeMismatchIsOneLineSayingWhichWayRound

 # appspec/06 puts the plist arm AHEAD of the text arm, and both halves of that
 # ordering are defects with the same signature: a preference file's markup is
 # diffed instead of its settings, so every plist macOS rewrites is reported as
 # drift on every run. The first mutation deletes the arm; the second keeps it
 # and moves it behind the text arm, which is what a reader tidying the
 # cheapest test to the front would do.
 ("the plist arm is gone", [repl(DR,
   "\tif detail, isPlistPair := comparePlists(sourceBytes, targetBytes, source, target); isPlistPair {\n"
   "\t\treturn detail\n\t}\n", "")],
   "FAIL: TestTwoSpellingsOfOnePropertyListAreComparedByContentNotBytes"),
   # rig: TestTwoSpellingsOfOnePropertyListAreComparedByContentNotBytes

 ("the text arm runs before the plist arm", [repl(DR,
   "\tif detail, isPlistPair := comparePlists(sourceBytes, targetBytes, source, target); isPlistPair {\n"
   "\t\treturn detail\n\t}\n"
   "\tif utf8.Valid(sourceBytes) && utf8.Valid(targetBytes) {\n"
   "\t\treturn compareText(sourceBytes, targetBytes, source, target)\n\t}\n",
   "\tif utf8.Valid(sourceBytes) && utf8.Valid(targetBytes) {\n"
   "\t\treturn compareText(sourceBytes, targetBytes, source, target)\n\t}\n"
   "\tif detail, isPlistPair := comparePlists(sourceBytes, targetBytes, source, target); isPlistPair {\n"
   "\t\treturn detail\n\t}\n")],
   "FAIL: TestAPropertyListIsComparedAsAStructureAndNotAsMarkup"),
   # rig: TestAPropertyListDiffShowsTheStructureAndNotTheMarkup

 # The byte comparison ahead of the three arms is load-bearing and not an
 # optimisation, which the injection pass on dpz.2 established against a
 # comment then claiming otherwise: the text arm has no identical answer of its
 # own, so without this every unchanged file in every run is reported as
 # differing and the idempotency promise of appspec/00 is gone. It is the one
 # entry here that found a defect in the PROGRAM rather than a hole in the rig.
 ("the byte-equality fast path is removed", [repl(DR,
   "\tif bytes.Equal(sourceBytes, targetBytes) {\n\t\treturn identical()\n\t}\n\n", "")],
   "FAIL: TestTwoIdenticalFilesAreTheIdempotencyFixedPoint"),
   # rig: TestASecondIdenticalRunDoesNothingAndPromptsForNothing

 # Identity for the plist arm is decided from the same rendering the diff is
 # taken over, so the arm cannot report "differs" and then print an empty diff.
 # A length comparison keeps every existing identical case green -- two
 # renderings of different length still differ -- and calls two documents with
 # the same shape and different values the same.
 ("plist identity is decided before rendering", [repl(DR,
   "func equalLines(a, b []string) bool {\n\tif len(a) != len(b) {\n\t\treturn false\n\t}\n"
   "\tfor i := range a {\n\t\tif a[i] != b[i] {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n}",
   "func equalLines(a, b []string) bool {\n\treturn len(a) == len(b)\n}")],
   "FAIL: TestTwoPropertyListsThatDifferProduceADiffOfTheirStructures"),
   # rig: TestAPropertyListDiffShowsTheStructureAndNotTheMarkup

 # appspec/07's "Do not generalize warnings -> stderr" names this message
 # specifically: the drift header and its diff body are STDOUT. A rig asserting
 # only the text would pass a program that misrouted every line of it, which is
 # why the conformance case asserts a silent stderr and this entry exists.
 ("the detail is printed on the error stream", [repl(DR,
   "\t\tstreams.Say(line.Level, line.Text)", "\t\tstreams.Say(ui.CopyFailure, line.Text)")],
   "FAIL: TestTheDriftDetailGoesToStdout"),
   # rig: TestTheDriftHeaderAndDiffAreOnStdoutAheadOfThePrompt

 # appspec/06 "Drift detection", the recursive directory comparison.
 #
 # "only in source" and "only in target" are the two lists a reimplementation
 # is most likely to exchange, and exchanging them is silent: the same names
 # appear, under labels that tell the user the copy is about to do the opposite
 # of what it will do.
 ("the two only-side lists are swapped", [repl(TD,
   "\t\tcase !inTarget:\n\t\t\td.onlySource = append(d.onlySource, relative)\n"
   "\t\tcase !inSource:\n\t\t\td.onlyTarget = append(d.onlyTarget, relative)",
   "\t\tcase !inTarget:\n\t\t\td.onlyTarget = append(d.onlyTarget, relative)\n"
   "\t\tcase !inSource:\n\t\t\td.onlySource = append(d.onlySource, relative)")],
   "FAIL: TestADirectoryComparisonListsTheThreeGroupsAppspec06AsksFor"),
   # rig: TestADirectoryComparisonIsRecursiveAndNamesTheThreeGroups

 ("a one-sided directory is descended into", [repl(TD,
   "\t\tcase !inTarget:\n\t\t\td.onlySource = append(d.onlySource, relative)\n",
   "\t\tcase !inTarget:\n\t\t\td.onlySource = append(d.onlySource, relative)\n"
   "\t\t\tif children, childErr := entriesOf(filepath.Join(source, name)); childErr == nil {\n"
   "\t\t\t\tfor child := range children {\n"
   "\t\t\t\t\td.onlySource = append(d.onlySource, path.Join(relative, child))\n"
   "\t\t\t\t}\n\t\t\t}\n")],
   "FAIL: TestADirectoryOnOneSideIsNamedOnceAndNotDescendedInto"),
   # rig: TestADirectoryOnOneSideIsNamedOnceAndNotDescendedInto

 # The directory arm's own idempotency fixed point. Without the empty check a
 # tree whose every file matches still returns a differing result with an empty
 # detail, so the user is prompted about an unchanged directory on every run --
 # the same failure shape as the byte fast path above, one arm over.
 ("an identical tree still reports detail", [repl(TD,
   "\tif found.empty() {\n\t\treturn identical()\n\t}\n", "")],
   "FAIL: TestTwoIdenticalTreesAreIdentical"),
   # rig: TestTwoIdenticalDirectoriesAreTheIdempotencyFixedPointToo

 # Promise 6 of appspec/00 is "diff-before-replace": the user is about to be
 # asked whether to replace the DESTINATION with the source, so the destination
 # is the "before" whose lines are removed. Printed the other way round, every
 # "+" names a line that is about to be deleted -- which is a diff that is
 # wrong in the one direction it is shown for.
 ("the diff runs source-to-destination", [repl(DR,
   "\treturn Result{Detail: unified(targetLines, sourceLines, target, source)}, true",
   "\treturn Result{Detail: unified(sourceLines, targetLines, source, target)}, true")],
   "FAIL: TestTwoPropertyListsThatDifferProduceADiffOfTheirStructures"),
   # rig: TestAPropertyListDiffShowsTheStructureAndNotTheMarkup

 # --- appspec/06 the diff SHAPE and the property-list reader (dpz.3) --------
 #
 # The last of dpz.2's three deferrals, paid the way the other two were. Twenty
 # of the entries below were parked with a note saying "waiting on a conformance
 # case for X"; test/conformance/compare_test.go, compare_unix_test.go and
 # plist_test.go are X, so those notes become entries and the notes are deleted
 # rather than left standing beside the thing they asked for. The twenty-first
 # ("a tree entry is classified unfollowed") was in no banner at all and was
 # found by injecting against the new cases, which is the argument for running
 # the probe over a whole file rather than only over the list you inherited.
 #
 # Each was injected TWICE before it was written here, and both halves are
 # observed rather than predicted. The `expect` is the UNIT diagnostic, taken
 # by applying the mutation to a scratch tree and running `go test ./internal/...`
 # -- ten seconds an entry, against the ninety a battery round costs to learn
 # the same thing as a WRONG-DIAGNOSTIC. The `# rig:` name is what the
 # conformance suite reported for that same mutation, taken the same way, which
 # is the half that decides whether an entry is honest or is a RIG-BLIND report
 # wearing a finding's clothes. Twenty-one written, twenty-one killed by both.

 # appspec/06's diff detail is a unified diff, and these six are the shape of
 # one: how much context a hunk carries, when two changes share a hunk, the two
 # range spellings diff(1) uses, the appspec/07 level each kind of line is
 # printed at, and the marker for a file that does not end in a newline. The
 # suite could already see that a diff appeared and which side each line was
 # on, which is what let all six survive it until compare_test.go.
 ("the diff has one line of context", [repl(DF,
   "const context = 3", "const context = 1")],
   "FAIL: TestTheDiffIsTheOneDiffWouldHavePrinted"),
   # rig: TestTheDiffCarriesThreeLinesOfContextOnEachSide

 # i..i and not i-context..i+context: every change becomes its own hunk, which
 # still renders and still shows the right lines. What is lost is the merge,
 # and a diff that prints two @@ headers where diff(1) prints one is not the
 # output appspec/06 promises even though every line in it is true.
 ("hunks never merge", [repl(DF,
   "\t\tfor j := i - context; j <= i+context; j++ {",
   "\t\tfor j := i; j <= i; j++ {")],
   "FAIL: TestChangesCloseTogetherShareAHunkAndFarApartDoNot"),
   # rig: TestChangesCloseTogetherShareAHunkAndFarApartDoNot

 # The two range spellings are diff(1) conventions, and both are the kind of
 # detail a reimplementation gets almost right: a one-line range is a bare
 # number rather than "n,1", and an empty range is numbered by the line BEFORE
 # it rather than incremented.
 ("a one-line range prints its count", [repl(DF,
   "\tif count == 1 {\n\t\treturn fmt.Sprintf(\"%d\", start+1)\n\t}\n", "")],
   "FAIL: TestAOneLineRangeOmitsItsCount"),
   # rig: TestAOneLineRangeOmitsItsCountAndAnEmptyOneIsNumberedFromZero

 ("an empty range is numbered from one", [repl(DF,
   "\t\treturn fmt.Sprintf(\"%d,0\", start)",
   "\t\treturn fmt.Sprintf(\"%d,0\", start+1)")],
   "FAIL: TestAnEmptyRangeIsNumberedTheWayDiffNumbersIt"),
   # rig: TestAOneLineRangeOmitsItsCountAndAnEmptyOneIsNumberedFromZero

 # appspec/07 gives a context line ordinary progress and an added line the
 # addition colour. Colouring context as an addition makes every unchanged line
 # of every diff read as new -- which is a diff whose every line is present and
 # whose meaning is inverted, and it is invisible to any case asserting text.
 ("a context line is an addition", [repl(DF,
   "lines = append(lines, Line{Level: ui.Progress, Text: \" \" + step.text})",
   "lines = append(lines, Line{Level: ui.DiffAdded, Text: \" \" + step.text})")],
   "FAIL: TestEachKindOfDiffLineCarriesTheLevelAppspec07GivesIt"),
   # rig: TestEachKindOfDiffLineCarriesTheLevelAppspec07GivesIt

 # The WHOLE body is replaced, not short-circuited with an early return. An
 # added `return before, after` above the live arms leaves those arms
 # unreachable, `go vet` says so, and the battery reports DOES-NOT-COMPILE --
 # a mutation that never reaches the gate and an entry that proves nothing.
 ("the no-newline marker is not emitted", [repl(DF,
   "\tif beforeTerminated == afterTerminated {\n\t\treturn before, after\n\t}\n"
   "\tif !beforeTerminated {\n\t\treturn append(append([]string(nil), before...), noNewline), after\n\t}\n"
   "\treturn before, append(append([]string(nil), after...), noNewline)\n}",
   "\t_, _ = beforeTerminated, afterTerminated\n\treturn before, after\n}")],
   "FAIL: TestTwoFilesDifferingOnlyInAFinalNewlineDifferAndSayHow"),
   # rig: TestTwoFilesDifferingOnlyInAFinalNewlineSayHow

 # appspec/06's directory comparison, the three claims the existing cases could
 # not see. The first is the classic shallow-comparison shortcut: two files of
 # the same length are declared the same without reading either, which is
 # exactly the check rsync's --size-only makes and exactly what a drift report
 # must not do -- an edit that preserves a file's length is the common case for
 # a preference file, not a contrived one.
 ("a tree comparison stops at the sizes", [repl(DR,
   "\tfirstBuffer := make([]byte, 32*1024)",
   "\tfirstInfo, firstStatErr := first.Stat()\n"
   "\tsecondInfo, secondStatErr := second.Stat()\n"
   "\tif firstStatErr == nil && secondStatErr == nil && firstInfo.Size() == secondInfo.Size() {\n"
   "\t\treturn true\n\t}\n"
   "\tfirstBuffer := make([]byte, 32*1024)")],
   "FAIL: TestADirectoryComparisonIsRecursiveAndNotAShallowStat"),
   # rig: TestADirectoryComparisonReadsContentsAndNotJustSizes

 # sort stays imported after this: union() below is its other user, so the
 # deletion is one line and needs no import edit. Two of the entries further
 # down DO need one, and the difference between them is worth the sentence --
 # a deletion that leaves an import unused is a DOES-NOT-COMPILE, not a
 # mutation.
 ("the three lists are not sorted", [repl(TD,
   "\t\tsort.Strings(group.names)\n", "")],
   "FAIL: TestADirectoryComparisonListsTheThreeGroupsAppspec06AsksFor"),
   # rig: TestTheDirectoryGroupsAreSortedWithinEachGroup

 # The syncfs asymmetry from the other side, and the entry the old parked
 # banner did not carry -- it was found by injection on this ticket rather than
 # transcribed. internal/syncfs.copyTree classifies with a FOLLOWING stat, so a
 # directory symlink inside a source tree is copied as real content; this walk
 # has to follow too, or the comparison declares drift against a copy that is
 # doing exactly what it was told. Both lines change: one os.Stat left behind
 # is a comparison that disagrees with itself about which side to follow.
 ("a tree entry is classified unfollowed", [repl(TD,
   "\tsourceInfo, sourceErr := os.Stat(source)\n\ttargetInfo, targetErr := os.Stat(target)",
   "\tsourceInfo, sourceErr := os.Lstat(source)\n\ttargetInfo, targetErr := os.Lstat(target)")],
   "FAIL: TestASymlinkInsideATreeIsFollowedSoTheComparisonIsTheCopysFixedPoint"),
   # rig: TestASymlinkedDirectoryInsideATreeMakesTheSecondBackupSilent

 # The two unreadable-path entries, which needed a mode-0000 fixture and so
 # waited on compare_unix_test.go rather than on the portable suite. Both are
 # the same defect in appspec/06's terms: "identical" is the claim that needs
 # evidence, and a file the process could not read supplies none. Reporting
 # agreement about it is how a backup silently skips the one file it could not
 # check.
 ("an unreadable file is agreement", [repl(DR,
   "\tsourceBytes, err := os.ReadFile(source)\n\tif err != nil {\n"
   "\t\t// \"If either file is unreadable, treated as differing with no detail\n"
   "\t\t// (plain prompt).\"\n\t\treturn differs()\n\t}\n"
   "\ttargetBytes, err := os.ReadFile(target)\n\tif err != nil {\n\t\treturn differs()\n\t}",
   "\tsourceBytes, err := os.ReadFile(source)\n\tif err != nil {\n"
   "\t\t// \"If either file is unreadable, treated as differing with no detail\n"
   "\t\t// (plain prompt).\"\n\t\treturn identical()\n\t}\n"
   "\ttargetBytes, err := os.ReadFile(target)\n\tif err != nil {\n\t\treturn identical()\n\t}")],
   "FAIL: TestAnUnreadableFileIsDifferingWithNoDetail"),
   # rig: TestAnUnreadableDestinationIsDifferingWithNoDiff

 # BOTH arms, in one edit. Changing only the source arm leaves the target arm
 # answering correctly, and every fixture that puts the unreadable path on the
 # destination side -- which is where compare_unix_test.go puts it, so the
 # world snapshot can still walk home -- would report a kill for a defect only
 # half present.
 ("an unreadable subdirectory ends the walk", [repl(TD,
   "\t\tif !d.walk(source, target, relative) {\n\t\t\td.changed = append(d.changed, relative)\n\t\t}",
   "\t\td.walk(source, target, relative)")],
   "FAIL: TestAnUnreadableDirectoryInsideATreeIsAChangedEntryRatherThanTheEndOfTheWalk"),
   # rig: TestAnUnreadableDirectoryInsideATreeIsAChangedEntryAndNotTheEndOfTheWalk

 # internal/plist, reached from the rig only through the plist arm of Compare.
 #
 # The first four are the leverage the dpz.2 banner promised and it delivered:
 # internal/plist/testdata holds ONE document in two spellings carrying a date,
 # a negative eight-byte integer, an emoji and fifteen keys, so a single
 # conformance case borrowing that pair kills all four. Each is a defect that
 # keeps the reader compiling and answering for every ordinary value, and loses
 # exactly one type -- which is why one fixture that holds every type at once is
 # worth more here than four cases each holding one.
 ("the date epoch is Unix", [repl(PB,
   "time.Date(2001, time.January, 1, 0, 0, 0, 0, time.UTC)",
   "time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)")],
   "FAIL: TestEveryPropertyListTypeIsReadBackFromBothSpellings"),
   # rig: TestTheTwoSpellingsOfOneDocumentAgreeOnEveryTypeItHolds

 # The sign bit cleared rather than the conversion removed. beUint returns a
 # uint64 and the mask is applied there, before the one conversion: written as
 # an int64 mask it would need the constant -1<<63 to name the same bit, which
 # is the spelling a reader mistakes for a typo.
 ("an eight-byte integer is read unsigned", [repl(PB,
   "\treturn int64(beUint(r.data[offset+1 : offset+1+width])), nil",
   "\tvalue := beUint(r.data[offset+1 : offset+1+width])\n"
   "\tif width == 8 {\n\t\tvalue &^= 1 << 63\n\t}\n\treturn int64(value), nil")],
   "FAIL: TestEveryPropertyListTypeIsReadBackFromBothSpellings"),
   # rig: TestTheTwoSpellingsOfOneDocumentAgreeOnEveryTypeItHolds

 # Widening each code unit to a rune is the tidy-up that looks like a
 # simplification: it is correct for every character in the basic multilingual
 # plane, so every ASCII fixture agrees, and it turns a surrogate pair into two
 # replacement characters. The import goes with it -- utf16.Decode is the only
 # use of that package in the file, and leaving it is a DOES-NOT-COMPILE.
 ("UTF-16 units are widened, not decoded", [
   repl(PB, "\treturn string(utf16.Decode(units)), nil",
        "\twidened := make([]rune, len(units))\n"
        "\t\tfor i, unit := range units {\n\t\t\twidened[i] = rune(unit)\n\t\t}\n"
        "\t\treturn string(widened), nil"),
   repl(PB, "\t\"unicode/utf16\"\n", "")],
   "FAIL: TestEveryPropertyListTypeIsReadBackFromBothSpellings"),
   # rig: TestTheTwoSpellingsOfOneDocumentAgreeOnEveryTypeItHolds

 # `low > 0x0F` and NOT `if false`. The low nibble cannot exceed 0x0F, so the
 # arm is dead either way -- but a literal false is a constant condition that
 # go vet's unreachable analysis can reject, and the point of a mutation is to
 # reach the gate.
 ("the count escape is ignored", [repl(PB,
   "\tif low == 0x0F {", "\tif low > 0x0F {")],
   "FAIL: TestEveryPropertyListTypeIsReadBackFromBothSpellings"),
   # rig: TestTheTwoSpellingsOfOneDocumentAgreeOnEveryTypeItHolds

 # A UID or a set silently becomes a nil value instead of a refusal, so two
 # files whose UIDs differ render identically and compare equal. At the
 # boundary the signature is inverted and that is what makes it visible: a
 # REFUSED file falls out of the plist arm into the byte comparison and prints
 # "binary contents differ", so accepting one makes that line disappear.
 ("an unmodelled marker is accepted", [repl(PB,
   "\treturn nil, notAPlist(\"object #%d has marker 0x%02x, which this reader does not model\", ref, marker)",
   "\treturn nil, nil")],
   "FAIL: TestABinaryPropertyListHoldingAUIDIsRefusedRatherThanModelled"),
   # rig: TestABinaryPropertyListHoldingAUIDIsNotComparedAsAPropertyList

 # Rewritten, not deleted. Deleting the check leaves `start` declared and not
 # used, which is a compile error rather than a mutation; `== ""` keeps the
 # variable read and accepts every document element there is.
 ("any XML document element is accepted", [repl(PX,
   "\tif start.Name.Local != \"plist\" {", "\tif start.Name.Local == \"\" {")],
   "FAIL: TestFilesThatAreNotPropertyListsAreRefused"),
   # rig: TestAnXMLDocumentWhoseRootIsNotPlistIsNotAPropertyList

 # The <string> arm given the treatment its four neighbours get, which is the
 # shape of this defect: <integer>, <real> and <date> all trim, so trimming
 # here reads as consistency. It is not -- whitespace inside a string is part
 # of the value, and a config whose value is an indented block or a single
 # space is silently rewritten by the reader that is meant to be reporting on
 # it.
 ("a string is trimmed like a number", [repl(PX,
   "\t\treturn text, nil", "\t\treturn strings.TrimSpace(text), nil")],
   "FAIL: TestWhitespaceInsideAStringIsKept"),
   # rig: TestWhitespaceInsideAPropertyListStringIsKept

 # CoreFoundation wraps base64 payloads across indented lines and
 # encoding/base64 rejects the newlines rather than ignoring them, so this
 # turns every wrapped <data> into a refusal. The import goes with it, as with
 # the UTF-16 entry: strings.Map's predicate is the only use of `unicode` here.
 ("base64 whitespace is not stripped", [
   repl(PX, "\t\tencoded := strings.Map(func(r rune) rune {\n"
        "\t\t\tif unicode.IsSpace(r) {\n\t\t\t\treturn -1\n\t\t\t}\n"
        "\t\t\treturn r\n\t\t}, text)", "\t\tencoded := text"),
   repl(PX, "\t\"unicode\"\n", "")],
   "FAIL: TestBase64DataIsReadAcrossTheLinesCoreFoundationWrapsItOn"),
   # rig: TestBase64DataIsReadAcrossTheLinesCoreFoundationWrapsItOn

 # The rendering is what the plist arm diffs, so a real and the integer beside
 # it printing the same string makes a settings change from 1 to 1.0 -- or the
 # reverse -- invisible to the comparison.
 ("a whole real prints as an integer", [repl(PF,
   "\t\treturn text + \".0\"", "\t\treturn text")],
   "FAIL: TestTheRenderingTellsEveryValueApart"),
   # rig: TestAWholeRealIsNotTheIntegerBesideIt

 # A property-list dictionary is unordered and the Go map this package parses
 # into keeps no order, so without the sort the rendering is Go's randomised
 # map order and two identical documents diff against each other differently on
 # every run. This is the one entry here that needed NO new case: the EXISTING
 # TestTwoSpellingsOfOnePropertyListAreComparedByContentNotBytes already kills
 # it, which was only established by probing it rather than by reading the
 # parked note that said otherwise. The import goes too -- format.go has no
 # other user of sort.
 ("dictionary keys are not sorted", [
   repl(PF, "\t\tsort.Strings(keys)\n", ""),
   repl(PF, "\t\"sort\"\n", "")],
   "FAIL: TestDictionaryKeysAreRenderedInSortedOrder"),
   # rig: TestTwoSpellingsOfOnePropertyListAreComparedByContentNotBytes

 # STILL PARKED, and what remains is one shape rather than a list of unrelated
 # gaps: the HOSTILE INPUTS. Nine mutations to the property-list reader -- eight
 # in binary.go and one in xml.go, which is the XML depth guard and the only one
 # of the nine a hand-written document can reach -- whose defect is visible only
 # on a file built to attack it: an offset pointing outside the object table, a
 # trailer whose offset table wraps, a structure nested past the depth guard, a
 # container declaring more elements than it holds, a scalar referenced enough
 # times to exhaust the budget.
 #
 #   the offset bounds check is removed            offsetOf's range check deleted
 #   the XML nesting guard is removed              element's depth >= maxDepth deleted
 #   the binary nesting guard is removed           object's len(r.open) >= maxDepth deleted
 #   the offset table start is unbounded           the fit check written as an addition
 #   the reference charge is removed               references' spend deleted
 #   the data charge is removed                    the 0x4 arm's spend deleted
 #   the string charge is removed                  the 0x5 arm's spend deleted
 #   the UTF-16 charge is removed                  the 0x6 arm's spend deleted
 #   the charge multiplies instead of dividing     spend's budget/each -> units*each
 #
 # They are OUT OF SCOPE on this ticket rather than forgotten, and the reason is
 # a real obstacle rather than time. internal/plist/plist_test.go builds these
 # fixtures in-package from the layout constants -- trailerSize, offsetSizeAt,
 # refSizeAt, objectCountAt, offsetTableAt -- and a black-box suite cannot
 # import them. Doing it honestly means checking a testdata FILE into
 # test/conformance/testdata per hostile shape. The boundary observable is the
 # same for all nine and is already established by the TWO refusal entries above
 # -- "an unmodelled marker is accepted" and "any XML document element is
 # accepted": a refused plist prints "binary contents differ", while a reader
 # missing its guard crashes, hangs, or prints a plist diff of a file that is
 # not one.
 #
 # One plist entry has no killing CASE and is listed with what it has, because
 # the alternative is to leave it out and let the next reader think it was
 # missed:
 #
 #   the cycle guard is removed                   object's r.open check deleted
 #       killed by the gate, not by a case. A self-referential array recurses
 #       until the goroutine stack is exhausted, which is a fatal error rather
 #       than a failure, so the package's test binary dies and takes the run
 #       with it. TestABinaryPropertyListThatContainsItselfIsRefused holds a
 #       timeout arm for the other shape of the same defect -- one that returns
 #       eventually -- and that arm names the case when it fires.
 #
 # PARKED FOR A REASON THAT IS NOT A MISSING CASE. These three are about the
 # diff search, and no case anywhere can see them today.
 #
 #   the search array is sized by the files       furthest sized 2*(n+m)+1
 #       A claim about memory only. TestTheSearchsMemoryIsAFunctionOfTheBoundAnd
 #       NotOfTheFiles states it inside the package; nothing at the boundary can
 #       observe an allocation, so this is a unit-only property by nature.
 #   the search bound is removed                  maxEdits clamp deleted
 #       TestTwoFilesWithNothingInCommonFallBackToAWholeFileReplacement
 #   the bound is one edit short                  maxEdits 1000 -> 999
 #       TestASearchThatFinishesAtExactlyTheBoundIsStillReadCorrectly
 #       The last two need a conformance fixture whose edit distance exceeds
 #       1000 lines, and it is NOT established that the bounded whole-file
 #       replacement and the unbounded Myers script differ observably on one.
 #       Build the fixture, inject by hand, and only then write the entries --
 #       in that order, since the entry is worth nothing if the two agree.
 #
 # Two mutations were injected and are NOT listed as entries, because they
 # survived and deserve to. Recorded so the next reader does not rediscover
 # them and "fix" the tests to catch what is not there.
 #
 #   * sameContents' `return firstDone && secondDone` -> `return true`. A chunk
 #     short on one side only is a chunk of a different length, which the byte
 #     comparison immediately above has already rejected, so the conjunction is
 #     implied. The line carries a comment saying so. Behaviour-preserving.
 #   * backtrack's `index := k - snapshot.first` -> re-deriving the offset from
 #     len(before)+len(after) and d, which is how it was spelled before the
 #     step struct carried its own first diagonal. NOT behaviour-preserving:
 #     the two disagree by one diagonal on the single step whose window is
 #     clipped at the start of the array, which is the last one when the search
 #     finishes at exactly the bound. It is listed here rather than as an entry
 #     because no input has been found where the value read one diagonal over
 #     changes the path -- four hundred random shapes in that regime and the
 #     two constructed ones all agree. The field exists so the question cannot
 #     arise, not because a case pins it; if one is ever written, promote this.
]





def snapshot_tree():
    for f in FILES:
        dst = os.path.join(BACKUP, f)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copyfile(os.path.join(REPO, f), dst)


def restore():
    for f in FILES:
        shutil.copyfile(os.path.join(BACKUP, f), os.path.join(REPO, f))

# Bounded, because one entry below injects a defect whose whole signature is
# that the suite BLOCKS -- the FIFO guard removed -- and it is killed by the
# harness's own 30s bound firing. If a later change breaks that bound, the
# unbounded form hung the battery itself rather than reporting the entry, and
# an overnight run came back with nothing. The bound here is far above any
# honest gate run; it exists to turn a hang into a result.
RUN_BOUND = 900

def run(cmd):
    # start_new_session puts the shell and everything it spawns in their own
    # process group, so the timeout below can kill the whole tree.
    #
    # subprocess.run's timeout kills only the direct child -- /bin/sh -- and
    # cmd here is a make invocation, so the surviving descendants are `go build`
    # and a running test binary. They kept compiling sources that the next
    # entry was already rewriting underneath them: a phantom DOES-NOT-COMPILE,
    # or a kill credited to a mutation that was never in the tree when the
    # compiler read it. That is the same misattribution the tail and cut guards
    # exist to prevent, arriving by a different road. Orphans also strand
    # macklebox-conformance-bin-* directories that only the harness's one-hour
    # reaper reclaims.
    #
    # Popen rather than subprocess.run because start_new_session plus a
    # killpg needs the pid, and subprocess.run does not hand it out.
    p = subprocess.Popen(cmd, shell=True, cwd=REPO, text=True,
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                         start_new_session=True)
    try:
        out, err = p.communicate(timeout=RUN_BOUND)
        return p.returncode, out + err
    except subprocess.TimeoutExpired:
        try:
            os.killpg(os.getpgid(p.pid), signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            # The group is already gone: the shell exited between the timeout
            # firing and this call. Nothing to kill, and the communicate below
            # still collects what was written.
            p.kill()
        # Reachable only because the group is dead: every writer of these pipes
        # was in it, so the reads see EOF rather than blocking on a descendant
        # that outlived the shell -- which is the deadlock the unkilled version
        # would have had here.
        out, err = p.communicate()
        return 124, (out or "") + (err or "") + "\nbattery: %r did not finish within %ds" % (cmd, RUN_BOUND)

def apply(edits, write=True):
    """Resolve every edit's anchors and, unless write is False, apply them.

    write=False is what --anchors uses. It has to touch nothing: the moment an
    anchor check is worth running is right after editing a file in FILES, when
    the tree is dirty by definition, and a mode that rewrote sources there
    would be asking to lose uncommitted work to an interrupt. Resolving is all
    the check needs, and resolution does not depend on writing because both
    modes resolve against the same in-memory buffer below -- that independence
    is a property this function has to arrange, not one it can assume.
    """
    # One entry's edits accumulate in memory and are written, if at all, only
    # once every anchor has resolved. Re-reading the file per edit made
    # resolution depend on writing: with write=True the previous edit was on
    # disk so the next one saw it, and with write=False it did not -- so
    # --anchors resolved every edit after the first against the PRISTINE file
    # and was blind to exactly the class it was added for, an anchor that
    # stops matching once an earlier edit in the same entry lands. Both modes
    # share this buffer rather than one path each, because two code paths for
    # "what does the next edit see" is the drift the check exists to catch.
    #
    # A side effect worth naming: an entry that fails on its second edit now
    # writes nothing at all, where before it left the first edit on disk for
    # restore() to undo on the next iteration.
    buffered = {}
    touched = set()
    for kind, f, a, b in edits:
        path = os.path.join(REPO, f)
        src = buffered.get(path)
        if src is None:
            src = open(path).read()
        if kind == "repl":
            n = src.count(a)
            if n != 1:
                return None, "anchor %r occurs %d times in %s" % (a[:60], n, f)
            src = src.replace(a, b)
        elif kind == "cut":
            anchored, ending = "\n" + a, "\n" + b
            n = src.count(anchored)
            if n != 1:
                return None, "cut start %r occurs %d times at the start of a line in %s" % (a[:60], n, f)
            i = src.find(anchored) + 1
            j = src.find(ending, i)
            if j < 0:
                return None, "cut end %r not found at the start of a line after the start in %s" % (b[:60], f)
            src = src[:i] + src[j + 1:]
        else:
            i = src.find(a)
            if i < 0:
                return None, "marker %r not found in %s" % (a[:60], f)
            strays = [line for line in src[i + len(a):].splitlines()
                      if line.startswith(("func ", "type ", "var ", "const "))]
            if strays:
                return None, ("tail from %r would also delete %d later top-level "
                              "declaration(s) in %s, starting with %r; this edit only "
                              "means what it names while its marker's declaration is "
                              "the last in the file"
                              % (a[:40], len(strays), f, strays[0][:60]))
            src = src[:i] + b
        buffered[path] = src
        touched.add(f)
    if write:
        for path, src in buffered.items():
            open(path, "w").write(src)
    return touched, None

# apply() writes to whatever path a mutation names, but only paths in FILES are
# copied at startup and put back afterwards -- so a mutation naming a file
# outside that list stays in the working tree for good, while the run still
# prints "tree restored". Nothing tied the two together, so tie them here.
untracked = sorted({edit[1] for entry in MUTATIONS for edit in entry[1]} - set(FILES))
if untracked:
    sys.exit("battery: these mutations edit files that are not in FILES, so they "
             "would be neither backed up nor restored: " + ", ".join(untracked))

# --anchors resolves every edit against the clean tree and exits, without
# running the gate once. Seconds rather than an hour.
#
# It exists because an anchor breaking is not a rare event: the two rounds
# before this one each rewrote a line some entry pointed at, and both times
# the tree was already committed and pushed before the full run reported
# BROKEN-ANCHOR. Nothing in `make check` knows this file exists -- it is not
# in FILES, and mutating it dirties the tree -- so there is no other moment
# that reminds you. Run it after any edit to a file in FILES.
#
# It reuses apply() rather than reimplementing the anchor rules, because a
# second copy of "the anchor must occur exactly once" is a copy that drifts,
# and drifting is what this mode is for. It passes write=False, so it touches
# no file and needs neither a clean tree nor a restore -- which matters,
# because the moment this check is worth running is right after an edit, when
# the tree is dirty by definition. It therefore runs BEFORE the dirty-tree
# refusal below, deliberately.
only = sys.argv[1:]
if "--anchors" in only:
    broken = []
    for entry in MUTATIONS:
        touched, why = apply(entry[1], write=False)
        if touched is None:
            broken.append((entry[0], why))
    for name, why in broken:
        print("BROKEN-ANCHOR  %s: %s" % (name, why))
    print("=== %d entries, %d with a broken anchor ===" % (len(MUTATIONS), len(broken)))
    sys.exit(1 if broken else 0)

# A dirty tree cannot be told apart from a run this script corrupted, and
# restore() would overwrite uncommitted work with the startup copy. Refuse.
dirty = subprocess.run(["git", "status", "--porcelain"], cwd=REPO,
                       capture_output=True, text=True).stdout.strip()
if dirty:
    sys.exit("battery: the working tree is dirty; commit or stash first, since "
             "this rewrites sources in place and restores from a copy taken now:\n" + dirty)

snapshot_tree()

# A name that matches nothing used to run zero mutations and still print
# "NOT KILLED: 0" and exit 0 -- a clean battery run that proved nothing, which
# is the exact vacuous-pass shape the rest of this branch exists to remove. A
# typo, or a wrapper naming a mutation that has since been renamed, got a green
# light. Both halves are needed: the name check catches the typo, and the empty
# check catches a MUTATIONS list that has been emptied or filtered to nothing.
known = {entry[0] for entry in MUTATIONS}
unknown = [name for name in only if name not in known]
if unknown:
    sys.exit("battery: no mutation is named " + ", ".join(repr(u) for u in unknown) +
             "\nknown mutations:\n  " + "\n  ".join(sorted(known)))
results = []
try:
  for entry in MUTATIONS:
    name, edits = entry[0], entry[1]
    expect = entry[2] if len(entry) > 2 else None
    if only and name not in only:
        continue
    restore()
    touched, err = apply(edits)
    if err:
        results.append((name, "BROKEN-ANCHOR", err)); print("BROKEN-ANCHOR  %s: %s" % (name, err), flush=True); continue
    for f in touched:
        run("gofmt -w " + f)
    rc, out = run("go vet -tags conformance ./...")
    if rc != 0:
        results.append((name, "DOES-NOT-COMPILE", out[-400:])); print("DOES-NOT-COMPILE  %s" % name, flush=True); print(out[-800:], flush=True); continue
    rc, out = run("make check")
    if expect is SURVIVES:
        if rc != 0:
            broke = sorted({l.split()[2] for l in out.splitlines() if l.strip().startswith("--- FAIL:")})
            results.append((name, "FALSE-POSITIVE", ",".join(broke)))
            print("FALSE-POSITIVE  %s: the gate rejected correct code (%s)" % (name, ",".join(broke)), flush=True)
        else:
            results.append((name, "survives", "as required"))
            print("survives  %-42s (required: the gate must not reject this)" % name, flush=True)
        continue
    if rc == 0:
        results.append((name, "SURVIVED", "")); print("SURVIVED (BATTERY FAILURE)  %s" % name, flush=True); continue
    failed = sorted({l.split()[2] for l in out.splitlines() if l.strip().startswith("--- FAIL:")})
    if expect and expect not in out:
        results.append((name, "WRONG-DIAGNOSTIC", expect)); print("WRONG-DIAGNOSTIC  %s: expected %r" % (name, expect), flush=True); continue
    # `make check` runs the unit packages first and stops there on a failure,
    # so a mutation the unit tests kill never reaches `make conformance` and
    # says NOTHING about whether the rig can see it. For a mutation to the
    # program itself, ask the rig separately. Without this the battery reports
    # a kill the conformance suite never made.
    conf = ""
    if any(f.startswith(("internal/", "cmd/")) for f in touched):
        crc, cout = run("make conformance")
        cfailed = sorted({l.split()[2] for l in cout.splitlines() if l.strip().startswith("--- FAIL:") and "/" not in l.split()[2]})
        if crc == 0:
            results.append((name, "RIG-BLIND", "")); print("RIG-BLIND  %s: killed only outside the conformance suite" % name, flush=True); continue
        conf = "  [rig: %s]" % ",".join(cfailed)
    results.append((name, "killed", ",".join(failed) + conf))
    print("killed  %-42s by %s%s" % (name, ",".join(failed) or "(build/gate)", conf), flush=True)

finally:
  # In a finally, so Ctrl-C during a run that takes minutes -- the expected way
  # to abort one -- puts the sources back instead of leaving "Usage!" in
  # internal/cli/usage.go for someone to find later.
  restore()

restored_rc, _ = run("make check")
print("\n=== tree restored, make check exit=%d ===" % restored_rc, flush=True)
if restored_rc != 0:
    print("BATTERY LEFT THE TREE BROKEN: restore did not put every mutation back", flush=True)
print("\n=== SUMMARY: %d run ===" % len(results), flush=True)
if not results:
    print("BATTERY RAN NOTHING: a run that exercises no mutation proves nothing", flush=True)
bad = [r for r in results if r[1] not in ("killed", "survives")]
for r in results:
    print("  %-16s %s" % (r[1], r[0]), flush=True)
print("\nNOT KILLED: %d" % len(bad), flush=True)
# The restore check is part of the verdict. Printing it and exiting 0 anyway
# let a wrapper record a clean run over a tree this script had broken.
sys.exit(1 if (bad or restored_rc != 0 or not results) else 0)
