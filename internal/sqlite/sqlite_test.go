package sqlite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures under testdata were produced by real SQLite -- the system
// sqlite3(1) and Python's own module -- and committed, rather than built at
// test time. That is the point of them: this package reads a file format it
// does not write, so a fixture this package generated would only prove it
// agrees with itself. Each one is here for a structure it contains and no
// other fixture does:
//
//	values.db    reserved bytes per page (Apple's SQLite reserves 12), an
//	             sqlite_autoindex b-tree beside the table's, and one row of
//	             every serial type the lookup can meet: text, integer, empty
//	             text, and NULL.
//	overflow.db  a value too long to fit on its page, so the payload spills
//	             into an overflow page and payloadOf's arithmetic is used.
//	many.db      enough rows to make the table's b-tree two levels deep, so
//	             the root is an interior page and walkPage recurses -- with
//	             the wanted row LAST, which is what puts the right-most-child
//	             pointer on the path.
//	utf16.db     a database whose text encoding is UTF-16, which this reader
//	             must refuse rather than decode.
const (
	values   = "testdata/values.db"
	overflow = "testdata/overflow.db"
	many     = "testdata/many.db"
	utf16    = "testdata/utf16.db"
)

// The query appspec/04 specifies, in the shape the google_drive engine issues
// it. Every case here reads through the same one, so a change to the reader
// is judged against the only call the program actually makes.
const (
	table       = "data"
	keyColumn   = "entry_key"
	key         = "local_sync_root_path"
	valueColumn = "data_value"
)

func TestLookupReadsTheValueAppspec04Asks(t *testing.T) {
	got, err := Lookup(values, table, keyColumn, key, valueColumn)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// A path with a space in it, because appspec/04's own example of a
	// Google Drive root is "Google Drive" and a reader that split the value
	// on whitespace anywhere would still pass on a one-word path.
	if want := "/Users/someone/Google Drive"; got != want {
		t.Errorf("Lookup = %q, want %q", got, want)
	}
}

