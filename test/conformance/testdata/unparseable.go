// This file is deliberately not valid Go, and it is not a mistake.
//
// It pins the two testdata exclusions that TestEveryDocCommentNamesWhatItDocuments
// and the check target's gofmt step both need. `go build`, `go vet` and
// `go list` ignore testdata directories entirely, so nothing here reaches the
// build; a doc-comment walk and a `gofmt -l` over ./test do NOT ignore it
// unless they are told to, and both used to fail the whole gate on a file like
// this one with a message about doc comments or formatting.
//
// Remove either exclusion and `make check` goes red here. That is the point:
// the alternative is two mechanisms nothing exercises.
this file is not Go {{{
