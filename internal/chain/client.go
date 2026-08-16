// Package chain adapts Robinhood Chain JSON-RPC to the domain types.
//
// Read-only by design: this package has no signer and no broadcast path.
// Transaction signing and submission arrive in a later phase.
package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// ErrNotFound reports a missing block, transaction or receipt.
var ErrNotFound = errors.New("not found")

// Reader is the read-only chain surface the application depends on.
// Keeping it an interface here lets tests and later use cases swap in fakes
// without pulling go-ethereum into the domain.
type Reader interface {
	ChainID(ctx context.Context) (uint64, error)
	LatestBlock(ctx context.Context) (domain.BlockRef, error)
	BlockByNumber(ctx context.Context, number uint64) (domain.BlockRef, error)
	TransactionByHash(ctx context.Context, h domain.Hash) (*Transaction, error)
	ReceiptByHash(ctx context.Context, h domain.Hash) (*Receipt, error)
	BalanceAt(ctx context.Context, addr domain.Address, blockNumber *uint64) (domain.Wei, error)
	FilterLogs(ctx context.Context, q LogQuery) ([]Log, error)
	Close()
}

// Transaction is a normalised view of an on-chain transaction.
type Transaction struct {
	Hash     domain.Hash
	From     domain.Address
	To       *domain.Address // nil for contract creation
	Value    domain.Wei
	Nonce    uint64
	Gas      uint64
	GasPrice *big.Int
	Data     []byte
	Pending  bool
}

// Receipt is a normalised transaction receipt.
type Receipt struct {
	TxHash      domain.Hash
	Status      uint64
	BlockNumber uint64
	BlockHash   domain.Hash
	GasUsed     uint64
	// EffectiveGasPrice may be nil on nodes that omit it.
	EffectiveGasPrice *big.Int
	ContractAddress   *domain.Address
	Logs              []Log
}

// LogQuery selects event logs. FromBlock/ToBlock nil means "latest".
type LogQuery struct {
	FromBlock *uint64
	ToBlock   *uint64
	Addresses []domain.Address
	Topics    [][]domain.Hash
}

// Log is a normalised event log.
type Log struct {
	Address     domain.Address
	Topics      []domain.Hash
	Data        []byte
	BlockNumber uint64
	TxHash      domain.Hash
	LogIndex    uint
	Removed     bool
}

// Options configure a Client.
type Options struct {
	RPCURL         string
	ExpectedChain  uint64
	RequestTimeout time.Duration
	MaxRetries     int
	RetryBackoff   time.Duration
}

// Client is a retrying, timeout-bounded JSON-RPC reader.
type Client struct {
	eth   *ethclient.Client
	rpc   *rpc.Client
	opts  Options
	retry retryPolicy
}

// Dial connects over HTTP(S) and verifies the chain ID before returning.
// Refusing to start on the wrong network is cheaper than discovering it mid-trade.
func Dial(ctx context.Context, opts Options) (*Client, error) {
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 10 * time.Second
	}
	rc, err := rpc.DialOptions(ctx, opts.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial rpc: %w", err)
	}
	c := &Client{
		eth:   ethclient.NewClient(rc),
		rpc:   rc,
		opts:  opts,
		retry: retryPolicy{MaxRetries: opts.MaxRetries, Backoff: opts.RetryBackoff},
	}

	if opts.ExpectedChain != 0 {
		got, err := c.ChainID(ctx)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("verify chain id: %w", err)
		}
		if got != opts.ExpectedChain {
			c.Close()
			return nil, fmt.Errorf("%w: node reports %d (%s), expected %d (%s)",
				domain.ErrWrongChain, got, domain.ChainName(got),
				opts.ExpectedChain, domain.ChainName(opts.ExpectedChain))
		}
	}
	return c, nil
}

// Close releases the underlying connection.
func (c *Client) Close() {
	if c.eth != nil {
		c.eth.Close()
	}
}

