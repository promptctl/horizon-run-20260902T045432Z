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

import atexit, os, shutil, subprocess, sys, tempfile

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
         "internal/version/version.go"]

H = "test/conformance/harness_test.go"
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
    return ("tail", f, marker, new)

def cut(f, start, end):
    """Remove everything from `start` up to, but not including, `end`.

    For deleting a whole switch arm or block. Replacing the BODY of an arm and
    calling it deleted is what left the FIFO branch standing while its entry
    reported a kill, so the shape that removes the arm itself has its own kind
    rather than a hand-written repl each time.
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
   "\tcase inv.Opts.Version:\n\t\tstreams.Outln(version.Banner())\n\t\treturn ExitOK\n",
   "\tcase inv.Opts.Version:\n\t\tif len(argv) > 1 {\n\t\t\tstreams.Outln(cli.Usage)\n\t\t\treturn ExitOK\n\t\t}\n\t\tstreams.Outln(version.Banner())\n\t\treturn ExitOK\n")],
   None),

 ("the argv scan does not stop at --help", [repl(A,
   "\tcase inv.Opts.Help:\n\t\tstreams.Outln(cli.Usage)\n\t\treturn ExitOK\n",
   "\tcase inv.Opts.Help:\n\t\tif len(argv) > 1 {\n\t\t\tstreams.Errf(\"mackup: unrecognized option: %s\\n\", argv[len(argv)-1])\n\t\t}\n\t\tstreams.Outln(cli.Usage)\n\t\treturn ExitOK\n")],
   None),

 ("declaration slid under a spec-level doc comment", [repl(H,
   "\t// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.\n\tmackupVCSBuildErr error",
   "\t// mackupVCSBuildErr says why mackupVCSBin is empty, when it is.\n\tinsertedUnderTheComment error\n\n\tmackupVCSBuildErr error")],
   'the doc comment on insertedUnderTheComment opens with "mackupVCSBuildErr"'),

 ("working-tree provenance outranks the linker stamp", [repl(V,
   "\tif value != \"\" {\n\t\treturn normalize(value)\n\t}\n",
   "\tif bi, ok := readBuildInfo(); ok {\n\t\tfor _, setting := range bi.Settings {\n\t\t\tif setting.Key == \"vcs.revision\" {\n\t\t\t\treturn Fallback\n\t\t\t}\n\t\t}\n\t}\n\tif value != \"\" {\n\t\treturn normalize(value)\n\t}\n")],
   None),

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
 ("doc stranded on an inserted test function", [repl(H,
   "func moduleRoot() (string, error) {",
   "func TestInserted(t *testing.T) {}\n\nfunc moduleRoot() (string, error) {")],
   'the doc comment on the test TestInserted opens with "moduleRoot"'),

 ("a test function may carry an ordinary explanatory comment", [repl(H,
   "// moduleRoot is the directory holding go.mod, found by walking up from the",
   "// Regression for the round-9 argv scan bug.\nfunc TestOrdinaryComment(t *testing.T) {}\n\n// moduleRoot is the directory holding go.mod, found by walking up from the")],
   SURVIVES),

 ("the symlink target is not recorded", [repl(H,
   '\t\t\ttarget, err := os.Readlink(path)\n\t\t\tif err != nil {\n\t\t\t\treturn err\n\t\t\t}\n',
   '\t\t\ttarget := "constant"\n\t\t\t_ = os.Readlink\n')],
   'want it to end with "-> real.txt"'),

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

 ("the version banner gains a suffix", [repl(V,
   'return "Mackup " + String()', 'return "Mackup " + String() + "-extra"')],
   None),
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
    try:
        p = subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True, text=True,
                           timeout=RUN_BOUND)
    except subprocess.TimeoutExpired as expired:
        out = (expired.stdout or b"") if isinstance(expired.stdout, bytes) else (expired.stdout or "")
        if isinstance(out, bytes):
            out = out.decode("utf-8", "replace")
        return 124, out + "\nbattery: %r did not finish within %ds" % (cmd, RUN_BOUND)
    return p.returncode, p.stdout + p.stderr

def apply(edits):
    touched = set()
    for kind, f, a, b in edits:
        path = os.path.join(REPO, f)
        src = open(path).read()
        if kind == "repl":
            n = src.count(a)
            if n != 1:
                return None, "anchor %r occurs %d times in %s" % (a[:60], n, f)
            src = src.replace(a, b)
        elif kind == "cut":
            n = src.count(a)
            if n != 1:
                return None, "cut start %r occurs %d times in %s" % (a[:60], n, f)
            i = src.find(a)
            j = src.find(b, i)
            if j < 0:
                return None, "cut end %r not found after the start in %s" % (b[:60], f)
            src = src[:i] + src[j:]
        else:
            i = src.find(a)
            if i < 0:
                return None, "marker %r not found in %s" % (a[:60], f)
            src = src[:i] + b
        open(path, "w").write(src)
        touched.add(f)
    return touched, None

# apply() writes to whatever path a mutation names, but only paths in FILES are
# copied at startup and put back afterwards -- so a mutation naming a file
# outside that list stays in the working tree for good, while the run still
# prints "tree restored". Nothing tied the two together, so tie them here.
untracked = sorted({edit[1] for entry in MUTATIONS for edit in entry[1]} - set(FILES))
if untracked:
    sys.exit("battery: these mutations edit files that are not in FILES, so they "
             "would be neither backed up nor restored: " + ", ".join(untracked))

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
only = sys.argv[1:]
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
            conf = "  [rig BLIND]"
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
