package ticks

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maxmoneycash/hlp/internal/domain"
)

// ErrCapacity leaves incoming trades in the recorder's bounded memory window.
var ErrCapacity = errors.New("ticks: local history storage limit reached; recent trades remain in memory")

// Coin leases protect individual files; this lock also serializes admission
// against the shared budget when different coins have different writers.
func budgetLock(root string) (func(), error) {
	deadline := time.Now().Add(time.Second)
	for {
		if unlock, ok := tryLock(filepath.Join(root, "budget.lock")); ok {
			return unlock, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("ticks: history budget lock is busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// On disk: <root>/<coin>/<YYYYMMDD>.trd, one append-only file per UTC day
// of trade time. A file is an 8-byte header and fixed 33-byte records,
// little endian: time ms (int64), tid (uint64), px (float64), sz (float64),
// flags (bit 0 seller was the aggressor, bit 1 gap before this trade).
// A torn tail (a crash mid-write) is ignored on read and cut by the next
// writer. Only the process holding <coin>/writer.lock appends.

const (
	magic   = "HLPTRD1\n"
	recSize = 33
	ext     = ".trd"

	flagSell = 1 << 0
	flagGap  = 1 << 1
)

// Defaults for Options.
const (
	DefaultRetentionDays = 7
	DefaultMaxMB         = 512
)

// Options bound the store.
type Options struct {
	// RetentionDays keeps this many whole UTC days before today (default 7).
	RetentionDays int
	// MaxBytes caps the whole store (every coin) by deleting the oldest
	// days first; today's files are never deleted. If they fill the budget,
	// new writes are refused (default 512 MB).
	MaxBytes int64
	// Now is the clock (tests).
	Now func() time.Time
}

func (o Options) norm() Options {
	if o.RetentionDays <= 0 {
		o.RetentionDays = DefaultRetentionDays
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxMB << 20
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Store is one coin's trade history.
type Store struct {
	root, dir, coin string
	o               Options

	mu     sync.Mutex
	unlock func()
	writer bool
	f      *os.File
	fDay   int64
	bounds map[int64]tradeBounds
}

// Open opens (creating) coin's store under root. It is read-only until
// TryLease succeeds.
func Open(root, coin string, o Options) (*Store, error) {
	if root == "" {
		return nil, errors.New("ticks: no store directory")
	}
	dir := filepath.Join(root, CoinDir(coin))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ticks: %w", err)
	}
	return &Store{root: root, dir: dir, coin: coin, o: o.norm()}, nil
}

// CoinDir maps a coin to a safe directory name (xyz:SP500 -> xyz%3ASP500).
func CoinDir(coin string) string {
	var b strings.Builder
	for _, r := range coin {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	s := b.String()
	if s == "." || s == ".." {
		s = strings.ReplaceAll(s, ".", "%2E")
	}
	return s
}

// Dir is the coin's directory.
func (s *Store) Dir() string { return s.dir }

// TryLease takes the writer lease without waiting. False means another
// process is recording this coin: this one reads only.
func (s *Store) TryLease() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer {
		return true
	}
	unlock, ok := tryLock(filepath.Join(s.dir, "writer.lock"))
	if !ok {
		return false
	}
	s.unlock, s.writer = unlock, true
	if release, err := budgetLock(s.root); err == nil {
		_ = s.pruneLocked(0, nil)
		release()
	}
	return true
}

// Writer reports whether this process holds the lease.
func (s *Store) Writer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writer
}

// Close releases the file and the lease.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.f != nil {
		err = s.f.Close()
		s.f = nil
	}
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
	s.writer = false
	return err
}

func dayName(day int64) string {
	return time.UnixMilli(day*DayMs).UTC().Format("20060102") + ext
}

func parseDay(name string) (int64, bool) {
	if !strings.HasSuffix(name, ext) {
		return 0, false
	}
	t, err := time.Parse("20060102", strings.TrimSuffix(name, ext))
	if err != nil {
		return 0, false
	}
	return DayOf(t.UnixMilli()), true
}

func encode(buf []byte, r Rec) []byte {
	var b [recSize]byte
	binary.LittleEndian.PutUint64(b[0:], uint64(r.Time))
	binary.LittleEndian.PutUint64(b[8:], r.TID)
	binary.LittleEndian.PutUint64(b[16:], math.Float64bits(r.Px))
	binary.LittleEndian.PutUint64(b[24:], math.Float64bits(r.Sz))
	var fl byte
	if r.Side == domain.Sell {
		fl |= flagSell
	}
	if r.Gap {
		fl |= flagGap
	}
	b[32] = fl
	return append(buf, b[:]...)
}

func decode(b []byte) Rec {
	r := Rec{
		Time: int64(binary.LittleEndian.Uint64(b[0:])),
		TID:  binary.LittleEndian.Uint64(b[8:]),
		Px:   math.Float64frombits(binary.LittleEndian.Uint64(b[16:])),
		Sz:   math.Float64frombits(binary.LittleEndian.Uint64(b[24:])),
		Side: domain.Buy,
		Gap:  b[32]&flagGap != 0,
	}
	if b[32]&flagSell != 0 {
		r.Side = domain.Sell
	}
	return r
}

// Append writes trades (any days) to their day files. Only the lease
// holder writes; others get an error.
func (s *Store) Append(rs []Rec) error {
	if len(rs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.writer {
		return errors.New("ticks: not the writer for " + s.coin)
	}
	release, err := budgetLock(s.root)
	if err != nil {
		return err
	}
	defer release()
	protected := make(map[string]bool)
	extra := int64(len(rs)) * recSize
	for _, r := range rs {
		path := filepath.Join(s.dir, dayName(DayOf(r.Time)))
		if protected[path] {
			continue
		}
		protected[path] = true
		st, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			extra += int64(len(magic))
		} else if err != nil {
			return err
		} else if st.Size() < int64(len(magic)) {
			extra += int64(len(magic)) - st.Size()
		}
	}
	if err := s.pruneLocked(extra, protected); err != nil {
		return err
	}
	for len(rs) > 0 {
		day := DayOf(rs[0].Time)
		n := 1
		for n < len(rs) && DayOf(rs[n].Time) == day {
			n++
		}
		buf := make([]byte, 0, n*recSize)
		for _, r := range rs[:n] {
			buf = encode(buf, r)
		}
		f, err := s.fileFor(day)
		if err != nil {
			return err
		}
		if _, err := f.Write(buf); err != nil {
			return fmt.Errorf("ticks: %w", err)
		}
		rs = rs[n:]
	}
	return nil
}

// fileFor returns the open append handle of a day, cutting a torn tail.
func (s *Store) fileFor(day int64) (*os.File, error) {
	if s.f != nil && s.fDay == day {
		open, openErr := s.f.Stat()
		linked, linkErr := os.Stat(filepath.Join(s.dir, dayName(day)))
		if openErr == nil && linkErr == nil && os.SameFile(open, linked) {
			return s.f, nil
		}
	}
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	path := filepath.Join(s.dir, dayName(day))
	if st, err := os.Stat(path); err == nil && st.Size() > int64(len(magic)) {
		if extra := (st.Size() - int64(len(magic))) % recSize; extra != 0 {
			if err := os.Truncate(path, st.Size()-extra); err != nil {
				return nil, fmt.Errorf("ticks: %w", err)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ticks: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("ticks: %w", err)
	}
	if st.Size() < int64(len(magic)) {
		// empty (or a torn header): start the file over
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("ticks: %w", err)
		}
		if _, err := f.Write([]byte(magic)); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("ticks: %w", err)
		}
	}
	s.f, s.fDay = f, day
	return f, nil
}

// Days lists the stored UTC days, oldest first.
func (s *Store) Days() ([]int64, error) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []int64
	for _, e := range ents {
		if d, ok := parseDay(e.Name()); ok && e.Type().IsRegular() {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// LoadDay reads one day's trades in stored (arrival) order.
func (s *Store) LoadDay(day int64) ([]Rec, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, dayName(day)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return decodeFile(b)
}

func decodeFile(b []byte) ([]Rec, error) {
	if len(b) < len(magic) {
		return nil, nil
	}
	if !bytes.Equal(b[:len(magic)], []byte(magic)) {
		return nil, errors.New("ticks: not a trade file")
	}
	b = b[len(magic):]
	out := make([]Rec, 0, len(b)/recSize)
	for len(b) >= recSize {
		out = append(out, decode(b[:recSize]))
		b = b[recSize:]
	}
	return out, nil
}

// Missing filters a pending buffer against the disk before a lease takeover
// or retry. A previous writer (or a partially successful append) may already
// have stored some records. Scan with bounded memory, including large days.
func (s *Store) Missing(rs []Rec) ([]Rec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[uint64]bool, len(rs))
	days := make(map[int64]bool)
	for _, r := range rs {
		if r.TID != 0 {
			wanted[r.TID] = true
		}
		days[DayOf(r.Time)] = true
	}
	for day := range days {
		f, err := os.Open(filepath.Join(s.dir, dayName(day)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer f.Close()
			rd := bufio.NewReaderSize(f, 64*1024)
			var header [len(magic)]byte
			if _, err := io.ReadFull(rd, header[:]); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					return nil // the next writer repairs a torn header
				}
				return err
			}
			if string(header[:]) != magic {
				return errors.New("ticks: not a trade file")
			}
			var b [recSize]byte
			for len(wanted) > 0 {
				if _, err := io.ReadFull(rd, b[:]); err != nil {
					if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
						return nil
					}
					return err
				}
				delete(wanted, binary.LittleEndian.Uint64(b[8:]))
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	out := make([]Rec, 0, len(rs))
	for _, r := range rs {
		if r.TID == 0 || wanted[r.TID] {
			out = append(out, r)
			delete(wanted, r.TID)
		}
	}
	return out, nil
}

// Stats describe what one coin's store holds.
type Stats struct {
	From   int64 `json:"from"`   // oldest stored trade, ms (0 = none)
	To     int64 `json:"to"`     // newest stored trade, ms
	Trades int64 `json:"trades"` // records stored (a resent trade may count twice)
	Bytes  int64 `json:"bytes"`
	Days   int   `json:"days"` // UTC day files
}

// Stats reads file sizes and the first and last records.
func (s *Store) Stats() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	days, err := s.Days()
	if err != nil {
		return Stats{}, err
	}
	if s.bounds == nil {
		s.bounds = make(map[int64]tradeBounds)
	}
	var st Stats
	active := make(map[int64]bool, len(days))
	for _, d := range days {
		fi, err := os.Stat(filepath.Join(s.dir, dayName(d)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // another coin's writer may have pruned an old day
		}
		if err != nil {
			return Stats{}, err
		}
		st.Bytes += fi.Size()
		if n := fi.Size() - int64(len(magic)); n > 0 {
			st.Trades += n / recSize
		}
		st.Days++
		active[d] = true
		bounds, err := s.readBounds(d, fi)
		if err != nil {
			return Stats{}, err
		}
		if bounds.from != 0 && (st.From == 0 || bounds.from < st.From) {
			st.From = bounds.from
		}
		st.To = max(st.To, bounds.to)
	}
	for d := range s.bounds {
		if !active[d] {
			delete(s.bounds, d)
		}
	}
	return st, nil
}

// Files are append-only, but their trade times need not be: reconnects can
// append an older snapshot. Cache the range of the complete prefix and scan
// only new records on later status reads, including when following a writer.
type tradeBounds struct {
	info     fs.FileInfo
	bytes    int64
	from, to int64
}

func (s *Store) readBounds(day int64, fi fs.FileInfo) (tradeBounds, error) {
	b := s.bounds[day]
	if b.info == nil || !os.SameFile(b.info, fi) || fi.Size() < b.bytes {
		b = tradeBounds{info: fi, bytes: int64(len(magic))}
	}
	target := int64(len(magic)) + max(int64(0), (fi.Size()-int64(len(magic)))/recSize)*recSize
	if target <= b.bytes {
		return b, nil
	}
	f, err := os.Open(filepath.Join(s.dir, dayName(day)))
	if errors.Is(err, fs.ErrNotExist) {
		return tradeBounds{}, nil
	}
	if err != nil {
		return b, err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(io.NewSectionReader(f, b.bytes, target-b.bytes), 64*1024)
	var rec [recSize]byte
	for b.bytes < target {
		if _, err := io.ReadFull(rd, rec[:]); err != nil {
			return b, err
		}
		t := int64(binary.LittleEndian.Uint64(rec[:]))
		if b.from == 0 || t < b.from {
			b.from = t
		}
		b.to = max(b.to, t)
		b.bytes += recSize
	}
	s.bounds[day] = b
	return b, nil
}

// Prune applies retention and the size cap now (also checked on every append).
func (s *Store) Prune() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := budgetLock(s.root)
	if err != nil {
		return err
	}
	defer release()
	return s.pruneLocked(0, nil)
}

func (s *Store) pruneLocked(extra int64, protected map[string]bool) error {
	now := s.o.Now()
	today := DayOf(now.UnixMilli())
	cutoff := today - int64(s.o.RetentionDays)
	type seg struct {
		path string
		day  int64
		size int64
	}
	var segs []seg
	var total int64
	coins, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, c := range coins {
		if !c.IsDir() {
			continue
		}
		dir := filepath.Join(s.root, c.Name())
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			d, ok := parseDay(e.Name())
			if !ok || !e.Type().IsRegular() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if d < cutoff && dir == s.dir && !protected[p] {
				if err := os.Remove(p); err == nil {
					continue
				}
			}
			fi, err := e.Info()
			if err != nil {
				return err
			}
			segs = append(segs, seg{p, d, fi.Size()})
			total += fi.Size()
		}
	}
	if total+extra <= s.o.MaxBytes {
		return nil
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].day < segs[j].day })
	for _, sg := range segs {
		if total+extra <= s.o.MaxBytes || sg.day >= today {
			break
		}
		if protected[sg.path] {
			continue
		}
		if os.Remove(sg.path) == nil {
			total -= sg.size
		}
	}
	if total+extra > s.o.MaxBytes {
		return ErrCapacity
	}
	return nil
}

// Coins lists the coins with a store under root (directory names decoded).
func Coins(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if c, ok := decodeCoinDir(e.Name()); ok {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

func decodeCoinDir(s string) (string, bool) {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b = append(b, s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		var v byte
		if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &v); err != nil {
			return "", false
		}
		b = append(b, v)
		i += 2
	}
	return string(b), len(b) > 0
}