// call runs fn under a per-request timeout with bounded retries.
func (c *Client) call(ctx context.Context, fn func(context.Context) error) error {
	return c.retry.Do(ctx, func(ctx context.Context) error {
		rctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
		defer cancel()
		return fn(rctx)
	})
}

// ChainID returns the chain ID reported by the node.
func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	var id *big.Int
	err := c.call(ctx, func(ctx context.Context) error {
		var e error
		id, e = c.eth.ChainID(ctx)
		return e
	})
	if err != nil {
		return 0, err
	}
	if !id.IsUint64() {
		return 0, fmt.Errorf("chain id %s out of range", id)
	}
	return id.Uint64(), nil
}

// LatestBlock returns the current head.
func (c *Client) LatestBlock(ctx context.Context) (domain.BlockRef, error) {
	return c.headerRef(ctx, nil)
}

// BlockByNumber returns a block header by height.
func (c *Client) BlockByNumber(ctx context.Context, number uint64) (domain.BlockRef, error) {
	return c.headerRef(ctx, new(big.Int).SetUint64(number))
}

func (c *Client) headerRef(ctx context.Context, number *big.Int) (domain.BlockRef, error) {
	var h *types.Header
	err := c.call(ctx, func(ctx context.Context) error {
		var e error
		h, e = c.eth.HeaderByNumber(ctx, number)
		return e
	})
	if errors.Is(err, ethereum.NotFound) {
		return domain.BlockRef{}, ErrNotFound
	}
	if err != nil {
		return domain.BlockRef{}, err
	}
	return headerToRef(h), nil
}

func headerToRef(h *types.Header) domain.BlockRef {
	return domain.BlockRef{
		Number:     h.Number.Uint64(),
		Hash:       domain.Hash(h.Hash().Hex()),
		ParentHash: domain.Hash(h.ParentHash.Hex()),
		Time:       time.Unix(int64(h.Time), 0).UTC(),
	}
}

// TransactionByHash looks up a transaction, including pending ones.
func (c *Client) TransactionByHash(ctx context.Context, h domain.Hash) (*Transaction, error) {
	var (
		tx      *types.Transaction
		pending bool
	)
	err := c.call(ctx, func(ctx context.Context) error {
		var e error
		tx, pending, e = c.eth.TransactionByHash(ctx, common.HexToHash(h.String()))
		return e
	})
	if errors.Is(err, ethereum.NotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Sender recovery needs the chain ID the transaction was signed for.
	from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return nil, fmt.Errorf("recover sender: %w", err)
	}

	out := &Transaction{
		Hash:     domain.Hash(tx.Hash().Hex()),
		From:     domain.Address(from.Hex()),
		Value:    domain.NewWei(tx.Value()),
		Nonce:    tx.Nonce(),
		Gas:      tx.Gas(),
		GasPrice: tx.GasPrice(),
		Data:     tx.Data(),
		Pending:  pending,
	}
	if to := tx.To(); to != nil {
		a := domain.Address(to.Hex())
		out.To = &a
	}
	return normaliseTx(out), nil
}

