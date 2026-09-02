package homepath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/promptctl/macklebox/internal/fault"
)

func TestAnUnsetOrRelativeHomeIsRefusedInTheUnguardedRegime(t *testing.T) {
	// appspec/03's environment table: HOME "must be set for the program to
	// function; if unset, home-relative operations fail with an uncaught error
	// (nonzero exit)". A relative value is refused in the same regime, because
	// every path the program later resolves would otherwise depend on the
	// working directory it was started in.
	//
	// The rule lives here rather than once per stage so that config load and
	// database assembly cannot come to disagree about what a home directory is
	// -- including about the sentence they print, which is the observable half.
	for _, home := range []string{"", "relative/home", "~/still-relative"} {
		got, err := Require(home)
		if err == nil {
			t.Errorf("Require(%q) returned %q; want a refusal", home, got)
			continue
		}
		if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
			t.Errorf("Require(%q) failed as (%v, declared=%v), want the unguarded regime", home, regime, declared)
		}
	}
}

func TestAnAcceptedHomeIsCleaned(t *testing.T) {
	// Every other rule in this package compares and joins against the returned
	// string, and Inside decides containment lexically, so an uncleaned home
	// would make a path plainly inside it compare as outside.
	home := filepath.Join(string(filepath.Separator), "home", "user")
	got, err := Require(home + string(filepath.Separator) + "." + string(filepath.Separator))
	if err != nil {
		t.Fatalf("Require of an absolute home failed: %v", err)
	}
	if got != home {
		t.Errorf("Require returned %q, want the cleaned %q", got, home)
	}
}

func TestATildeIsExpandedOnlyWhenItNamesTheHomeDirectory(t *testing.T) {
	// appspec/03 specifies "~" and "~/..." and nothing else. "~otheruser/x" is
	// left alone deliberately: expanding it would mean reading the password
	// database to produce a path the specification never mentions, and the
	// caller resolves what comes back like any other relative path.
	home := "/home/person"
	for _, c := range []struct{ path, want string }{
		{"~", home},
		{"~/x.cfg", "/home/person/x.cfg"},
		{"~otheruser/x.cfg", "~otheruser/x.cfg"},
		{"~x", "~x"},
		{"/abs/x.cfg", "/abs/x.cfg"},
		{"relative/x.cfg", "relative/x.cfg"},
		{"", ""},
	} {
		if got := Expand(c.path, home); got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestARelativePathIsMadeAbsoluteAgainstTheWorkingDirectory(t *testing.T) {
	// The rule for the two environment-named paths: neither specification
	// gives them the home-relative reading appspec/03 gives -c, so they take
	// the ordinary one. The point of the function is that the result is
	// absolute -- a relative path left alone compares as outside the home
	// directory for the wrong reason, which is how a containment check passes
	// while meaning nothing.
	working, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	if got, want := Absolute("x/y"), filepath.Join(working, "x/y"); got != want {
		t.Errorf("Absolute(%q) = %q, want %q", "x/y", got, want)
	}
	if got, want := Absolute("/a/b/../c"), "/a/c"; got != want {
		t.Errorf("Absolute(%q) = %q, want it cleaned to %q", "/a/b/../c", got, want)
	}
}

func TestContainmentIsDecidedByPathElementsAndNotByPrefix(t *testing.T) {
	// The case that makes a prefix test wrong: "~/..config" is a dotted name
	// inside the home directory, and this program manages dotted names. A
	// HasPrefix on ".." reports it outside, which would refuse an ordinary
	// config file. The sibling case is "/home/personal", which shares a string
	// prefix with "/home/person" and is a different directory.
	home := "/home/person"
	for _, c := range []struct {
		path string
		want bool
	}{
		{"/home/person/.mackup.cfg", true},
		{"/home/person/..config", true},
		{"/home/person/a/b/c", true},
		{"/home/person", true},
		{"/home/personal/.mackup.cfg", false},
		{"/home/person/../other/.mackup.cfg", false},
		{"/etc/mackup.cfg", false},
		{"/home", false},
	} {
		if got := Inside(c.path, home); got != c.want {
			t.Errorf("Inside(%q, %q) = %v, want %v", c.path, home, got, c.want)
		}
	}
}

func TestTheXDGBaseDefaultsToDotConfigUnderHome(t *testing.T) {
	// appspec/03 and appspec/05 state the same default. An empty value is the
	// unset one -- appspec/03 says so of $MACKUP_CONFIG in the same paragraph
	// -- and taking it literally would resolve the base to the working
	// directory, which is neither home-relative nor the same on two runs.
	home := "/home/person"
	if got, want := ConfigHome("", home), "/home/person/.config"; got != want {
		t.Errorf("ConfigHome(\"\") = %q, want %q", got, want)
	}
	if got, want := ConfigHome("~/elsewhere", home), "/home/person/elsewhere"; got != want {
		t.Errorf("ConfigHome(%q) = %q, want the tilde expanded to %q", "~/elsewhere", got, want)
	}
	if got, want := ConfigHome("/somewhere/else", home), "/somewhere/else"; got != want {
		t.Errorf("ConfigHome(%q) = %q, want %q", "/somewhere/else", got, want)
	}

	// A relative value resolves against the working directory and is returned
	// absolute, so the caller's containment check compares two absolute paths.
	working, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	if got, want := ConfigHome("xdg", home), filepath.Join(working, "xdg"); got != want {
		t.Errorf("ConfigHome(%q) = %q, want %q", "xdg", got, want)
	}
}

func TestTheXDGBaseIsNotItselfCheckedForContainment(t *testing.T) {
	// The division of labour between this function and its callers, pinned so
	// that "helpfully" folding the check in here is a test failure rather than
	// a silent stage change. appspec/05 puts the containment failure in
	// database assembly; appspec/03 does not make it of the config candidate at
	// all. A ConfigHome that refused an outside base would move the diagnostic
	// to config load, where appspec/07's table has no row for it.
	if got, want := ConfigHome("/outside", "/home/person"), "/outside"; got != want {
		t.Errorf("ConfigHome(%q) = %q, want %q returned rather than refused", "/outside", got, want)
	}
	if Inside("/outside", "/home/person") {
		t.Error("the caller's own containment check reports /outside as inside /home/person")
	}
}
