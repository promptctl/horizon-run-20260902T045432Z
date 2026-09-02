// Package sqlite reads one value out of a SQLite database file.
//
// It exists for a single line of appspec/04-storage-engines.md: the
// google_drive engine resolves its storage root by reading a database the
// Google Drive desktop client maintains, "from table `data`, select
// `data_value` where `entry_key = 'local_sync_root_path'`". That file is a
// third party's, in a format this program does not choose, so reading it is
// part of the contract rather than an implementation detail -- appspec/04
// calls the equivalent Dropbox data shape "peer-observable, so this data shape
// is contract", and the same reasoning applies here.
//
// It is a READER, not a database. There is no SQL, no writing, no
// transactions, no locking, and no connection: Lookup opens the file, walks
// one table's b-tree, and closes it. That is the whole surface, and it is
// deliberate. The alternative was a general-purpose SQLite driver, which for
// a program whose only query is the constant above would mean carrying a
// translated copy of SQLite's C source -- megabytes of dependency, and a
// build that needs cgo or a code generator -- to answer a question the file
// format answers directly. This module has no dependencies and this package
// does not add one.
//
// # What it does not support
//
// Said out loud, because a reader that quietly returns the wrong answer is
// worse than one that refuses:
//
//   - Write-ahead logging. A database with un-checkpointed commits sitting in
//     a "-wal" sidecar reads here as its last checkpointed state. The value
//     this package is asked for is written once when the user sets up their
//     sync client and not touched again, so the checkpointed state is the
//     answer in every case the program actually meets; a client mid-write
//     could still hand back a stale root. Tracked as its own work rather than
//     hidden here.
//   - Text encodings other than UTF-8. UTF-16 databases are refused by name
//     rather than decoded, because a wrong guess about encoding produces a
//     path that looks plausible and is not.
//   - WITHOUT ROWID tables, whose rows live in an index b-tree. Refused by
//     name for the same reason.
//   - Anything about a table but its rows: no indexes are consulted, so a
//     lookup is a full scan of the named table. The database this exists for
//     holds a few dozen rows.
package sqlite

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// ErrNoSuchRow reports that the table was read successfully and held no row
// matching the key. It is separate from every other error here because the
// caller acts on it differently: appspec/04 treats "the query yields no usable
// value" and "the DB cannot be opened/queried" as the same failure for the
// user, but only the first one means the file was intact.
var ErrNoSuchRow = errors.New("sqlite: no row matched")

// headerSize is the fixed 100-byte database header at the start of page 1. It
// sits inside page 1, so page 1's b-tree header begins after it.
const headerSize = 100

// magic opens every SQLite database file, including the trailing NUL.
const magic = "SQLite format 3\x00"

// maxPageSize is the largest page a SQLite file may declare. The page-size
// field is 16 bits and the value 1 means 65536, which is why the largest
// legal size cannot be spelled directly.
const maxPageSize = 65536

// pageBudget bounds how many pages one Lookup may read.
//
// A b-tree whose pointers form a cycle -- corruption, or a file that is not
// the database it claims to be -- would otherwise walk forever. The bound is
// generous enough that no real client database approaches it and small enough
// that a malformed file fails in milliseconds. Overflow chains are counted
// against the same budget, since a self-referencing overflow page is the same
// hazard one level down.
const pageBudget = 1 << 20

