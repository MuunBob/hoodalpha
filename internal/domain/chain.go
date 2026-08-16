// Package domain holds the core trading concepts. It must not import
// PostgreSQL, Redis, Asynq, Telegram, HTTP or RPC packages.
package domain

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// ErrWrongChain is returned when a connected node reports an unexpected chain ID.
var ErrWrongChain = errors.New("chain id mismatch")

// KnownChains maps the Robinhood Chain networks documented at
// https://docs.robinhood.com/chain/connecting/ to human names.
const (
	ChainIDMainnet uint64 = 4663
	ChainIDTestnet uint64 = 46630
)

// ChainName returns a readable network name for logs and health output.
func ChainName(id uint64) string {
	switch id {
	case ChainIDMainnet:
		return "robinhood-mainnet"
	case ChainIDTestnet:
		return "robinhood-testnet"
	default:
		return fmt.Sprintf("chain-%d", id)
	}
}

// Address is a checksum-agnostic EVM address stored in lowercase hex.
// Storing one canonical form keeps database lookups and dedup trivial.
type Address string

var addressRe = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// ParseAddress validates an EVM address and normalises it to lowercase hex.
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if !addressRe.MatchString(s) {
		return "", fmt.Errorf("invalid evm address %q", s)
	}
	return Address(strings.ToLower(s)), nil
}

// String returns the lowercase hex form.
func (a Address) String() string { return string(a) }

// IsZero reports whether the address is the EVM zero address.
func (a Address) IsZero() bool {
	return a == "0x0000000000000000000000000000000000000000"
}

// Hash is a 32-byte transaction or block hash in lowercase hex.
type Hash string

var hashRe = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// ParseHash validates a 32-byte hash and normalises it to lowercase hex.
func ParseHash(s string) (Hash, error) {
	s = strings.TrimSpace(s)
	if !hashRe.MatchString(s) {
		return "", fmt.Errorf("invalid hash %q", s)
	}
	return Hash(strings.ToLower(s)), nil
}

// String returns the lowercase hex form.
func (h Hash) String() string { return string(h) }

// BlockRef is the minimal identity of a block: enough to detect progress,
// staleness and reorgs without carrying the full block body around.
type BlockRef struct {
	Number     uint64
	Hash       Hash
	ParentHash Hash
	Time       time.Time
}

// IsStale reports whether the block is older than maxAge relative to now.
// Used by the health check to detect a silently wedged RPC connection.
func (b BlockRef) IsStale(now time.Time, maxAge time.Duration) bool {
	if b.Time.IsZero() {
		return true
	}
	return now.Sub(b.Time) > maxAge
}

// Wei is a native-token amount in the chain's smallest unit. Native amounts are
// exact integers, so they never touch floating point.
type Wei struct{ i *big.Int }

// NewWei wraps a big.Int. A nil or negative input yields zero.
func NewWei(i *big.Int) Wei {
	if i == nil || i.Sign() < 0 {
		return Wei{i: new(big.Int)}
	}
	return Wei{i: new(big.Int).Set(i)}
}

// BigInt returns a defensive copy of the underlying value.
func (w Wei) BigInt() *big.Int {
	if w.i == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(w.i)
}

// IsZero reports whether the amount is zero.
func (w Wei) IsZero() bool { return w.i == nil || w.i.Sign() == 0 }

// String returns the decimal wei value.
func (w Wei) String() string { return w.BigInt().String() }

// Ether renders the amount as a fixed-point decimal string with 18 decimals.
// It is for display only; never feed this back into arithmetic.
func (w Wei) Ether() string {
	v := w.BigInt()
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	whole, frac := new(big.Int).QuoRem(v, unit, new(big.Int))
	s := fmt.Sprintf("%s.%018s", whole.String(), frac.String())
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
