package llm

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sync"
	"time"

	"github.com/sjawhar/envoy/agentloop"
)

// The measures of the pacer.
const (
	// paceWindow is the span the pacer's limit holds over: any one minute.
	paceWindow = time.Minute
	// cacheLife is how long the provider keeps a prefix after a call writes or reads it: the
	// five minutes of the cache mark that Run asks for (agentloop's HistoryMarkOnly).
	cacheLife = 5 * time.Minute
	// bytesPerToken is how the harness estimates a prompt's tokens from its bytes, as
	// writing-check's reader budget counts them.
	bytesPerToken = 4
)

// Pacer holds the uncached input of the model calls that pass it to a limit of tokens in any one
// minute, across every Runner that shares it. A call's uncached input is the prompt tokens the
// provider bills at the full input price: its input tokens less those it read from the cache. A
// cache write counts. Before each attempt of a call, the pacer charges the attempt's estimate. It
// waits while the charges of the last minute leave no room for that estimate. After the reply, it
// corrects the charge to the tokens the provider reported. Calls go through in the order they
// came. A call larger than the limit goes when the last minute holds no charge, and then it is
// the only call of its minute. A Pacer of limit 0 holds no call back: it only counts.
type Pacer struct {
	limit int
	// turn lets one call at a time wait for room, so the calls go through in the order they came.
	turn chan struct{}

	mu sync.Mutex
	// charges are every charge of the process, oldest first. window is the index of the first
	// charge less than a minute old, and inWindow the tokens of the charges from there on.
	charges  []charge
	window   int
	inWindow int
	// freed is closed, and replaced, when a correction gives tokens of the last minute back.
	freed chan struct{}
	// written is, for each prefix (estimate.key), the start of the newest attempt whose reply
	// reported the prefix written to the provider's cache or read from it.
	written map[[sha256.Size]byte]time.Time
}

// charge is one attempt's uncached input: the estimate until its reply, then what the reply
// reported.
type charge struct {
	at     time.Time
	tokens int
}

// NewPacer is a pacer of perMinute uncached input tokens a minute; 0 holds no call back.
func NewPacer(perMinute int) *Pacer {
	return &Pacer{
		limit:   max(perMinute, 0),
		turn:    make(chan struct{}, 1),
		freed:   make(chan struct{}),
		written: map[[sha256.Size]byte]time.Time{},
	}
}

// estimate is one call's prompt as the pacer reads it: its tokens and those of its marked prefix,
// each at four bytes a token, and the prefix's key. marked is false for a call that carries no
// cache mark, which the pacer charges whole.
type estimate struct {
	whole, prefix int
	key           [sha256.Size]byte
	marked        bool
}

// tokensOf is n bytes of prompt in tokens, at four bytes a token.
func tokensOf(n int) int { return (n + bytesPerToken - 1) / bytesPerToken }

// estimateOf is the estimate of req on model, whose Shared goes as the blocks shared. The prefix
// is what the provider caches up to the mark at Shared's end: the model with its effort, the
// system prompt, the schema and the Shared blocks. A call that sets no Mark caches nothing, so
// its estimate is not marked.
func estimateOf(model agentloop.Model, req Request, shared []agentloop.Block) estimate {
	prefix := len(req.System) + len(req.Schema)
	for _, b := range shared {
		prefix += len(b.Text)
	}
	e := estimate{whole: tokensOf(prefix + len(req.User)), prefix: tokensOf(prefix)}
	if !req.Mark || len(shared) == 0 {
		return e
	}
	// Each field goes after its length, so no two prefixes hash the same bytes.
	h := sha256.New()
	length := func(n int) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		h.Write(b[:])
	}
	for _, s := range [...]string{string(model.Endpoint.Kind), model.Endpoint.BaseURL, model.ID, model.Effort, req.System} {
		length(len(s))
		_, _ = io.WriteString(h, s)
	}
	length(len(req.Schema))
	h.Write(req.Schema)
	for _, b := range shared {
		length(len(b.Text))
		_, _ = io.WriteString(h, b.Text)
	}
	h.Sum(e.key[:0])
	e.marked = true
	return e
}