// Lookup returns the text held in valueColumn by the first row of table whose
// keyColumn equals key.
//
// Rows are visited in the table's own b-tree order, which for a rowid table is
// ascending rowid. "First" therefore means lowest rowid among the matches; the
// databases this reads key the column it searches, so there is at most one.
//
// It reports ErrNoSuchRow when no row matches, and a NULL or empty value is
// returned as the empty string rather than as a miss -- telling those apart is
// the caller's business, and appspec/04 wants "present and non-empty".
func Lookup(path, table, keyColumn, key, valueColumn string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	db, err := open(file)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	value, err := db.lookup(table, keyColumn, key, valueColumn)
	if err != nil && !errors.Is(err, ErrNoSuchRow) {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return value, err
}

// database is one open database file: the reader, and the two page dimensions
// every offset in the format is computed from.
type database struct {
	r io.ReaderAt

	// pageSize is the on-disk stride between pages; usable is pageSize less
	// the per-page reserved region declared in the header. They differ on
	// real files -- Apple's system SQLite reserves 12 bytes per page -- and
	// the difference is not cosmetic: the overflow threshold and an overflow
	// page's payload capacity are both computed from the USABLE size, so a
	// reader that used the page size instead reads long values as garbage.
	pageSize int
	usable   int

	// budget is what remains of pageBudget for this lookup.
	budget int
}

// open reads the database header and reports a reader positioned to walk it.
func open(r io.ReaderAt) (*database, error) {
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, headerSize), header); err != nil {
		return nil, fmt.Errorf("reading the database header: %w", err)
	}
	if string(header[:len(magic)]) != magic {
		return nil, errors.New("not a SQLite database: the file does not open with the format's magic string")
	}

	pageSize := int(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = maxPageSize
	}
	if pageSize < 512 || pageSize > maxPageSize || pageSize&(pageSize-1) != 0 {
		return nil, fmt.Errorf("the header declares a page size of %d, which is not a power of two between 512 and %d", pageSize, maxPageSize)
	}

	reserved := int(header[20])
	usable := pageSize - reserved
	// 480 is the format's own floor on the usable size. Below it the overflow
	// arithmetic in payloadOf stops being meaningful, so this is checked here
	// rather than left to produce a negative length later.
	if usable < 480 {
		return nil, fmt.Errorf("the header reserves %d bytes of every %d-byte page, leaving %d usable, which is below the format's minimum of 480", reserved, pageSize, usable)
	}

	if encoding := binary.BigEndian.Uint32(header[56:60]); encoding != 1 {
		return nil, fmt.Errorf("the database text encoding is %d; this reader handles only UTF-8 (1), and decoding UTF-16 as UTF-8 would yield a plausible-looking wrong answer", encoding)
	}

	return &database{r: r, pageSize: pageSize, usable: usable, budget: pageBudget}, nil
}

// lookup finds the table's root page, then scans it for the wanted row.
func (d *database) lookup(table, keyColumn, key, valueColumn string) (string, error) {
	root, schema, err := d.tableRoot(table)
	if err != nil {
		return "", err
	}
	keyAt, err := columnIndex(schema, keyColumn)
	if err != nil {
		return "", fmt.Errorf("table %q: %w", table, err)
	}
	valueAt, err := columnIndex(schema, valueColumn)
	if err != nil {
		return "", fmt.Errorf("table %q: %w", table, err)
	}

	var found string
	matched := false
	err = d.walk(root, func(row []value) bool {
		if keyAt >= len(row) || row[keyAt].text() != key {
			return true
		}
		matched = true
		if valueAt < len(row) {
			found = row[valueAt].text()
		}
		return false
	})
	if err != nil {
		return "", err
	}
	if !matched {
		return "", ErrNoSuchRow
	}
	return found, nil
}

// sqliteMasterRoot is the page holding the schema table, fixed by the format.
const sqliteMasterRoot = 1

// Column positions in sqlite_master, which the format fixes rather than
// declaring: type, name, tbl_name, rootpage, sql.
const (
	masterType = iota
	masterName
	_
	masterRootPage
	masterSQL
)

// tableRoot returns the root page of the named table and the CREATE statement
// that declares its columns.
func (d *database) tableRoot(table string) (int64, string, error) {
	var root int64
	var schema string
	err := d.walk(sqliteMasterRoot, func(row []value) bool {
		if len(row) <= masterSQL || row[masterType].text() != "table" || row[masterName].text() != table {
			return true
		}
		root = row[masterRootPage].integer()
		schema = row[masterSQL].text()
		return false
	})
	if err != nil {
		return 0, "", err
	}
	if root == 0 {
		return 0, "", fmt.Errorf("no table named %q", table)
	}
	return root, schema, nil
}

// walk visits every row of the b-tree rooted at page, in b-tree order, until
// visit returns false or the tree is exhausted.
func (d *database) walk(page int64, visit func(row []value) bool) error {
	_, err := d.walkPage(page, visit)
	return err
}

