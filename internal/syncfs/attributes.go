package syncfs

import (
	"os"
	"os/exec"
	"runtime"
)

// The attribute cleanup of appspec/06-sync-operations.md "Attribute cleanup":
// the external commands that strip ACLs and immutable flags before this package
// deletes or chmods a path. appspec/06 calls them "the only subprocesses the
// program spawns during sync", so this file is the whole of the program's
// process-spawning surface and is kept apart from the primitives for that
// reason.

// attributeCleanups gives the cleanup commands appspec/06 specifies for one
// operating system, as argv slices with the path already appended.
//
// A pure function of (goos, path) rather than a switch inside the caller,
// because the platform table is the part worth pinning: appspec/00 "Platform
// assumptions" lists per-platform attribute commands as one of exactly three
// places behavior differs between macOS and Linux, and running Linux's `setfacl
// -R -b` on macOS -- or macOS's `chmod -R -N` on Linux, where /bin/chmod exists
// and does not have that flag -- is a defect that leaves no trace, since these
// are best-effort and their failures are discarded.
//
// The commands are recursive and are given the path itself, which is why the
// callers invoke this once at the top of an operation and not per entry of a
// walked tree.
//
// Any other GOOS gets no cleanup. appspec/00 says the program targets macOS and
// Linux, and neither specification names a command for a third platform;
// guessing one would spawn a process no reference behavior asks for.
func attributeCleanups(goos, path string) [][]string {
	switch goos {
	case "darwin":
		return [][]string{
			{"/bin/chmod", "-R", "-N", path},
			{"/usr/bin/chflags", "-R", "nouchg", path},
		}
	case "linux":
		return [][]string{
			{"/bin/setfacl", "-R", "-b", path},
			{"/usr/bin/chattr", "-R", "-f", "-i", path},
		}
	default:
		return nil
	}
}

// runCleanup runs one cleanup command. It is a variable so that a test can
// observe WHICH commands an operation asked for without spawning anything --
// the same seam internal/app gives os.Geteuid, and for the same reason: the
// behavior worth pinning is the call, and the call is invisible once the
// process it makes has been discarded.
var runCleanup = runCleanupCommand

// runCleanupCommand runs one cleanup command, best-effort.
//
// appspec/06: "These are best-effort: if the binary is absent, that cleanup
// step is skipped." So the binary's existence is checked and every other
// failure is discarded -- a non-zero exit, an unexecutable file, a path that is
// a directory. Nothing about a file's attributes is a reason to fail a sync:
// the chmod or the delete that follows is what has to succeed, and it reports
// its own failure through the partial-failure contract of appspec/06.
//
// The absence check changes no outcome, and saying so is the honest way to
// keep it. exec would fail with its own ENOENT and that failure would be
// discarded here to exactly the same effect, so nothing this package can be
// asked -- no test in it, and no observation of the filesystem afterwards --
// tells the two versions apart. Confirmed by injection: removing the check
// leaves every case in this package passing.
//
// It stays for two reasons that are not "it makes the program correct". It is
// the condition appspec/06 states, and this package's whole method is to spell
// the specification's conditions rather than to arrive at their consequences by
// another route. And the absent case is the common one, not a corner:
// /bin/setfacl is missing on a Linux system without the acl package and
// /usr/bin/chattr on one without e2fsprogs, so without the check a backup would
// fork twice per file for processes that cannot run.
//
// os.Stat and not an executability test: appspec/06 says "only if that binary
// exists", and a binary that exists but cannot be executed by this user is a
// failure of the same best-effort kind as a non-zero exit.
func runCleanupCommand(argv []string) {
	if _, err := os.Stat(argv[0]); err != nil {
		return
	}
	_ = exec.Command(argv[0], argv[1:]...).Run()
}

// cleanAttributes strips the attributes that would block modification of a
// path, recursively, before it is deleted or chmod-ed.
//
// appspec/06 states this as a precondition "shared by the delete and chmod
// primitives, so it holds across all operations that remove or overwrite" --
// which is why it is called by Delete and by Clamp and by nothing else. Copy
// and Link inherit it through Clamp, which is a post-condition of both, so
// every path this package writes to has been through here.
//
// It returns nothing. There is no failure to report: every step is best-effort
// by appspec/06's own words, so an error here would be a value no caller could
// act on without contradicting the specification.
func cleanAttributes(path string) {
	for _, argv := range attributeCleanups(runtime.GOOS, path) {
		runCleanup(argv)
	}
}
