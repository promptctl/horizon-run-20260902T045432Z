package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/ui"
)

// The unit half of backup and restore: the pieces of the one copy operation
// that can be asked a question directly -- the direction record, the
// confirmation mechanism, the two folder gates and the prompt's type noun.
//
// What the black-box suite cannot see is why these exist. test/conformance
// observes the program through argv and two streams, so it can pin the wording
// of a prompt and the exit code after one; it cannot show that BOTH force
// policies answer without reading stdin at all, or that the reader survives
// between two prompts, or that a gate refuses a path that exists and is not a
// directory. Each of those is a property of a function, and this is where a
// property of a function is asserted.

// promptRun is one confirmer wired to captured streams, so a case can assert
// what was asked as well as what was answered.
type promptRun struct {
	confirm confirmer
	streams *ui.IO
	out     *bytes.Buffer
	err     *bytes.Buffer
}

// newPromptRun builds a confirmer under one policy, with input available on
// stdin.
func newPromptRun(p policy, input string) *promptRun {
	var out, errb bytes.Buffer
	streams := &ui.IO{In: strings.NewReader(input), Out: &out, Err: &errb}
	return &promptRun{
		confirm: confirmer{policy: p, streams: streams},
		streams: streams,
		out:     &out,
		err:     &errb,
	}
}

func (p *promptRun) outText() string { return ui.StripColor(p.out.String()) }

func TestPolicyOfReadsTheConfirmationDecisionOffTheOptions(t *testing.T) {
	// appspec/01 section 3: the confirmation policy is "a single three-valued
	// decision, fixed for the whole run ... force = auto-yes, force-no =
	// auto-no, neither = ask on stdin".
	//
	// The fourth row is the one worth writing down. Both flags together are
	// rejected at parse time, so this function is never asked to resolve the
	// contradiction -- and it answers NO if it ever is, because the safe half
	// of a conflict it cannot see is the one that changes nothing.
	for _, test := range []struct {
		what string
		opts cli.Options
		want policy
	}{
		{"neither flag", cli.Options{}, policyAsk},
		{"--force", cli.Options{Force: true}, policyYes},
		{"--force-no", cli.Options{ForceNo: true}, policyNo},
		{"both, which the parser refuses first", cli.Options{Force: true, ForceNo: true}, policyNo},
	} {
		if got := policyOf(test.opts); got != test.want {
			t.Errorf("policyOf(%s) = %v, want %v", test.what, got, test.want)
		}
	}
}

func TestTheForcePoliciesAnswerWithoutAskingAnything(t *testing.T) {
	// appspec/07: --force "pre-answers every prompt with yes: no prompt is
	// shown"; --force-no does the same with no.
	//
	// Nothing is read, either. The input below would answer the opposite way
	// if it were consulted, so a policy that printed nothing and still read
	// stdin -- which passes every assertion the conformance suite can make
	// about a silent stdout -- fails here on the answer.
	for _, test := range []struct {
		what  string
		p     policy
		input string
		want  bool
	}{
		{"--force", policyYes, "no\n", true},
		{"--force-no", policyNo, "yes\n", false},
	} {
		run := newPromptRun(test.p, test.input)

		answer, err := run.confirm.Ask("Are you sure that you want to replace it?")

		if err != nil {
			t.Errorf("%s: Ask returned %v, want no error", test.what, err)
		}
		if answer != test.want {
			t.Errorf("%s: Ask answered %v, want %v", test.what, answer, test.want)
		}
		if run.out.Len() != 0 || run.err.Len() != 0 {
			t.Errorf("%s: Ask printed stdout %q and stderr %q, want nothing: no prompt is shown",
				test.what, run.out.String(), run.err.String())
		}
	}
}