// walkPage visits one page of a table b-tree and, for an interior page, its
// children. It reports whether the caller should keep going.
func (d *database) walkPage(number int64, visit func(row []value) bool) (bool, error) {
	page, err := d.readPage(number)
	if err != nil {
		return false, err
	}
	// Page 1 carries the database header ahead of its b-tree header; every
	// other page starts with the b-tree header. Cell pointers are offsets
	// from the start of the PAGE either way, so only the header position
	// moves.
	start := 0
	if number == sqliteMasterRoot {
		start = headerSize
	}
	if len(page) < start+8 {
		return false, fmt.Errorf("page %d is too short to hold a b-tree header", number)
	}

	kind := page[start]
	if kind != leafTable && kind != interiorTable {
		return false, fmt.Errorf("page %d is a %s, and this reader walks only rowid tables; a WITHOUT ROWID table stores its rows in an index b-tree", number, pageKindName(kind))
	}

	cells := int(binary.BigEndian.Uint16(page[start+3 : start+5]))
	pointers := start + 8
	if kind == interiorTable {
		pointers = start + 12
	}
	if pointers+2*cells > len(page) {
		return false, fmt.Errorf("page %d declares %d cells, more than its size can hold", number, cells)
	}

	for i := 0; i < cells; i++ {
		offset := int(binary.BigEndian.Uint16(page[pointers+2*i : pointers+2*i+2]))
		if offset < 0 || offset >= len(page) {
			return false, fmt.Errorf("page %d cell %d points outside the page", number, i)
		}
		if kind == interiorTable {
			if offset+4 > len(page) {
				return false, fmt.Errorf("page %d cell %d is truncated", number, i)
			}
			child := int64(binary.BigEndian.Uint32(page[offset : offset+4]))
			keepGoing, err := d.walkPage(child, visit)
			if err != nil || !keepGoing {
				return keepGoing, err
			}
			continue
		}
		row, err := d.readLeafCell(page[offset:], number, i)
		if err != nil {
			return false, err
		}
		if !visit(row) {
			return false, nil
		}
	}

	if kind == interiorTable {
		// The right-most child, which has no cell of its own: an interior
		// page's cells cover the keys BELOW each separator, and everything
		// above the last separator lives here. Omitting it drops the tail of
		// every table -- and the row this package exists to find sits at the
		// end of the one database whose rows are inserted in key order.
		rightMost := int64(binary.BigEndian.Uint32(page[start+8 : start+12]))
		return d.walkPage(rightMost, visit)
	}
	return true, nil
}

// Page type bytes, from the format's b-tree header.
const (
	interiorIndex = 0x02
	interiorTable = 0x05
	leafIndex     = 0x0a
	leafTable     = 0x0d
)

// pageKindName names a page type for a diagnostic.
func pageKindName(kind byte) string {
	switch kind {
	case interiorIndex:
		return "interior index page"
	case leafIndex:
		return "leaf index page"
	case 0:
		return "overflow or free page"
	default:
		return fmt.Sprintf("page of unknown type 0x%02x", kind)
	}
}

// readPage reads one whole page by its one-based number.
func (d *database) readPage(number int64) ([]byte, error) {
	if number < 1 {
		return nil, fmt.Errorf("page number %d is not a page", number)
	}
	if d.budget--; d.budget < 0 {
		return nil, fmt.Errorf("gave up after reading %d pages; the file's page pointers form a cycle or it is not the database it claims to be", pageBudget)
	}
	page := make([]byte, d.pageSize)
	if _, err := io.ReadFull(io.NewSectionReader(d.r, (number-1)*int64(d.pageSize), int64(d.pageSize)), page); err != nil {
		return nil, fmt.Errorf("reading page %d: %w", number, err)
	}
	return page, nil
}

// readLeafCell decodes one table-leaf cell into its column values, following
// the overflow chain when the payload does not fit on the page.
func (d *database) readLeafCell(cell []byte, page int64, index int) ([]value, error) {
	size, n := uvarint(cell)
	if n == 0 {
		return nil, fmt.Errorf("page %d cell %d: unreadable payload length", page, index)
	}
	cell = cell[n:]
	if _, n = uvarint(cell); n == 0 { // the rowid, which a table's columns never include
		return nil, fmt.Errorf("page %d cell %d: unreadable row id", page, index)
	}
	cell = cell[n:]

	payload, err := d.payloadOf(cell, int(size), page, index)
	if err != nil {
		return nil, err
	}
	return decodeRecord(payload)
}

// payloadOf returns a cell's whole payload, reading overflow pages when the
// local part is only its beginning.
//
// The arithmetic is the format's, not a choice: for a table leaf the payload
// stays local while it fits in usable-35 bytes, and otherwise the local part
// is sized so that an overflow page is never opened for a handful of bytes.
func (d *database) payloadOf(local []byte, size int, page int64, index int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("page %d cell %d: negative payload length", page, index)
	}
	maxLocal := d.usable - 35
	if size <= maxLocal {
		if size > len(local) {
			return nil, fmt.Errorf("page %d cell %d: payload runs past the end of the page", page, index)
		}
		return local[:size], nil
	}

	minLocal := ((d.usable-12)*32)/255 - 23
	inThisPage := minLocal + (size-minLocal)%(d.usable-4)
	if inThisPage > maxLocal {
		inThisPage = minLocal
	}
	if inThisPage < 0 || inThisPage+4 > len(local) {
		return nil, fmt.Errorf("page %d cell %d: the spilled payload's local part runs past the end of the page", page, index)
	}

	payload := make([]byte, 0, size)
	payload = append(payload, local[:inThisPage]...)
	next := int64(binary.BigEndian.Uint32(local[inThisPage : inThisPage+4]))
	for len(payload) < size {
		if next == 0 {
			return nil, fmt.Errorf("page %d cell %d: the overflow chain ended %d bytes short of the payload", page, index, size-len(payload))
		}
		overflow, err := d.readPage(next)
		if err != nil {
			return nil, err
		}
		next = int64(binary.BigEndian.Uint32(overflow[:4]))
		chunk := overflow[4:d.usable]
		if remaining := size - len(payload); remaining < len(chunk) {
			chunk = chunk[:remaining]
		}
		payload = append(payload, chunk...)
	}
	return payload, nil
}