// ReceiptByHash returns a transaction receipt.
func (c *Client) ReceiptByHash(ctx context.Context, h domain.Hash) (*Receipt, error) {
	var r *types.Receipt
	err := c.call(ctx, func(ctx context.Context) error {
		var e error
		r, e = c.eth.TransactionReceipt(ctx, common.HexToHash(h.String()))
		return e
	})
	if errors.Is(err, ethereum.NotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	out := &Receipt{
		TxHash:            domain.Hash(r.TxHash.Hex()),
		Status:            r.Status,
		BlockNumber:       r.BlockNumber.Uint64(),
		BlockHash:         domain.Hash(r.BlockHash.Hex()),
		GasUsed:           r.GasUsed,
		EffectiveGasPrice: r.EffectiveGasPrice,
		Logs:              make([]Log, 0, len(r.Logs)),
	}
	if r.ContractAddress != (common.Address{}) {
		a := domain.Address(r.ContractAddress.Hex())
		out.ContractAddress = &a
	}
	for _, l := range r.Logs {
		out.Logs = append(out.Logs, convertLog(l))
	}
	return normaliseReceipt(out), nil
}

// BalanceAt returns a native ETH balance. A nil blockNumber means latest.
func (c *Client) BalanceAt(ctx context.Context, addr domain.Address, blockNumber *uint64) (domain.Wei, error) {
	parsed, err := domain.ParseAddress(addr.String())
	if err != nil {
		return domain.Wei{}, err
	}
	var bn *big.Int
	if blockNumber != nil {
		bn = new(big.Int).SetUint64(*blockNumber)
	}
	var bal *big.Int
	err = c.call(ctx, func(ctx context.Context) error {
		var e error
		bal, e = c.eth.BalanceAt(ctx, common.HexToAddress(parsed.String()), bn)
		return e
	})
	if err != nil {
		return domain.Wei{}, err
	}
	return domain.NewWei(bal), nil
}

// FilterLogs retrieves event logs matching the query.
func (c *Client) FilterLogs(ctx context.Context, q LogQuery) ([]Log, error) {
	fq := ethereum.FilterQuery{}
	if q.FromBlock != nil {
		fq.FromBlock = new(big.Int).SetUint64(*q.FromBlock)
	}
	if q.ToBlock != nil {
		fq.ToBlock = new(big.Int).SetUint64(*q.ToBlock)
	}
	for _, a := range q.Addresses {
		parsed, err := domain.ParseAddress(a.String())
		if err != nil {
			return nil, err
		}
		fq.Addresses = append(fq.Addresses, common.HexToAddress(parsed.String()))
	}
	for _, group := range q.Topics {
		row := make([]common.Hash, 0, len(group))
		for _, t := range group {
			row = append(row, common.HexToHash(t.String()))
		}
		fq.Topics = append(fq.Topics, row)
	}

	var raw []types.Log
	err := c.call(ctx, func(ctx context.Context) error {
		var e error
		raw, e = c.eth.FilterLogs(ctx, fq)
		return e
	})
	if err != nil {
		return nil, err
	}
	out := make([]Log, 0, len(raw))
	for i := range raw {
		out = append(out, convertLog(&raw[i]))
	}
	return out, nil
}

func convertLog(l *types.Log) Log {
	topics := make([]domain.Hash, 0, len(l.Topics))
	for _, t := range l.Topics {
		h, _ := domain.ParseHash(t.Hex())
		topics = append(topics, h)
	}
	addr, _ := domain.ParseAddress(l.Address.Hex())
	txh, _ := domain.ParseHash(l.TxHash.Hex())
	return Log{
		Address:     addr,
		Topics:      topics,
		Data:        l.Data,
		BlockNumber: l.BlockNumber,
		TxHash:      txh,
		LogIndex:    l.Index,
		Removed:     l.Removed,
	}
}

// normaliseTx lowercases hex fields so downstream storage keys are canonical.
func normaliseTx(t *Transaction) *Transaction {
	if h, err := domain.ParseHash(t.Hash.String()); err == nil {
		t.Hash = h
	}
	if a, err := domain.ParseAddress(t.From.String()); err == nil {
		t.From = a
	}
	if t.To != nil {
		if a, err := domain.ParseAddress(t.To.String()); err == nil {
			t.To = &a
		}
	}
	return t
}

func normaliseReceipt(r *Receipt) *Receipt {
	if h, err := domain.ParseHash(r.TxHash.String()); err == nil {
		r.TxHash = h
	}
	if h, err := domain.ParseHash(r.BlockHash.String()); err == nil {
		r.BlockHash = h
	}
	if r.ContractAddress != nil {
		if a, err := domain.ParseAddress(r.ContractAddress.String()); err == nil {
			r.ContractAddress = &a
		}
	}
	return r
}

var _ Reader = (*Client)(nil)
