package storage

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/fault"
)

// The Google Drive fixtures under testdata are real SQLite databases carrying
// the schema and the row appspec/04 names. They are committed rather than
// built because this program cannot write a SQLite file, so a generated
// fixture would only prove the reader agrees with itself.
//
//	sync_config.db  the row present and usable
//	other_root.db   a different root, so a case can tell WHICH file was read
//	no_root.db      the table without the local_sync_root_path row
//	empty_root.db   the row present and empty, which appspec/04 calls unusable
const (
	driveOK       = "testdata/sync_config.db"
	driveOther    = "testdata/other_root.db"
	driveNoRoot   = "testdata/no_root.db"
	driveNoValue  = "testdata/empty_root.db"
	driveRootPath = "/Users/someone/Google Drive"
)

// newHome returns an empty home directory for one case.
func newHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// writeHostDB writes a Dropbox host database holding the given tokens.
func writeHostDB(t *testing.T, home string, lines ...string) {
	t.Helper()
	path := filepath.Join(home, hostDB)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// installDriveDB copies one of the fixtures to a home-relative path, so a case
// can put a database at either of the two locations appspec/04 checks.
func installDriveDB(t *testing.T, home, fixture, relative string) {
	t.Helper()
	content, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("reading %s: %v", fixture, err)
	}
	path := filepath.Join(home, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestTheFourEngineNamesAreTheClosedSet(t *testing.T) {
	// appspec/04: "a closed enumeration of exactly four members ... where 'any
	// other value' is unrepresentable". EngineNamed is the type boundary, so
	// it is the one place that can be wrong in either direction: accepting a
	// fifth name, or refusing one of the four.
	for _, name := range []string{"dropbox", "google_drive", "icloud", "file_system"} {
		engine, known := EngineNamed(name)
		if !known {
			t.Errorf("EngineNamed(%q) refused a name appspec/03 lists", name)
			continue
		}
		if engine.String() != name {
			t.Errorf("EngineNamed(%q).String() = %q, want the name it was built from", name, engine)
		}
	}
	// appspec/03: "Value is matched exactly (case-sensitive)". The casings
	// below are the ones a user would plausibly write, and every one of them
	// must be an unknown engine rather than a silent success.
	for _, name := range []string{"Dropbox", "DROPBOX", "Google_Drive", "iCloud", "File_System", "", " dropbox", "dropbox ", "s3"} {
		if _, known := EngineNamed(name); known {
			t.Errorf("EngineNamed(%q) was accepted; appspec/03 matches the value exactly", name)
		}
	}
}

func TestTheDefaultEngineIsDropbox(t *testing.T) {
	// appspec/03 gives [storage] engine the default "dropbox", and the zero
	// value of Engine is what a Config that never set one carries. Pinning it
	// here means a member reordered into first position is caught by a case
	// that says why the order matters.
	var unset Engine
	if unset != Dropbox {
		t.Errorf("the zero Engine is %v, want dropbox: appspec/03 makes it the default when the config names no engine", unset)
	}
}

func TestDropboxDecodesTheSecondTokenOfTheHostDatabase(t *testing.T) {
	home := newHome(t)
	// The shape appspec/04 describes: whitespace-separated tokens, the second
	// one Base64. A real host.db opens with a revision number on its own line.
	const want = "/home/some_user/Dropbox"
	writeHostDB(t, home, "6103", base64.StdEncoding.EncodeToString([]byte(want)))

	root, err := Resolve(Dropbox, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if root != want {
		t.Errorf("Resolve = %q, want %q", root, want)
	}
}

func TestDropboxReadsAPathHoldingSpaces(t *testing.T) {
	// The decoded bytes are the whole path. A resolver that split the DECODED
	// value on whitespace -- rather than only the file's tokens -- would
	// truncate this one and still pass every single-word case.
	home := newHome(t)
	const want = "/home/some user/Dropbox (Work)"
	writeHostDB(t, home, "6103", base64.StdEncoding.EncodeToString([]byte(want)))

	root, err := Resolve(Dropbox, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if root != want {
		t.Errorf("Resolve = %q, want %q", root, want)
	}
}

func TestDropboxDoesNotRequireTheDecodedFolderToExist(t *testing.T) {
	// appspec/04 clause 2 admits two shapes of auto-detection: a path that
	// exists by construction, "or is read from an existing client database".
	// Dropbox is the second, and its list of failures is the FILE's -- missing,
	// short, un-decodable -- and says nothing about the folder. A resolver
	// that also stat'd the decoded path would move the failure of a deleted
	// Dropbox folder from the environment gate's "Unable to find the storage
	// folder" message to this engine's, which is a different message on a
	// different line of appspec/07's table.
	home := newHome(t)
	const want = "/nowhere/at/all/Dropbox"
	writeHostDB(t, home, "6103", base64.StdEncoding.EncodeToString([]byte(want)))

	root, err := Resolve(Dropbox, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v, want the decoded path even though nothing is there", err)
	}
	if root != want {
		t.Errorf("Resolve = %q, want %q", root, want)
	}
}

func TestEveryDropboxFailureIsTheOneGuardedProviderMessage(t *testing.T) {
	// appspec/04 lists four distinct causes -- "the file missing or
	// unreadable, fewer than two tokens, or the second token not being valid
	// Base64 / not decodable to text" -- and gives them one message. So the
	// case asserts the message is the same for all of them, not merely that
	// each fails.
	for _, c := range []struct {
		what  string
		lines []string
	}{
		{"one token", []string{"6103"}},
		{"an empty file", []string{""}},
		{"a second token that is not Base64", []string{"6103", "not base64 at all!"}},
		{"Base64 with the wrong padding, which strict decoding refuses", []string{"6103", "L2hvbWUvdQ"}},
		{"a token decoding to bytes that are not text", []string{"6103", base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe, 0x00})}},
		{"a token decoding to nothing", []string{"6103", ""}},
	} {
		home := newHome(t)
		writeHostDB(t, home, c.lines...)
		_, err := Resolve(Dropbox, home, "")
		expectProviderFailure(t, c.what, err, "Dropbox install")
	}

	// And the file not being there at all, which needs no fixture.
	_, err := Resolve(Dropbox, newHome(t), "")
	expectProviderFailure(t, "no host database at all", err, "Dropbox install")
}

// expectProviderFailure asserts that an error is the guarded, multi-line
// "unable to find your <provider>" diagnostic appspec/04 gives the three
// auto-detecting engines.
func expectProviderFailure(t *testing.T, what string, err error, provider string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: Resolve succeeded, want the provider failure", what)
		return
	}
	regime, declared := fault.RegimeOf(err)
	if !declared {
		t.Errorf("%s: Resolve returned an unclassified error %v; appspec/07 puts this row in the guarded column", what, err)
		return
	}
	if regime != fault.Guarded {
		t.Errorf("%s: Resolve failed %v, want guarded: appspec/07 gives the storage-folder rows a clean exit 1", what, regime)
	}
	message := err.Error()
	if want := "Unable to find your " + provider + " =("; !strings.HasPrefix(message, want) {
		t.Errorf("%s: Resolve = %q, want it to open with %q", what, message, want)
	}
	if !strings.Contains(message, documentationURL) {
		t.Errorf("%s: Resolve = %q, want it to carry the documentation pointer appspec/04 requires", what, message)
	}
	if lines := strings.Count(message, "\n") + 1; lines < 3 {
		t.Errorf("%s: Resolve = %q, which is %d lines; appspec/04 gives this message the provider line, guidance, and a URL", what, message, lines)
	}
}

func TestGoogleDriveReadsTheLocalSyncRootOutOfTheClientDatabase(t *testing.T) {
	home := newHome(t)
	installDriveDB(t, home, driveOK, filepath.Join(driveSupportDir, drivePreferred))

	root, err := Resolve(GoogleDrive, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if root != driveRootPath {
		t.Errorf("Resolve = %q, want %q", root, driveRootPath)
	}
}

func TestGoogleDrivePrefersTheUserDefaultDatabase(t *testing.T) {
	// appspec/04 gives the user_default path first, "if it exists", and the
	// one beside it otherwise. Both are installed with DIFFERENT roots, so the
	// case fails if the order is reversed -- which a case installing only one
	// at a time cannot detect.
	home := newHome(t)
	installDriveDB(t, home, driveOK, filepath.Join(driveSupportDir, drivePreferred))
	installDriveDB(t, home, driveOther, filepath.Join(driveSupportDir, driveFallback))

	root, err := Resolve(GoogleDrive, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if root != driveRootPath {
		t.Errorf("Resolve = %q, want the user_default database's %q", root, driveRootPath)
	}
}

func TestGoogleDriveFallsBackToTheDatabaseBesideIt(t *testing.T) {
	home := newHome(t)
	installDriveDB(t, home, driveOther, filepath.Join(driveSupportDir, driveFallback))

	root, err := Resolve(GoogleDrive, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "/Users/someone/Fallback Drive"; root != want {
		t.Errorf("Resolve = %q, want %q", root, want)
	}
}

func TestEveryGoogleDriveFailureIsTheOneGuardedProviderMessage(t *testing.T) {
	// appspec/04 again gives one message to several causes: "If neither DB
	// file exists, or the query yields no usable value, or the DB cannot be
	// opened/queried".
	t.Run("neither database exists", func(t *testing.T) {
		_, err := Resolve(GoogleDrive, newHome(t), "")
		expectProviderFailure(t, "neither database exists", err, "Google Drive install")
	})
	t.Run("the row is absent", func(t *testing.T) {
		home := newHome(t)
		installDriveDB(t, home, driveNoRoot, filepath.Join(driveSupportDir, drivePreferred))
		_, err := Resolve(GoogleDrive, home, "")
		expectProviderFailure(t, "the row is absent", err, "Google Drive install")
	})
	t.Run("the value is empty", func(t *testing.T) {
		// appspec/04 wants the value "present and non-empty", so an empty
		// string is a failure and not a storage root of "".
		home := newHome(t)
		installDriveDB(t, home, driveNoValue, filepath.Join(driveSupportDir, drivePreferred))
		_, err := Resolve(GoogleDrive, home, "")
		expectProviderFailure(t, "the value is empty", err, "Google Drive install")
	})
	t.Run("the database cannot be read", func(t *testing.T) {
		home := newHome(t)
		path := filepath.Join(home, driveSupportDir, drivePreferred)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("creating the support directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
			t.Fatalf("writing the fixture: %v", err)
		}
		_, err := Resolve(GoogleDrive, home, "")
		expectProviderFailure(t, "the database cannot be read", err, "Google Drive install")
	})
	t.Run("the preferred database is a directory", func(t *testing.T) {
		// Existence alone does not make a candidate readable, and a
		// non-regular file at the preferred path must not be chosen -- but it
		// must not silently fall through to the other one either, because
		// nothing here is installed at the fallback path.
		home := newHome(t)
		if err := os.MkdirAll(filepath.Join(home, driveSupportDir, drivePreferred), 0o700); err != nil {
			t.Fatalf("creating the fixture: %v", err)
		}
		_, err := Resolve(GoogleDrive, home, "")
		expectProviderFailure(t, "the preferred database is a directory", err, "Google Drive install")
	})
}

func TestICloudResolvesToTheFixedLocationWhenItExists(t *testing.T) {
	home := newHome(t)
	want := filepath.Join(home, iCloudDir)
	if err := os.MkdirAll(want, 0o700); err != nil {
		t.Fatalf("creating the iCloud directory: %v", err)
	}

	root, err := Resolve(ICloud, home, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if root != want {
		t.Errorf("Resolve = %q, want %q", root, want)
	}
}

func TestICloudFailsWhenTheDirectoryIsAbsentOrNotADirectory(t *testing.T) {
	// appspec/04: "the existence of that directory *is* the resolution". A
	// regular file at that path is not the iCloud Drive folder, so it is the
	// same failure as nothing being there -- and a resolver that only checked
	// for the path's existence would return it as a storage root.
	t.Run("absent", func(t *testing.T) {
		_, err := Resolve(ICloud, newHome(t), "")
		expectProviderFailure(t, "absent", err, "iCloud Drive")
	})
	t.Run("a regular file", func(t *testing.T) {
		home := newHome(t)
		path := filepath.Join(home, iCloudDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("creating the parent: %v", err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("writing the fixture: %v", err)
		}
		_, err := Resolve(ICloud, home, "")
		expectProviderFailure(t, "a regular file", err, "iCloud Drive")
	})
}

func TestFileSystemResolvesThePathTheUserGave(t *testing.T) {
	home := newHome(t)
	for _, c := range []struct{ what, path, want string }{
		{"a relative path, resolved under home", "some/folder", filepath.Join(home, "some/folder")},
		{"a bare name", "storage", filepath.Join(home, "storage")},
		{"an absolute path, used verbatim", "/abs/folder", "/abs/folder"},
		{"a path holding spaces, unquoted", "my sync folder", filepath.Join(home, "my sync folder")},
		{"an absolute path holding spaces", "/Volumes/Big Disk/sync", "/Volumes/Big Disk/sync"},
	} {
		root, err := Resolve(FileSystem, home, c.path)
		if err != nil {
			t.Errorf("%s: Resolve(%q) = %v", c.what, c.path, err)
			continue
		}
		if root != c.want {
			t.Errorf("%s: Resolve(%q) = %q, want %q", c.what, c.path, root, c.want)
		}
	}
}

func TestFileSystemDoesNotCheckThatThePathExists(t *testing.T) {
	// THE contract of appspec/04 clause 2, and the one a reimplementer is told
	// by name not to "fix": the uniform existence guarantee is the environment
	// gate's, not the resolver's, and the deferral is observable because the
	// user sees "Unable to find the storage folder: <path>" from the gate
	// instead of this engine's message. A stat added here would move that
	// message and break macklebox-resolvers-5iw.4's half of the contract.
	home := newHome(t)
	want := filepath.Join(home, "nowhere/at/all")

	root, err := Resolve(FileSystem, home, "nowhere/at/all")
	if err != nil {
		t.Fatalf("Resolve on a nonexistent path = %v, want the path itself: appspec/04 forbids an existence check in this engine", err)
	}
	if root != want {
		t.Errorf("Resolve = %q, want %q", root, want)
	}
}

func TestFileSystemWithNoPathIsTheUnguardedFailure(t *testing.T) {
	// appspec/04 clause 3 makes this the one exception to the guarded shape
	// its three siblings use, and appspec/07's table agrees: "file_system
	// engine with no path" is unguarded, while every "storage folder not
	// locatable" row is guarded. Getting this backwards is invisible to a
	// case that only asserts the run failed.
	_, err := Resolve(FileSystem, newHome(t), "")
	if err == nil {
		t.Fatal("Resolve with no path succeeded")
	}
	regime, declared := fault.RegimeOf(err)
	if !declared {
		t.Fatalf("Resolve = %v, an unclassified error; appspec/07 puts this row in the unguarded column", err)
	}
	if regime != fault.Unguarded {
		t.Errorf("Resolve failed %v, want unguarded", regime)
	}
	// appspec/02 requires an unguarded diagnostic to name the offending value.
	// Here the offending value is the engine whose requirement went unmet.
	if !strings.Contains(err.Error(), "file_system") {
		t.Errorf("Resolve = %q, want it to name the engine", err)
	}
}

func TestAnEngineWithNoResolverFailsRatherThanCrashing(t *testing.T) {
	// resolverFor's default arm, which is unreachable through EngineNamed. It
	// exists so that adding a fifth member to the enumeration and forgetting
	// its arm produces a diagnostic during config load instead of a nil
	// dereference; a guard nothing exercises is worth no more than no guard.
	_, err := Resolve(Engine(99), newHome(t), "")
	if err == nil {
		t.Fatal("Resolve on an engine with no resolver succeeded")
	}
	if !strings.Contains(err.Error(), "Engine(99)") {
		t.Errorf("Resolve = %q, want it to name the engine it could not resolve", err)
	}
}