func TestLookupReadsEveryColumnTypeTheTableHolds(t *testing.T) {
	// The row this package exists for is text, but the table it lives in is a
	// key/value store holding whatever the client put there. A decoder that
	// mishandled an integer or a NULL would walk off the record and misread
	// the rows AFTER it, so each type is read directly.
	for _, c := range []struct{ key, want string }{
		{"local_sync_root_path", "/Users/someone/Google Drive"},
		{"user_email", "someone@example.com"},
		{"highest_app_version", "77"},
		{"empty_value", ""},
		{"null_value", ""},
	} {
		got, err := Lookup(values, table, keyColumn, c.key, valueColumn)
		if err != nil {
			t.Errorf("Lookup(%q): %v", c.key, err)
			continue
		}
		if got != c.want {
			t.Errorf("Lookup(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestAnEmptyValueIsFoundRatherThanMissed(t *testing.T) {
	// The distinction appspec/04 draws: the query "yields no usable value"
	// covers both a missing row and an empty one, but only the caller may
	// collapse them. A reader that reported ErrNoSuchRow for an empty string
	// would make "the key is absent" and "the client wrote an empty root"
	// indistinguishable here, and the diagnostic the user sees is chosen from
	// exactly that.
	got, err := Lookup(values, table, keyColumn, "empty_value", valueColumn)
	if err != nil {
		t.Fatalf("Lookup on an empty value: %v, want it found", err)
	}
	if got != "" {
		t.Errorf("Lookup on an empty value = %q, want the empty string", got)
	}
}

func TestAMissingRowIsReportedAsSuch(t *testing.T) {
	_, err := Lookup(values, table, keyColumn, "no_such_key", valueColumn)
	if !errors.Is(err, ErrNoSuchRow) {
		t.Errorf("Lookup for an absent key = %v, want ErrNoSuchRow", err)
	}
}

func TestAValueTooLongForItsPageIsReadWhole(t *testing.T) {
	got, err := Lookup(overflow, table, keyColumn, key, valueColumn)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// 6001 bytes, well past a 4096-byte page, so the payload spills. The
	// length is asserted first because a truncated read is the failure this
	// case exists for and it produces a value that still looks like a path.
	if len(got) != 6001 {
		t.Fatalf("Lookup returned %d bytes, want 6001: the overflow chain was not followed to the end", len(got))
	}
	if !strings.HasPrefix(got, "/q") || strings.Trim(got[1:], "q") != "" {
		t.Errorf("Lookup returned %.40q..., want a slash followed by 6000 q's", got)
	}
}

func TestARowBeyondTheLastSeparatorOfAnInteriorPageIsReached(t *testing.T) {
	// many.db's table b-tree has an interior root, and the wanted row was
	// inserted last, so it sits in the right-most child -- the one an
	// interior page has no cell for. A walk that visited only the pages its
	// cells name misses it, and misses it silently: every other row still
	// reads correctly.
	got, err := Lookup(many, table, keyColumn, key, valueColumn)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if want := "/deep/in/the/btree"; got != want {
		t.Errorf("Lookup = %q, want %q", got, want)
	}
}

func TestEveryRowOfADeepTreeIsVisited(t *testing.T) {
	// The other direction of the case above: a walk that returned only the
	// right-most child would pass it. 200 filler rows were written across the
	// tree's leaves, so this fails if any leaf is skipped.
	for _, name := range []string{"filler-0000", "filler-0117", "filler-0199"} {
		got, err := Lookup(many, table, keyColumn, name, valueColumn)
		if err != nil {
			t.Errorf("Lookup(%q): %v", name, err)
			continue
		}
		if len(got) != 120 {
			t.Errorf("Lookup(%q) returned %d bytes, want 120", name, len(got))
		}
	}
}

func TestAUTF16DatabaseIsRefusedRatherThanDecoded(t *testing.T) {
	_, err := Lookup(utf16, table, keyColumn, key, valueColumn)
	if err == nil {
		t.Fatal("Lookup on a UTF-16 database succeeded; decoding its text as UTF-8 yields a path that looks plausible and is not")
	}
	if !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("Lookup on a UTF-16 database = %v, want a diagnostic naming the encoding it can read", err)
	}
}

func TestAFileThatIsNotADatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync_config.db")
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database at all\n", 100)), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	_, err := Lookup(path, table, keyColumn, key, valueColumn)
	if err == nil {
		t.Fatal("Lookup on a text file succeeded")
	}
	if errors.Is(err, ErrNoSuchRow) {
		t.Errorf("Lookup on a text file = %v, want a failure distinct from an absent row: appspec/04 shows the same message either way, but only one of them means the file was intact", err)
	}
}

func TestATruncatedDatabaseIsRefused(t *testing.T) {
	// A file that opens with a valid header and then stops. Every page read
	// past the first fails, which is the shape a partially-synced or
	// interrupted client file takes.
	whole, err := os.ReadFile(values)
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sync_config.db")
	if err := os.WriteFile(path, whole[:200], 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	if _, err := Lookup(path, table, keyColumn, key, valueColumn); err == nil {
		t.Error("Lookup on a truncated database succeeded")
	}
}

func TestAnAbsentFileIsReportedAsSuch(t *testing.T) {
	_, err := Lookup(filepath.Join(t.TempDir(), "absent.db"), table, keyColumn, key, valueColumn)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Lookup on an absent file = %v, want it to wrap os.ErrNotExist so a caller can tell it from a corrupt one", err)
	}
}

func TestAnAbsentTableAndAnAbsentColumnAreNamed(t *testing.T) {
	// Both are programming errors in the caller rather than conditions of the
	// user's machine, so the diagnostic has to name what was asked for; a
	// bare "no such row" would send the reader looking at the database.
	if _, err := Lookup(values, "no_such_table", keyColumn, key, valueColumn); err == nil ||
		!strings.Contains(err.Error(), "no_such_table") {
		t.Errorf("Lookup on an absent table = %v, want a diagnostic naming the table", err)
	}
	if _, err := Lookup(values, table, keyColumn, key, "no_such_column"); err == nil ||
		!strings.Contains(err.Error(), "no_such_column") {
		t.Errorf("Lookup on an absent column = %v, want a diagnostic naming the column", err)
	}
}

func TestTheAutoindexBesideTheTableIsNotWalked(t *testing.T) {
	// values.db declares PRIMARY KEY (entry_key), so SQLite built an
	// sqlite_autoindex_data_1 index b-tree and recorded it in sqlite_master
	// alongside the table. Its rows are in an index page this reader refuses
	// to walk, so a tableRoot that matched on name without checking the
	// "table" type would fail here rather than read the table.
	if _, err := Lookup(values, "sqlite_autoindex_data_1", keyColumn, key, valueColumn); err == nil {
		t.Error("Lookup on an index succeeded; this reader walks only rowid tables")
	}
}

func TestTheColumnListIsReadOutOfTheCreateStatement(t *testing.T) {
	// columnIndex is the reader's only contact with SQL, and getting a
	// position wrong shifts every column after it -- which reads as the wrong
	// VALUE, not as an error. The forms below are the ones a real schema uses.
	for _, c := range []struct {
		what   string
		schema string
		column string
		want   int
	}{
		{"a plain declaration", `CREATE TABLE data (entry_key TEXT, data_value TEXT)`, "data_value", 1},
		{"a trailing table constraint", `CREATE TABLE data (entry_key TEXT, data_value TEXT, PRIMARY KEY (entry_key))`, "data_value", 1},
		{"a leading table constraint", `CREATE TABLE data (CONSTRAINT c UNIQUE (a), a TEXT, b TEXT)`, "b", 1},
		{"a type with its own parentheses", `CREATE TABLE data (a VARCHAR(20, 3), b TEXT)`, "b", 1},
		{"a default holding a comma", `CREATE TABLE data (a TEXT DEFAULT 'x,y', b TEXT)`, "b", 1},
		{"quoted names", `CREATE TABLE data ("entry key" TEXT, ` + "`data_value`" + ` TEXT)`, "data_value", 1},
		{"a bracketed name", `CREATE TABLE data ([entry_key] TEXT, [data_value] TEXT)`, "data_value", 1},
		{"a doubled quote inside a name", `CREATE TABLE data ("a""b" TEXT, c TEXT)`, `a"b`, 0},
		{"a name differing only in case", `CREATE TABLE data (Entry_Key TEXT, Data_Value TEXT)`, "data_value", 1},
		{"newlines between columns", "CREATE TABLE data (\n\tentry_key TEXT,\n\tdata_value TEXT\n)", "data_value", 1},
	} {
		got, err := columnIndex(c.schema, c.column)
		if err != nil {
			t.Errorf("%s: columnIndex(%q) = %v", c.what, c.column, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: columnIndex(%q) = %d, want %d", c.what, c.column, got, c.want)
		}
	}
}

func TestAColumnListThatCannotBeReadIsReported(t *testing.T) {
	for _, c := range []struct{ what, schema string }{
		{"no column list at all", `CREATE TABLE data`},
		{"an unclosed column list", `CREATE TABLE data (a TEXT, b TEXT`},
		{"an unclosed quoted name", `CREATE TABLE data ("a TEXT, b TEXT)`},
	} {
		if _, err := columnIndex(c.schema, "b"); err == nil {
			t.Errorf("%s: columnIndex succeeded on %q", c.what, c.schema)
		}
	}
}

func TestTheVarintDecoderReadsEveryWidthTheFormatUses(t *testing.T) {
	// SQLite's varint is big-endian with a nine-byte case, which is neither
	// of the two encodings in encoding/binary. A decoder that reached for one
	// of those reads short values correctly and long ones as nonsense, so the
	// widths are pinned rather than sampled.
	for _, c := range []struct {
		bytes []byte
		want  int64
		size  int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x7f}, 127, 1},
		{[]byte{0x81, 0x00}, 128, 2},
		{[]byte{0xff, 0x7f}, 16383, 2},
		{[]byte{0x81, 0x80, 0x00}, 1 << 14, 3},
		{[]byte{0x81, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}, 1 << 57, 9},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, -1, 9},
	} {
		got, size := uvarint(c.bytes)
		if got != c.want || size != c.size {
			t.Errorf("uvarint(% x) = (%d, %d), want (%d, %d)", c.bytes, got, size, c.want, c.size)
		}
	}
	// A varint whose continuation bits run off the end of the slice.
	if _, size := uvarint([]byte{0x81, 0x80}); size != 0 {
		t.Errorf("uvarint on a truncated encoding reported %d bytes consumed, want 0", size)
	}
	if _, size := uvarint(nil); size != 0 {
		t.Errorf("uvarint on an empty slice reported %d bytes consumed, want 0", size)
	}
}

