package catalog

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// appendixPath locates the specification appendix from the package directory,
// which is where `go test` runs. The appendix is the source of truth for the
// key set (appspec/05 "Reference-build application set"), so these cases read
// it rather than restating 614 keys a second time -- a copy would agree with
// itself forever while the two drifted.
var appendixPath = filepath.Join("..", "..", "appspec", "appendix-application-names.md")

// appendixKeyBlock matches the fenced block holding the keys, and totalLine the
// count the appendix states in prose. Both are read, and cross-checked against
// each other, because they are two independent claims in one file: an edit that
// adds a key without touching the total leaves the appendix disagreeing with
// itself, and this package would then ship whichever half it happened to read.
var (
	appendixKeyBlock = regexp.MustCompile("(?s)```\n(.*?)```")
	totalLine        = regexp.MustCompile(`(?m)^Total: (\d+) applications\.$`)
)

// appendix reads the specification appendix and returns its keys, in the order
// it lists them, together with the total it states.
func appendix(t *testing.T) (keys []string, total int) {
	t.Helper()
	body, err := os.ReadFile(appendixPath)
	if err != nil {
		t.Fatalf("reading the key appendix: %v", err)
	}
	block := appendixKeyBlock.FindSubmatch(body)
	if block == nil {
		t.Fatalf("%s holds no fenced key block", appendixPath)
	}
	for _, line := range strings.Split(string(block[1]), "\n") {
		if key := strings.TrimSpace(line); key != "" {
			keys = append(keys, key)
		}
	}
	stated := totalLine.FindSubmatch(body)
	if stated == nil {
		t.Fatalf("%s states no total", appendixPath)
	}
	total, err = strconv.Atoi(string(stated[1]))
	if err != nil {
		t.Fatalf("the stated total is not a number: %v", err)
	}
	return keys, total
}

// definition is one parsed definition file: the two authoring sources of
// appspec/05 are kept apart here, unlike in the database, where they become one
// file set. These cases check what was written, so they need to see which
// section a path was written in.
type definition struct {
	name string
	// configurationFiles are the [configuration_files] entries, home-relative.
	configurationFiles []string
	// xdgConfigurationFiles are the [xdg_configuration_files] entries, relative
	// to the XDG config directory.
	xdgConfigurationFiles []string
}

// parse reads one definition file strictly, reporting anything it does not
// recognize.
//
// This is a second, independent reader, and that is the point of it. The
// program's parser is macklebox-resolvers-5iw.3's; checking this package's data
// with that parser would only establish that the data and the parser agree, so
// a shape both got wrong -- a section name misspelled in every file, say --
// would read back clean. It is also stricter than the program's parser is
// required to be: appspec/05 fixes what a definition MAY contain, while these
// cases fix what this project's own definitions DO contain, and nothing here
// constrains what the program must accept from a user's directory.
func parse(t *testing.T, name, body string) definition {
	t.Helper()
	var def definition
	section := ""
	seen := map[string]bool{}
	for number, line := range strings.Split(body, "\n") {
		where := name + ":" + strconv.Itoa(number+1)
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line != strings.TrimSpace(line) {
			t.Errorf("%s: %q carries leading or trailing whitespace, which would make the path it names differ from the file it is meant to select", where, line)
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				t.Errorf("%s: %q opens a section and does not close it", where, line)
				continue
			}
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			switch section {
			case "application", "configuration_files", "xdg_configuration_files":
			default:
				t.Errorf("%s: section [%s] is not one appspec/05 recognizes, so everything under it is silently ignored by the database", where, section)
			}
			if seen[section] {
				t.Errorf("%s: section [%s] appears twice", where, section)
			}
			seen[section] = true
			continue
		}
		switch section {
		case "":
			t.Errorf("%s: %q precedes any section header", where, line)
		case "application":
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				t.Errorf("%s: %q is not a key = value line", where, line)
				continue
			}
			if strings.TrimSpace(key) != "name" {
				t.Errorf("%s: [application] holds key %q; appspec/05 recognizes only name", where, strings.TrimSpace(key))
				continue
			}
			def.name = strings.TrimSpace(value)
		case "configuration_files":
			def.configurationFiles = append(def.configurationFiles, line)
		case "xdg_configuration_files":
			def.xdgConfigurationFiles = append(def.xdgConfigurationFiles, line)
		}
	}
	return def
}

func TestTheShippedKeysAreExactlyTheAppendix(t *testing.T) {
	// The done-claim of macklebox-resolvers-5iw.1, read at the only level this
	// package can be read at: `list` prints the keys the database assembled,
	// and with no user directories in play those are exactly the keys shipped
	// here. Surfacing them through the command is macklebox-resolvers-5iw.4.
	keys, total := appendix(t)
	if len(keys) != total {
		t.Fatalf("the appendix lists %d keys and states a total of %d; the two halves of the same file disagree", len(keys), total)
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("the appendix's keys are not sorted ascending, which appspec/05 says is the order `list` prints them in")
	}

	entries, err := fs.ReadDir(Definitions(), ".")
	if err != nil {
		t.Fatalf("reading the shipped definition directory: %v", err)
	}
	var shipped []string
	for _, entry := range entries {
		shipped = append(shipped, strings.TrimSuffix(entry.Name(), extension))
	}

	// Reported as two difference lists rather than a count, because a count
	// tells you the set is wrong and nothing about which key to add.
	want := map[string]bool{}
	for _, key := range keys {
		want[key] = true
	}
	have := map[string]bool{}
	for _, key := range shipped {
		have[key] = true
	}
	for _, key := range keys {
		if !have[key] {
			t.Errorf("the appendix lists %q and no definition ships for it", key)
		}
	}
	for _, key := range shipped {
		if !want[key] {
			t.Errorf("a definition ships for %q, which the appendix does not list", key)
		}
	}
	if len(shipped) != total {
		t.Errorf("%d definitions ship; the appendix's count trailer would read %d", len(shipped), total)
	}
}

