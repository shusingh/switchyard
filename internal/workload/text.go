package workload

import (
	"hash/fnv"
	"strings"
)

// vocabulary holds common English words that most LLM tokenizers encode as a
// single token when preceded by a space, so a segment of N words is close to
// N tokens. The exact count is measured against the real tokenizer during the
// GPU benchmarks (the server's usage report gives the true prompt size).
var vocabulary = strings.Fields(`
the of and to in is that for it as was with be by on not he this are or his
from at which but have an they you were her she there been one all we their
has would when if so no will more can out other its about into them than
some could time these two may then do first any my now such like our over
man me even most made after also did many before must through back years
where much your way well down should because each just those people how too
little state good very make world still own see men work long get here
between both life being under never day same another know while last might
us great old year off come since against go came right used take three
states himself few house use during without again place around however home
small found thought went say part once general high upon school every
does got united left number course war until always away something fact
though water less public put think almost hand enough far took head yet
government system better set told nothing night end why called did find
going look asked later knew point next program city business give group
toward young days let room president side social given present several
order national possible rather second face per among form important often
things looked early white case large become john big need four within felt
along children saw best church ever least power development light thing
seemed family interest want members mind country area others done turned
although open god service certain kind problem began different door thus
help sense means whole matter perhaps itself york times law human line
above name example action company hands local show whether five history
`)

// Segment is a deterministic run of synthetic words. The same seed always
// renders the same text, so requests that share segments share byte-identical
// prefixes, exactly like an agent resending its growing context.
type Segment struct {
	Seed  uint64
	Words int
}

// render appends the segment's text to b. Every word is preceded by a space.
func (s Segment) render(b *strings.Builder) {
	state := s.Seed
	for range s.Words {
		b.WriteByte(' ')
		b.WriteString(vocabulary[splitmix64(&state)%uint64(len(vocabulary))])
	}
}

// renderSegments renders segments in order into one string.
func renderSegments(segs []Segment) string {
	var b strings.Builder
	words := 0
	for _, s := range segs {
		words += s.Words
	}
	b.Grow(words * 6)
	for _, s := range segs {
		s.render(&b)
	}
	return b.String()
}

// splitmix64 is a small, fast, well-distributed PRNG step (Steele et al.,
// "Fast Splittable Pseudorandom Number Generators", 2014).
func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// SeedFor derives a stable segment seed from a sequence of identifiers, so
// generators can name content ("app 3, system prompt") without coordinating.
func SeedFor(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}
