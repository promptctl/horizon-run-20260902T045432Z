package drift

import (
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/promptctl/macklebox/internal/ui"
)

// compareTrees is appspec/06's directory arm: "compared recursively by content
// (not shallow stat). Identical only if every file matches byte-for-byte and
// neither side has extra entries."
//
// # Entries inside a tree are followed, unlike the pair at the top
//
// The pair Compare was given is classified with os.Lstat, because appspec/06
// makes "either path is a symlink" a question about the path. Entries found
// INSIDE the two directories are classified with os.Stat instead, and the
// reversal is deliberate rather than an inconsistency to tidy up.
//
// appspec/06 says nothing directly about a symlink nested in a synced
// directory; what it does say is that the directories are compared "by
// content", and that an identical pair is "skipped with no prompt -- this is
// the backup/restore idempotency fixed point". internal/syncfs.Copy follows
// symlinks when it copies a tree, so the copy it writes has a real file
// wherever the source had a link. If this walk did not follow, that pair would
// be reported as changed on every run: the user would be prompted, would agree,
// the copy would reproduce exactly the state that was just called different,
// and the next run would prompt again. Following is what makes this
// comparison the fixed point of the copy it guards, which is the property
// appspec/06 states outright.
//
// The top-level rule does not have that problem to solve, because appspec/06
// states it and because backup's link-skip (its step 2) already takes the case
// that would otherwise repeat.
//
// # The three lists
//
// "Otherwise the detail lists, sorted: changed files ('changed: <name>'), files
// present only in source ('only in source: <name>'), and files present only in
// destination ('only in target: <name>')." Sorted within each list and in that
// order between them, which is the order the sentence gives.
//
// A directory present on only one side is named once and not descended into,
// the way diff(1) reports a directory only one side has. Listing its whole
// contents would bury the fact that the directory itself is missing under a
// line for every file in it.
func compareTrees(source, target string) Result {
	found := &treeDiff{}
	if !found.walk(source, target, "") {
		// The root of one side could not be read. There is nothing to list and
		// nothing that would be true to say about which files differ, so this
		// is the differing-with-no-detail answer, as for a symlink.
		return differs()
	}
	if found.empty() {
		return identical()
	}
	return Result{Detail: found.lines()}
}

// A treeDiff accumulates the three lists appspec/06 asks for.
type treeDiff struct {
	changed    []string
	onlySource []string
	onlyTarget []string
}

func (d *treeDiff) empty() bool {
	return len(d.changed) == 0 && len(d.onlySource) == 0 && len(d.onlyTarget) == 0
}

// lines renders the three lists as detail, each sorted.
//
// Sorted here rather than relying on the walk's own order, which is not the
// same thing: the walk visits each directory's entries in order and descends
// immediately, so "a/x" is emitted before "a-b" while sorting the paths puts
// "a-b" first. appspec/06 asks for sorted lists, and only a sort gives them.
//
// The levels are the closest of appspec/07's four diff decorations, and the
// mapping is worth stating because the specification assigns levels to diff
// output and this detail is not a diff. A name present only in the source is
// what the copy is about to add, so it takes the added level; a name present
// only in the target is the one the source does not have, which is the removed
// level's role in the diff above; and a changed file is neither, so it takes
// the file-header level, which is the decoration that names a file rather than
// marking a change within one.
func (d *treeDiff) lines() []Line {
	var lines []Line
	for _, group := range []struct {
		level  ui.Level
		prefix string
		names  []string
	}{
		{ui.DiffFileHeader, "changed: ", d.changed},
		{ui.DiffAdded, "only in source: ", d.onlySource},
		{ui.DiffRemoved, "only in target: ", d.onlyTarget},
	} {
		sort.Strings(group.names)
		for _, name := range group.names {
			lines = append(lines, Line{Level: group.level, Text: group.prefix + name})
		}
	}
	return lines
}

// walk compares one directory of each tree, reporting whether both could be
// read.
//
// It returns false only for the directory it was called on. A subdirectory that
// cannot be read is recorded as changed and the walk carries on: an unreadable
// directory somewhere inside a config tree is a real difference the user should
// be told about, and it is not a reason to abandon everything else the
// comparison found.
//
// A directory symlink that leads back into the tree it is inside makes this
// recurse on a lengthening path, and the recursion is ended by the operating
// system rather than by a check here: each step adds a link to resolve, so the
// kernel's limit on symlink traversals turns the read into an error within a
// few dozen levels and that subtree is recorded as changed. internal/syncfs's
// copy of the same tree meets the same limit the same way, which is the
// behaviour to match -- a private cycle detector here would report agreement
// about a tree the copy cannot write.
func (d *treeDiff) walk(source, target, prefix string) bool {
	sourceEntries, sourceErr := entriesOf(source)
	targetEntries, targetErr := entriesOf(target)
	if sourceErr != nil || targetErr != nil {
		return false
	}

	for _, name := range union(sourceEntries, targetEntries) {
		// path.Join and not filepath.Join: this name is printed, and a name
		// the user reads should be spelled one way whatever the host's
		// separator is. The paths the walk actually opens are built with
		// filepath.Join just below.
		relative := path.Join(prefix, name)
		_, inSource := sourceEntries[name]
		_, inTarget := targetEntries[name]
		switch {
		case !inTarget:
			d.onlySource = append(d.onlySource, relative)
		case !inSource:
			d.onlyTarget = append(d.onlyTarget, relative)
		default:
			d.compare(filepath.Join(source, name), filepath.Join(target, name), relative)
		}
	}
	return true
}

// compare classifies one entry present on both sides.
func (d *treeDiff) compare(source, target, relative string) {
	sourceInfo, sourceErr := os.Stat(source)
	targetInfo, targetErr := os.Stat(target)
	switch {
	case sourceErr != nil || targetErr != nil:
		// A dangling symlink, or an entry this process cannot reach. Reported
		// as changed rather than skipped: the two sides are not established to
		// match, and appspec/06 makes "identical" the claim that needs
		// evidence.
		d.changed = append(d.changed, relative)
	case sourceInfo.IsDir() && targetInfo.IsDir():
		if !d.walk(source, target, relative) {
			d.changed = append(d.changed, relative)
		}
	case sourceInfo.Mode().IsRegular() && targetInfo.Mode().IsRegular():
		if !sameContents(source, target) {
			d.changed = append(d.changed, relative)
		}
	default:
		// A directory on one side and a file on the other, or something that
		// is neither. The type mismatch appspec/06 gives its own one-line
		// detail for at the top level is, inside a tree, one of the changed
		// names -- that list is the only vocabulary this arm has.
		d.changed = append(d.changed, relative)
	}
}

// entriesOf reads one directory into a set of names.
func entriesOf(dir string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = struct{}{}
	}
	return names, nil
}

// union returns every name in either directory, sorted, so that the walk visits
// a name once however many sides it is on.
func union(a, b map[string]struct{}) []string {
	names := make([]string, 0, len(a)+len(b))
	for name := range a {
		names = append(names, name)
	}
	for name := range b {
		if _, both := a[name]; !both {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