func TestASignedColumnKeepsItsSign(t *testing.T) {
	// The record format stores integers as two's-complement big-endian at six
	// different widths, and only the eight-byte one carries its sign for free.
	// Nothing in the google_drive query reads a negative number; the decoder
	// is shared, and one that dropped the sign extension would report
	// -1 as 255 in whatever later query did.
	for _, c := range []struct {
		serial int64
		bytes  []byte
		want   int64
	}{
		{1, []byte{0xff}, -1},
		{1, []byte{0x7f}, 127},
		{2, []byte{0xff, 0xfe}, -2},
		{3, []byte{0xff, 0xff, 0xfd}, -3},
		{4, []byte{0xff, 0xff, 0xff, 0xfc}, -4},
		{5, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xfb}, -5},
		{6, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfa}, -6},
		{8, nil, 0},
		{9, nil, 1},
	} {
		if got := (value{serial: c.serial, bytes: c.bytes}).integer(); got != c.want {
			t.Errorf("serial type %d over % x = %d, want %d", c.serial, c.bytes, got, c.want)
		}
	}
}

func TestAReservedSerialTypeIsRefused(t *testing.T) {
	// 10 and 11 are reserved for SQLite's internal use and have no defined
	// length, so a record carrying one cannot be split into columns at all.
	// Skipping it as zero-length would silently shift every column after it.
	for _, serial := range []int64{10, 11} {
		if _, err := serialSize(serial); err == nil {
			t.Errorf("serialSize(%d) succeeded, want a refusal", serial)
		}
	}
}

func TestARecordThatRunsPastItsEndIsRefused(t *testing.T) {
	// A header promising a column longer than the body that follows it. The
	// decoder must report it rather than slice out of range.
	for _, c := range []struct {
		what    string
		payload []byte
	}{
		{"a column longer than the body", []byte{0x02, 0x1b, 'a', 'b'}},
		{"a header longer than the record", []byte{0x40, 0x13}},
		{"an empty payload", nil},
	} {
		if _, err := decodeRecord(c.payload); err == nil {
			t.Errorf("%s: decodeRecord succeeded on % x", c.what, c.payload)
		}
	}
}
