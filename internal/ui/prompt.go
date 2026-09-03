package ui

import (
	"bufio"
	"io"
)

// The input half of the boundary this package owns.
//
// appspec/07-output-safety-lifecycle.md "The confirmation / safety model"
// specifies a prompt as two halves of one exchange: "the prompt is written to
// stdout as the question text followed by ` <Yes|No> `, and the answer is read
// from stdin". Both halves live here for the reason the output half does --
// the stream is contract, and stdin is the only stream the program reads --
// while the POLICY that decides whether to ask at all, what counts as yes, and
// what to do at end-of-input is internal/app's, because appspec/01 section 3
// makes it one decision threaded to every prompt in the program rather than a
// property of the streams.

// Prompt writes a question at its level, on the stream that level names, and
// leaves the cursor on the line so the answer is typed after it.
//
// The one writer here that does not end its message with a newline, and the
// exception Say's doc points at. What it leaves unterminated is the LINE, not
// the colour: Colorize still closes with a reset, so the answer the user types
// is not swallowed into the question's colour. A prompt is the only message in
// the program the user replies to in place, which is why this exists once and
// why nothing else should use it -- a second unterminated writer is how a
// progress line comes to share a line with whatever printed next.
func (s *IO) Prompt(level Level, text string) {
	s.write(s.streamFor(level), "%s", Colorize(level, text))
}

// ReadLine reads one line of input, without its newline.
//
// The reader is created once and kept, which is the whole reason this is a
// method rather than a function over s.In. appspec/07 makes any other input a
// re-ask ("the loop repeats until a recognized answer is given"), and a run can
// reach many prompts, so the answers arrive as a stream: a fresh bufio.Reader
// per call would read a block, hand back the first line and discard the rest,
// so `printf 'no\nyes\n' | mackup backup` would answer the first prompt and
// then see end-of-input at the second. That failure is invisible to a case
// that only ever prompts once.
//
// A final line with no trailing newline is an answer. bufio reports it as data
// plus io.EOF, and treating that pair as end-of-input would make `printf yes`
// -- what a shell here-string and most test harnesses produce -- an unanswered
// prompt.
func (s *IO) ReadLine() (string, error) {
	if s.in == nil {
		s.in = bufio.NewReader(s.In)
	}
	line, err := s.in.ReadString('\n')
	if err != nil {
		if err == io.EOF && line != "" {
			return trimNewline(line), nil
		}
		return "", err
	}
	return trimNewline(line), nil
}

// trimNewline removes one trailing line terminator, in either spelling.
//
// The carriage return is stripped too, so an answer typed on a terminal whose
// line discipline sends CRLF -- and, more to the point, an answer piped in
// from a file with CRLF line endings -- is "yes" rather than "yes\r", which
// appspec/07's vocabulary would not recognize and which would therefore re-ask
// forever against a finite input.
func trimNewline(line string) string {
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	return line
}