// A value is one column of one row, as the record format stores it.
//
// Kept as raw bytes with their serial type rather than converted on the way
// out, so that a column this lookup does not care about costs nothing to skip
// and an unexpected type in one it does care about is reported rather than
// coerced.
type value struct {
	serial int64
	bytes  []byte
}

// text renders a value as the string SQLite would show. A NULL, and a number
// where a string was wanted, both read as their printed form rather than
// raising: the caller compares against a known key and returns a path, and a
// row whose key column holds a number simply does not match.
func (v value) text() string {
	switch {
	case v.serial == 0:
		return ""
	case v.serial >= 12:
		return string(v.bytes)
	case v.serial == 7:
		return fmt.Sprintf("%g", math.Float64frombits(uint64(v.integer())))
	default:
		return fmt.Sprintf("%d", v.integer())
	}
}

// integer renders a value as an integer, which for a non-integer serial type
// is zero.
func (v value) integer() int64 {
	switch v.serial {
	case 8:
		return 0
	case 9:
		return 1
	case 1, 2, 3, 4, 5, 6, 7:
		var n int64
		for _, b := range v.bytes {
			n = n<<8 | int64(b)
		}
		// Sign-extend: the format stores these as two's-complement big-endian
		// integers of 1, 2, 3, 4, 6 or 8 bytes, so the high bit of the first
		// byte is the sign for every width but the 8-byte one, where the
		// shifting above has already produced it.
		if bits := 8 * len(v.bytes); bits < 64 && len(v.bytes) > 0 && v.bytes[0]&0x80 != 0 {
			n -= 1 << bits
		}
		return n
	default:
		return 0
	}
}

// serialSize is the number of body bytes a serial type occupies.
func serialSize(serial int64) (int, error) {
	switch {
	case serial < 0:
		return 0, fmt.Errorf("negative serial type %d", serial)
	case serial == 0, serial == 8, serial == 9:
		return 0, nil
	case serial >= 1 && serial <= 4:
		return int(serial), nil
	case serial == 5:
		return 6, nil
	case serial == 6, serial == 7:
		return 8, nil
	case serial == 10 || serial == 11:
		return 0, fmt.Errorf("serial type %d is reserved for internal use", serial)
	default:
		return int((serial - 12) / 2), nil
	}
}

// decodeRecord splits a record payload into its column values.
func decodeRecord(payload []byte) ([]value, error) {
	headerSize, n := uvarint(payload)
	if n == 0 || headerSize < int64(n) || headerSize > int64(len(payload)) {
		return nil, errors.New("unreadable record header")
	}
	header := payload[n:headerSize]
	body := payload[headerSize:]

	var row []value
	for len(header) > 0 {
		serial, n := uvarint(header)
		if n == 0 {
			return nil, errors.New("unreadable serial type in a record header")
		}
		header = header[n:]
		size, err := serialSize(serial)
		if err != nil {
			return nil, err
		}
		if size > len(body) {
			return nil, fmt.Errorf("a column of %d bytes runs past the end of the record", size)
		}
		row = append(row, value{serial: serial, bytes: body[:size]})
		body = body[size:]
	}
	return row, nil
}

// uvarint decodes SQLite's big-endian variable-length integer: up to eight
// bytes carrying seven bits each with the high bit as a continuation flag,
// and a ninth byte contributing all eight of its bits. It returns the value
// and how many bytes it consumed, or a zero length if the encoding runs off
// the end of the slice.
//
// Not encoding/binary's Uvarint, which is a different encoding: that one is
// little-endian and has no nine-byte case.
func uvarint(b []byte) (int64, int) {
	var n uint64
	for i := 0; i < 8; i++ {
		if i >= len(b) {
			return 0, 0
		}
		if b[i] < 0x80 {
			return int64(n<<7 | uint64(b[i])), i + 1
		}
		n = n<<7 | uint64(b[i]&0x7f)
	}
	if len(b) < 9 {
		return 0, 0
	}
	return int64(n<<8 | uint64(b[8])), 9
}