func TestTheAcceptedAnswersAreTheOnesAppspec07Lists(t *testing.T) {
	// "Accepted yes answers (case-insensitive): yes, y. Accepted no answers:
	// no, n."
	//
	// The spellings with surrounding whitespace and a CRLF terminator are here
	// because they are what real input carries: a terminal on a machine
	// sending CRLF, and a file of answers written on one. An implementation
	// comparing the raw line would re-ask forever against a finite input,
	// which is a hang rather than a wrong answer.
	for _, test := range []struct {
		input string
		want  bool
	}{
		{"yes\n", true},
		{"y\n", true},
		{"YES\n", true},
		{"Y\n", true},
		{"  yes  \n", true},
		{"yes\r\n", true},
		{"no\n", false},
		{"n\n", false},
		{"No\n", false},
		{"N\r\n", false},
	} {
		run := newPromptRun(policyAsk, test.input)

		answer, err := run.confirm.Ask("Are you sure?")

		if err != nil {
			t.Errorf("Ask(%q) returned %v, want no error", test.input, err)
			continue
		}
		if answer != test.want {
			t.Errorf("Ask(%q) answered %v, want %v", test.input, answer, test.want)
		}
	}
}

func TestAnUnrecognizedAnswerReAsksTheSameQuestion(t *testing.T) {
	// appspec/07: "any other input re-asks the same question (the loop repeats
	// until a recognized answer is given)".
	//
	// The empty line is in the input on purpose: the vocabulary does not hold
	// the empty string, so a bare return is "any other input" and not a
	// default. That is the reading under which a stray keypress cannot delete
	// a file.
	run := newPromptRun(policyAsk, "maybe\n\nyes\n")

	answer, err := run.confirm.Ask("Are you sure?")

	if err != nil || !answer {
		t.Fatalf("Ask answered (%v, %v), want (true, nil) once a recognized answer arrived", answer, err)
	}
	if got := strings.Count(run.outText(), "Are you sure?"); got != 3 {
		t.Errorf("the question was asked %d times for three answers, want 3\nstdout: %q", got, run.out.String())
	}
}

func TestEndOfInputAtAPromptIsAnUnguardedFailureAndNotAnAnswer(t *testing.T) {
	// appspec/07: "if a prompt is reached with no force flag and stdin reaches
	// end-of-input ... the program cannot obtain a valid answer and terminates
	// with a nonzero exit (an unhandled end-of-input condition -- the
	// unguarded regime)".
	//
	// The regime is the assertion, not merely the error. Returning false and
	// no error would be the program answering for a user who is not there, and
	// would be indistinguishable at the call site from a declined prompt --
	// which exits 0 having skipped one file.
	run := newPromptRun(policyAsk, "")

	answer, err := run.confirm.Ask("Are you sure?")

	if err == nil {
		t.Fatalf("Ask at end-of-input answered %v with no error, want a failure: EOF is not an implicit no", answer)
	}
	if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Unguarded {
		t.Errorf("Ask at end-of-input returned %v (regime %v, declared %v), want the unguarded regime",
			err, regime, declared)
	}
}

