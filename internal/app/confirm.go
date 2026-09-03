package app

import (
	"strings"

	"github.com/promptctl/macklebox/internal/cli"
	"github.com/promptctl/macklebox/internal/fault"
	"github.com/promptctl/macklebox/internal/ui"
)

// The one confirmation mechanism of appspec/01-architecture.md section 3.
//
// "There is exactly one confirmation mechanism; every yes/no in the program
// (every destructive-replace prompt, the whole-uninstall confirmation, the
// folder-creation prompt) routes through it. ... This decision is passed as
// data, not read from an ambient global."
//
// Both halves of that sentence are structural here. The three-valued decision
// is a value a caller is handed and cannot reach around -- there is no package
// variable holding it and no way to ask the flags again -- and every prompt in
// the program is a call to Ask. A second yes/no written inline anywhere else
// would be a second mechanism, and appspec/07's guarantee that --force
// pre-answers EVERY prompt would then hold only for the prompts someone
// remembered.

// A policy is the three-valued confirmation decision, fixed for the whole run.
type policy int

const (
	// policyAsk is the default: the question is put to stdin.
	policyAsk policy = iota
	// policyYes is --force: every prompt is pre-answered yes and none is
	// shown.
	policyYes
	// policyNo is --force-no: every prompt is pre-answered no and none is
	// shown.
	policyNo
)

// policyOf reads the decision off the parsed options.
//
// The two flags together are rejected at parse time, before config load
// (appspec/02, and runArgv does it), so this is never asked to resolve a
// contradiction. It is written so that if one ever reached here it would
// answer NO rather than yes: the conflict is refused upstream, and the safe
// half of a conflict this function cannot see is the one that changes nothing.
func policyOf(opts cli.Options) policy {
	switch {
	case opts.ForceNo:
		return policyNo
	case opts.Force:
		return policyYes
	default:
		return policyAsk
	}
}

// A confirmer puts a yes/no question to the user under the run's policy.
//
// It is a value, copied freely, and holds no answer history: appspec/07 gives
// the two force flags the same effect at every prompt and gives an interactive
// run no memory between them, so there is nothing here to carry from one
// question to the next. What IS carried between prompts -- the unread
// remainder of stdin -- belongs to the ui.IO, whose reader survives because
// the same IO is used for the whole run.
type confirmer struct {
	policy  policy
	streams *ui.IO
}

// answers is the vocabulary of appspec/07: "Accepted yes answers
// (case-insensitive): yes, y. Accepted no answers: no, n. Any other input
// re-asks the same question."
//
// A map rather than a switch so that the two sets are one table a reader can
// see the whole of. The empty string is deliberately absent: a bare newline is
// "any other input" and re-asks, which is what appspec/07 says and is also the
// only reading under which a stray keypress cannot delete a file.
var answers = map[string]bool{
	"yes": true,
	"y":   true,
	"no":  false,
	"n":   false,
}

// answerSuffix is what appspec/07 puts after the question text: "the prompt is
// written to stdout as the question text followed by ` <Yes|No> `". Both
// spaces are part of it.
const answerSuffix = " <Yes|No> "

// Ask puts question to the user and reports whether the answer was yes.
//
// Under either force flag it reports the pre-answer and prints NOTHING:
// appspec/07 says of --force that "no prompt is shown, the guarded action
// proceeds", and of --force-no the same with the action skipped. A run that
// printed the question and then answered it itself would put a question on
// stdout that nothing ever asked, and would break the property that makes
// --force scriptable.
//
// The question may be several lines -- the folder-creation prompt and the
// whole-uninstall confirmation both are. Each line is written as its own
// coloured message, because appspec/07 promises "every colored string is
// terminated with a reset" and a multi-line string coloured as one leaves its
// middle lines opening a colour they never close. The last line is the one
// that carries the answer suffix and stays unterminated, which is what puts
// the cursor where the answer is typed.
//
// The prompt is stdout. appspec/07 lists "the confirmation-prompt text" among
// the stdout messages outright, and ui.Progress is the level whose stream that
// is; the alternative reading -- that a question about replacing a file is an
// anomaly -- would make it bold yellow for no reason appspec/07 gives.
//
// End of input is a failure, not an answer. appspec/07: "If a prompt is
// reached with no force flag and stdin reaches end-of-input ... the program
// cannot obtain a valid answer and terminates with a nonzero exit (an
// unhandled end-of-input condition -- the unguarded regime)". Returning an
// error rather than false is what makes that distinguishable from a declined
// prompt, which exits 0 having skipped one file. Reading it as an implicit no
// would be the same shape as reading it as an implicit yes: the program
// answering for a user who is not there.
func (c confirmer) Ask(question string) (bool, error) {
	switch c.policy {
	case policyYes:
		return true, nil
	case policyNo:
		return false, nil
	}

	for {
		lines := strings.Split(question, "\n")
		for _, line := range lines[:len(lines)-1] {
			c.streams.Say(ui.Progress, line)
		}
		c.streams.Prompt(ui.Progress, lines[len(lines)-1]+answerSuffix)

		line, err := c.streams.ReadLine()
		if err != nil {
			// The read failure and end-of-input take the same arm and the
			// same regime. appspec/07 names only the second, but a stdin that
			// cannot be read is the same fact from the program's side -- no
			// valid answer can be obtained -- and inventing a second outcome
			// for it would be a contract this program made up.
			return false, fault.Unguardedf("end of input reached at a confirmation prompt: %s", err)
		}
		if answer, recognized := answers[strings.ToLower(strings.TrimSpace(line))]; recognized {
			return answer, nil
		}
		// Unrecognized input re-asks the SAME question, which is why the
		// whole prompt is inside the loop rather than above it.
	}
}