// paced is one attempt's charge, which settle corrects after the reply. A paced of no pacer
// corrects nothing.
type paced struct {
	p      *Pacer
	index  int
	at     time.Time
	key    [sha256.Size]byte
	marked bool
}

// admit waits until the charges of the last minute leave room for the attempt's estimate, or
// until ctx ends, and charges it. The estimate is the whole prompt, less the marked prefix where
// an attempt that sent the same prefix within the cache's life had a reply that reported it
// written to the cache or read from it. A nil Pacer admits at once and charges nothing.
func (p *Pacer) admit(ctx context.Context, e estimate) (paced, error) {
	if p == nil {
		return paced{}, nil
	}
	if p.limit > 0 {
		select {
		case p.turn <- struct{}{}:
		case <-ctx.Done():
			return paced{}, ctx.Err()
		}
		defer func() { <-p.turn }()
	}
	for {
		p.mu.Lock()
		now := time.Now()
		p.slide(now)
		tokens := e.whole
		if e.marked {
			if at, ok := p.written[e.key]; ok {
				if now.Sub(at) < cacheLife {
					tokens -= e.prefix
				} else {
					delete(p.written, e.key)
				}
			}
		}
		if p.limit == 0 || p.inWindow == 0 || p.inWindow+max(tokens, 1) <= p.limit {
			p.charges = append(p.charges, charge{at: now, tokens: tokens})
			p.inWindow += tokens
			p.mu.Unlock()
			return paced{p: p, index: len(p.charges) - 1, at: now, key: e.key, marked: e.marked}, nil
		}
		// The last minute holds tokens, so it holds a charge: wait until its oldest charge leaves
		// it, or until a reply gives tokens back.
		timer := time.NewTimer(p.charges[p.window].at.Add(paceWindow).Sub(now))
		freed := p.freed
		p.mu.Unlock()
		select {
		case <-timer.C:
		case <-freed:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return paced{}, ctx.Err()
		}
	}
}

// slide drops from the last minute's tokens the charges a minute old or older at now.
func (p *Pacer) slide(now time.Time) {
	for p.window < len(p.charges) && !now.Before(p.charges[p.window].at.Add(paceWindow)) {
		p.inWindow -= p.charges[p.window].tokens
		p.window++
	}
}

// settle corrects the attempt's charge to the uncached input its reply reported in used: the
// input tokens and the cache write tokens. A reply that reported no counts keeps the estimate. A
// reply that reported a cache write or a cache read puts the attempt's prefix in the cache for
// the cache's life from the attempt's start.
func (c paced) settle(used agentloop.Usage) {
	p := c.p
	if p == nil || !used.Metered {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := &p.charges[c.index]
	tokens := used.Input + used.CacheWrite
	if c.index >= p.window {
		p.inWindow += tokens - ch.tokens
		if tokens < ch.tokens && p.limit > 0 {
			close(p.freed)
			p.freed = make(chan struct{})
		}
	}
	ch.tokens = tokens
	if c.marked && (used.CacheWrite > 0 || used.CacheRead > 0) && p.written[c.key].Before(c.at) {
		p.written[c.key] = c.at
	}
}

// PaceUse is the uncached input of the calls that passed a pacer: each attempt's tokens as its
// reply reported them, or its estimate where the reply reported none.
type PaceUse struct {
	// Tokens is the uncached input of every attempt, and BusiestMinute the most of it in any one
	// minute, each attempt counted at its start.
	Tokens        int `json:"tokens"`
	BusiestMinute int `json:"busiest_minute_tokens"`
}

// Use is the uncached input of the calls that passed the pacer so far; none for a nil Pacer.
func (p *Pacer) Use() PaceUse {
	if p == nil {
		return PaceUse{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var u PaceUse
	first, minute := 0, 0
	for _, c := range p.charges {
		u.Tokens += c.tokens
		minute += c.tokens
		for !c.at.Before(p.charges[first].at.Add(paceWindow)) {
			minute -= p.charges[first].tokens
			first++
		}
		u.BusiestMinute = max(u.BusiestMinute, minute)
	}
	return u
}