func TestThePromptEndsWithTheAnswerHintAndNoNewline(t *testing.T) {
	// appspec/07: "the prompt is written to stdout as the question text
	// followed by ` <Yes|No> `, and the answer is read from stdin". Both
	// spaces are part of it, and the line is deliberately left unterminated --
	// that is what puts the cursor where the answer is typed.
	run := newPromptRun(policyAsk, "yes\n")

	if _, err := run.confirm.Ask("Are you sure?"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if want := "Are you sure?" + answerSuffix; run.outText() != want {
		t.Errorf("the prompt was %q, want exactly %q", run.outText(), want)
	}
	if run.err.Len() != 0 {
		t.Errorf("the prompt wrote %q to stderr, want stdout only", run.err.String())
	}
	// The COLOUR is closed even though the line is not: appspec/07 promises
	// every coloured string is terminated with a reset, so what stays open is
	// the line and not the escape.
	if !strings.HasSuffix(run.out.String(), "\x1b[0m") {
		t.Errorf("the prompt was %q, want it to end in a reset: the line is left open, the colour is not", run.out.String())
	}
}

func TestAMultiLineQuestionPutsOnlyItsLastLineOnThePromptLine(t *testing.T) {
	// The folder-creation prompt is two lines and the whole-uninstall
	// confirmation is several. Each line but the last is an ordinary message
	// -- coloured, terminated, closed -- and only the last one carries the
	// answer hint and stays open.
	//
	// Written as one coloured string the middle lines would open a colour they
	// never close, which is the exact promise appspec/07 makes about every
	// coloured string.
	run := newPromptRun(policyAsk, "yes\n")

	if _, err := run.confirm.Ask("first line\nsecond line"); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if want := "first line\nsecond line" + answerSuffix; run.outText() != want {
		t.Errorf("the prompt was %q, want %q", run.outText(), want)
	}
	for _, line := range strings.Split(strings.TrimSuffix(run.out.String(), "\n"), "\n") {
		if !strings.HasSuffix(line, "\x1b[0m") {
			t.Errorf("the prompt line %q does not end in a reset", line)
		}
	}
}

func TestTheAnswersToTwoPromptsAreReadFromOneStream(t *testing.T) {
	// The failure this exists to prevent is invisible to a case that prompts
	// once. A bufio.Reader created per call reads a BLOCK from stdin, hands
	// back the first line and discards the rest, so `printf 'no\nyes\n' |
	// mackup backup` answers the first prompt and then sees end-of-input at
	// the second -- a run that fails partway through for a reason the user
	// cannot see, having been given exactly the answers it asked for.
	//
	// The confirmer is copied between the two calls, as it is at every call
	// site: what carries the unread remainder is the ui.IO, not the confirmer.
	run := newPromptRun(policyAsk, "no\nyes\n")

	first, err := run.confirm.Ask("first question?")
	if err != nil {
		t.Fatalf("the first Ask: %v", err)
	}
	copied := run.confirm
	second, err := copied.Ask("second question?")
	if err != nil {
		t.Fatalf("the second Ask, whose answer was on the same stream: %v", err)
	}
	if first != false || second != true {
		t.Errorf("the two answers were (%v, %v), want (false, true): both were read from one stdin", first, second)
	}
}

func TestTheDirectionRecordsAreTheTableAppspec06Gives(t *testing.T) {
	// appspec/06's direction table, as data. Every field of both records is
	// one column of it, and the reason to assert the table rather than each
	// behaviour separately is appspec/01 section 1: the two commands are one
	// procedure, so the whole of their difference is supposed to be visible
	// here in one place. A future column added to only one row is a divergence
	// this case sees.
	for _, test := range []struct {
		what string
		got  direction
		want direction
	}{
		{"backup", backupDirection, direction{
			fromHome:      true,
			verb:          "Backing up",
			driftPhrasing: "home and Mackup",
			location:      "the Mackup folder",
			mentionsForce: true,
			linkSkip:      true,
			gate:          gateEnsure,
			summaryNoun:   "Backup",
		}},
		{"restore", restoreDirection, direction{
			fromHome:      false,
			verb:          "Recovering",
			driftPhrasing: "Mackup and home",
			location:      "your home folder",
			mentionsForce: false,
			linkSkip:      false,
			gate:          gateRequire,
			summaryNoun:   "Restore",
		}},
	} {
		if test.got != test.want {
			t.Errorf("the %s direction is %+v, want %+v", test.what, test.got, test.want)
		}
	}
}

func TestTheDirectionOrientsTheTwoPathsAndTheForceHint(t *testing.T) {
	// The two things the record is asked for per file: which path is the
	// source, and whether the prompt mentions --force.
	//
	// The paths are the same pair in both directions, which is what makes this
	// a claim about orientation rather than about two path computations. A
	// procedure that derived its own source and destination would make the
	// direction record decorative.
	home, mackup := "/home/.vimrc", "/storage/Mackup/.vimrc"

	if src, dst := backupDirection.endpoints(home, mackup); src != home || dst != mackup {
		t.Errorf("backup reads %s and writes %s, want %s -> %s", src, dst, home, mackup)
	}
	if src, dst := restoreDirection.endpoints(home, mackup); src != mackup || dst != home {
		t.Errorf("restore reads %s and writes %s, want %s -> %s", src, dst, mackup, home)
	}
	if got := backupDirection.forceHint(); got != " (use --force to skip this prompt)" {
		t.Errorf("backup's force hint is %q, want the parenthetical appspec/07 gives it", got)
	}
	if got := restoreDirection.forceHint(); got != "" {
		t.Errorf("restore's force hint is %q, want none: the column says no", got)
	}
}

func TestTheTypeNounDescribesThePathItselfAndNotWhatItPointsAt(t *testing.T) {
	// appspec/07: "<type> above is one of file, folder, or link, describing
	// the existing path".
	//
	// The link row is the whole reason the noun exists. It is derived from an
	// Lstat, so a symlink is reported as a link rather than as whatever is on
	// the other end -- and the fixture points at a DIRECTORY, so an
	// implementation that followed would say "folder" and tell the user
	// something true about a path they were not being asked about.
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	folder := filepath.Join(root, "folder")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatalf("creating the folder: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(folder, link); err != nil {
		t.Fatalf("creating the link: %v", err)
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{file, "file"},
		{folder, "folder"},
		{link, "link"},
	} {
		info, err := os.Lstat(test.path)
		if err != nil {
			t.Fatalf("stat %s: %v", test.path, err)
		}
		if got := typeNoun(info); got != test.want {
			t.Errorf("typeNoun(%s) = %q, want %q", filepath.Base(test.path), got, test.want)
		}
	}
}

func TestStepOnesTestAdmitsFilesAndDirectoriesAndFollowsSymlinks(t *testing.T) {
	// appspec/06 step 1: "if the source path does not exist as a regular file
	// or directory, skip it silently". It follows symlinks, which is what
	// makes a live link to a real file copyable and a DANGLING one absent --
	// appspec/01 section 2 requires the dangling case to read as nothing to do
	// rather than as an error, and a home directory full of broken links is
	// ordinary residue.
	//
	// The FIFO row is the one that separates "exists" from "exists as a
	// regular file or directory". It is created only where the platform has
	// one; syncfs's own unix-tagged cases carry the copy half of that rule.
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	folder := filepath.Join(root, "folder")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatalf("creating the folder: %v", err)
	}
	live := filepath.Join(root, "live-link")
	if err := os.Symlink(file, live); err != nil {
		t.Fatalf("creating the live link: %v", err)
	}
	dangling := filepath.Join(root, "dangling-link")
	if err := os.Symlink(filepath.Join(root, "nowhere"), dangling); err != nil {
		t.Fatalf("creating the dangling link: %v", err)
	}

	for _, test := range []struct {
		what string
		path string
		want bool
	}{
		{"a regular file", file, true},
		{"a directory", folder, true},
		{"a symlink to a file", live, true},
		{"a dangling symlink", dangling, false},
		{"nothing at all", filepath.Join(root, "absent"), false},
	} {
		got, err := sourcePresent(test.path)
		if err != nil {
			t.Errorf("sourcePresent(%s) errored: %v -- every row here is a path the stat can read", test.what, err)
			continue
		}
		if got != test.want {
			t.Errorf("sourcePresent(%s) = %v, want %v", test.what, got, test.want)
		}
	}

	// The third answer, which the rows above cannot express: a stat that failed
	// for a reason other than absence is neither true nor false. A path UNDER a
	// regular file is the portable way to get one -- ENOTDIR, not ENOENT -- and
	// the conformance suite's unix cases carry the EACCES shape a real home
	// reaches this through. Returning false here is what let step 1 skip a
	// file it could not read, silently, and still exit 0.
	if _, err := sourcePresent(filepath.Join(file, "under-a-file")); err == nil {
		t.Errorf("sourcePresent(a path under a regular file) returned no error, want the stat failure reported rather than read as absence")
	}
}