// columnIndex returns the position of the named column in a CREATE TABLE
// statement's column list.
//
// The schema is read from sqlite_master, which stores the statement the table
// was created with verbatim, so this is the only place the reader has to
// understand SQL at all -- and it understands exactly one clause of it. Column
// names are compared case-insensitively, as SQLite compares identifiers.
func columnIndex(schema, column string) (int, error) {
	list, err := columnList(schema)
	if err != nil {
		return 0, err
	}
	for i, name := range list {
		if strings.EqualFold(name, column) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("has no column named %q; it has %s", column, strings.Join(list, ", "))
}

// columnList returns the declared column names of a CREATE TABLE statement,
// in order.
//
// Table constraints are skipped: a trailing "PRIMARY KEY (entry_key)" is a
// comma-separated item in the same list and is not a column, and counting it
// as one would shift every position after it. Only the constraint keywords
// that may open such an item are recognized, which is the whole of the
// format's grammar for it.
func columnList(schema string) ([]string, error) {
	body, err := parenthesized(schema)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, item := range splitTopLevel(body) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name := identifier(item)
		if name == "" {
			continue
		}
		switch strings.ToUpper(name) {
		case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN":
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, errors.New("declares no columns")
	}
	return names, nil
}

// parenthesized returns what lies between a statement's outermost matching
// parentheses.
func parenthesized(statement string) (string, error) {
	open := strings.IndexByte(statement, '(')
	if open < 0 {
		return "", errors.New("its CREATE statement has no column list")
	}
	depth := 0
	for i := open; i < len(statement); i++ {
		switch statement[i] {
		case '\'', '"', '`', '[':
			skip, err := skipQuoted(statement[i:])
			if err != nil {
				return "", err
			}
			i += skip - 1
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return statement[open+1 : i], nil
			}
		}
	}
	return "", errors.New("its CREATE statement has an unclosed column list")
}

// splitTopLevel splits a column list on the commas that are not inside
// parentheses or quotes.
func splitTopLevel(list string) []string {
	var items []string
	depth, start := 0, 0
	for i := 0; i < len(list); i++ {
		switch list[i] {
		case '\'', '"', '`', '[':
			skip, err := skipQuoted(list[i:])
			if err != nil {
				return append(items, list[start:])
			}
			i += skip - 1
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, list[start:i])
				start = i + 1
			}
		}
	}
	return append(items, list[start:])
}

// identifier reads the leading identifier of a column definition, unquoting it
// if it is quoted.
func identifier(item string) string {
	if item == "" {
		return ""
	}
	switch item[0] {
	case '\'', '"', '`', '[':
		skip, err := skipQuoted(item)
		if err != nil {
			return ""
		}
		return unquote(item[:skip])
	}
	end := strings.IndexAny(item, " \t\r\n(,")
	if end < 0 {
		return item
	}
	return item[:end]
}

// closingQuote is the character that ends each of SQLite's quoted forms.
var closingQuote = map[byte]byte{'\'': '\'', '"': '"', '`': '`', '[': ']'}

// skipQuoted returns the length of the quoted token at the start of text,
// including both delimiters. A doubled closing delimiter is an escaped one and
// does not end the token, which is how SQLite spells a quote inside a name.
func skipQuoted(text string) (int, error) {
	closing, quoted := closingQuote[text[0]]
	if !quoted {
		return 0, fmt.Errorf("%q does not open a quoted token", text[0])
	}
	for i := 1; i < len(text); i++ {
		if text[i] != closing {
			continue
		}
		// Brackets have no doubling rule: "[a]]" is not a name containing a
		// bracket, so only the other three forms look ahead.
		if text[0] != '[' && i+1 < len(text) && text[i+1] == closing {
			i++
			continue
		}
		return i + 1, nil
	}
	return 0, errors.New("a quoted identifier in the schema is never closed")
}

// unquote removes a quoted identifier's delimiters and undoubles any escaped
// delimiter inside it.
func unquote(token string) string {
	if len(token) < 2 {
		return token
	}
	closing := closingQuote[token[0]]
	inner := token[1 : len(token)-1]
	if token[0] == '[' {
		return inner
	}
	return strings.ReplaceAll(inner, string([]byte{closing, closing}), string(closing))
}