func TestEveryShippedFileIsADefinition(t *testing.T) {
	// The database decides precedence by filename (appspec/05 "Discovery"), so
	// a file here that is not "<key>.cfg" is not merely inert: it is a file a
	// user could shadow with a name they cannot guess, or one that shadows
	// nothing while looking like it should.
	entries, err := fs.ReadDir(Definitions(), ".")
	if err != nil {
		t.Fatalf("reading the shipped definition directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the shipped definition directory is empty, so the embed pattern matched nothing")
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			t.Errorf("%s is a directory; appspec/05 reads the *.cfg files directly in the definition directory and nothing below it", name)
			continue
		}
		if !strings.HasSuffix(name, extension) {
			t.Errorf("%s does not end in %s, so the database ignores it -- appspec/05: \"Only files ending in .cfg are considered\"", name, extension)
			continue
		}
		key := strings.TrimSuffix(name, extension)
		if key == "" || key != path.Clean(key) || strings.ContainsAny(key, "/ ") {
			t.Errorf("%s yields the application key %q, which is not a name a user can type", name, key)
		}
	}
}

func TestEveryDefinitionIsWellFormedData(t *testing.T) {
	entries, err := fs.ReadDir(Definitions(), ".")
	if err != nil {
		t.Fatalf("reading the shipped definition directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		body, err := fs.ReadFile(Definitions(), name)
		if err != nil {
			t.Errorf("reading %s: %v", name, err)
			continue
		}
		def := parse(t, name, string(body))

		// appspec/05 makes [application] name required, and the reference
		// build has it on every definition. A definition without one shows a
		// blank display name, which `show` prints as a fact about the app.
		if def.name == "" {
			t.Errorf("%s has no [application] name, which `show` prints as the display name", name)
		}
		if def.name != strings.TrimSpace(def.name) {
			t.Errorf("%s has a display name padded with whitespace: %q", name, def.name)
		}

		// Duplicates are looked for within a section, not across the two.
		// The same spelling in both means two different files -- appspec/05
		// joins the XDG base to one and not the other -- so a shared set would
		// report a definition listing "git/config" in each as repeating
		// itself, which it is not.
		for _, group := range [][]string{def.configurationFiles, def.xdgConfigurationFiles} {
			seen := map[string]bool{}
			for _, file := range group {
				// The rejection appspec/05 calls load-bearing for the sync
				// engine's safety: an absolute path in either section aborts
				// assembly, so shipping one would make every command fatal
				// out of the box. Checked on the data, so the failure is a
				// named file here rather than a dead program.
				if strings.HasPrefix(file, "/") {
					t.Errorf("%s lists the absolute path %q; appspec/05 rejects it at assembly and every command would fail", name, file)
				}
				// Not an appspec/05 rejection, and deliberately not claimed as
				// one: the spec rejects a leading "/" and says nothing about
				// "..". It is a claim about this project's own data. The
				// home-relativity guarantee exists so that joining HOME to any
				// file yields a path under home, and "../.ssh" satisfies the
				// letter of the rejection while defeating that.
				if file == ".." || strings.HasPrefix(file, "../") || strings.Contains(file, "/../") || strings.HasSuffix(file, "/..") {
					t.Errorf("%s lists %q, which climbs out of the directory it is relative to", name, file)
				}
				if strings.Contains(file, "=") {
					t.Errorf("%s lists %q; entries in the file-set sections are bare paths with no \"=\"", name, file)
				}
				if file != path.Clean(file) {
					t.Errorf("%s lists %q, which is not the path it names in its plainest form (%q)", name, file, path.Clean(file))
				}
				if seen[file] {
					t.Errorf("%s lists %q twice; the file set is a union, so the duplicate says nothing and hides an intended second path", name, file)
				}
				seen[file] = true
			}
		}
	}
}

func TestTheMackupSelfDefinitionCoversItsOwnConfiguration(t *testing.T) {
	// Whole-Mackup mode (appspec/06 "Whole-Mackup mode") syncs the program's
	// own configuration through the ordinary application machinery, so the
	// mackup key has to exist and has to name the config file and the custom
	// definition directory. macklebox-link-sync-83q.1 is blocked on this.
	body, err := fs.ReadFile(Definitions(), "mackup"+extension)
	if err != nil {
		t.Fatalf("the mackup self-definition does not ship: %v", err)
	}
	def := parse(t, "mackup"+extension, string(body))
	if def.name == "" {
		t.Error("the mackup self-definition has no display name")
	}
	for _, want := range []string{".mackup.cfg", ".mackup"} {
		found := false
		for _, file := range def.configurationFiles {
			if file == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the mackup self-definition does not list %s; `show mackup` would omit it and whole-Mackup mode would not sync it", want)
		}
	}
}