func TestTheEnsureGateCreatesTheFolderOnlyOnYes(t *testing.T) {
	// Level 2 of appspec/01 section 4's lattice, both arms. On no the failure
	// is GUARDED and carries appspec/07's line verbatim: the user answered the
	// question the program asked, so this is a refusal rather than a fall-over.
	//
	// The declined arm asserts that nothing was created, which is the
	// substance of the refusal. A gate that created the folder and then
	// declined to use it would have changed the user's storage on an answer
	// that said not to.
	root := t.TempDir()

	created := filepath.Join(root, "created", "Mackup")
	if err := ensureMackupFolder(newPromptRun(policyAsk, "yes\n").confirm, created); err != nil {
		t.Fatalf("ensureMackupFolder on yes: %v", err)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Errorf("ensureMackupFolder on yes left no directory at %s (%v): it creates recursively", created, err)
	}

	declined := filepath.Join(root, "declined", "Mackup")
	err := ensureMackupFolder(newPromptRun(policyAsk, "no\n").confirm, declined)
	if err == nil {
		t.Fatalf("ensureMackupFolder on no returned nil, want the refusal appspec/07 names")
	}
	if want := "Error: Mackup can't do anything without a home =("; fault.Diagnostic(err) != want {
		t.Errorf("ensureMackupFolder on no said %q, want %q", fault.Diagnostic(err), want)
	}
	if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Guarded {
		t.Errorf("the declined-folder failure is regime %v (declared %v), want guarded", regime, declared)
	}
	if _, err := os.Lstat(declined); err == nil {
		t.Errorf("%s exists after the prompt was declined, want nothing created", declined)
	}
}

func TestTheEnsureGateAsksNothingWhenTheFolderIsAlreadyThere(t *testing.T) {
	// The ordinary case, and the reason it is worth a case of its own: stdin
	// is at end-of-input, so a gate that prompted unconditionally -- and then
	// took the answer it could not read as a failure -- would fail here rather
	// than in whichever command happened to run second.
	folder := t.TempDir()

	run := newPromptRun(policyAsk, "")
	if err := ensureMackupFolder(run.confirm, folder); err != nil {
		t.Fatalf("ensureMackupFolder over an existing folder: %v", err)
	}
	if run.out.Len() != 0 {
		t.Errorf("the gate printed %q over a folder that already exists, want nothing", run.out.String())
	}
}

func TestBothGatesAreSatisfiedOnlyByADirectory(t *testing.T) {
	// A regular file sitting where the Mackup folder belongs is not the
	// folder, in either direction. appspec/06 puts the synced files INSIDE
	// this path, so reporting it as present moves the failure to the first
	// copy -- where it arrives as an "Unable to copy" line for every file in
	// the run rather than as the one gate failure it is.
	//
	// The ensure gate then fails to create it, which is the second half of the
	// row: the answer was yes, the program could not do what it promised, and
	// appspec/07's guarded regime is where a condition this gate exists to
	// meet belongs.
	root := t.TempDir()
	file := filepath.Join(root, "not-a-folder")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	err := ensureMackupFolder(newPromptRun(policyAsk, "yes\n").confirm, file)
	if err == nil {
		t.Fatalf("the ensure gate accepted a regular file as the Mackup folder")
	}
	if !strings.HasPrefix(fault.Diagnostic(err), "Error: Unable to create the Mackup folder: ") {
		t.Errorf("the ensure gate said %q, want the create failure", fault.Diagnostic(err))
	}
	if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Guarded {
		t.Errorf("the create failure is regime %v (declared %v), want guarded", regime, declared)
	}

	if err := requireMackupFolder(file); err == nil {
		t.Errorf("the require gate accepted a regular file as the Mackup folder")
	}
}

func TestTheRequireGateNamesTheMissingFolderAndCreatesNothing(t *testing.T) {
	// Level 3: "require the Mackup folder to already exist -- if absent, fatal
	// error naming the missing Mackup folder (with a hint to back up or sync
	// first) and exit 1".
	//
	// The hint is asserted as a second line rather than as wording, because it
	// is what separates this from level 1's storage-root failure for a user
	// who has run neither command: the answer -- back up on the machine that
	// has the files, or let the sync client bring the folder down -- is not
	// derivable from the missing path alone.
	folder := filepath.Join(t.TempDir(), "Mackup")

	err := requireMackupFolder(folder)
	if err == nil {
		t.Fatalf("requireMackupFolder over an absent folder returned nil")
	}
	diagnostic := fault.Diagnostic(err)
	if !strings.HasPrefix(diagnostic, "Error: Unable to find the Mackup folder: "+folder) {
		t.Errorf("requireMackupFolder said %q, want it to open by naming the missing folder", diagnostic)
	}
	if len(strings.Split(diagnostic, "\n")) < 2 {
		t.Errorf("requireMackupFolder said %q, want the hint appspec/07 gives the row on a second line", diagnostic)
	}
	if regime, declared := fault.RegimeOf(err); !declared || regime != fault.Guarded {
		t.Errorf("the missing-folder failure is regime %v (declared %v), want guarded", regime, declared)
	}
	if _, err := os.Lstat(folder); err == nil {
		t.Errorf("%s exists after the require gate ran, want nothing created: that is the whole difference between the two levels", folder)
	}

	if err := requireMackupFolder(filepath.Dir(folder)); err != nil {
		t.Errorf("requireMackupFolder over an existing directory: %v, want nil", err)
	}
}
