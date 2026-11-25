// Package evm is a minimal, read-only HyperEVM client: JSON-RPC eth_call,
// eth_getCode, eth_chainId and eth_blockNumber with endpoint failover, plus
// ABI encoding and decoding for the static types the strategy-vault reads
// need (address, uintN, int256, bool, string, arrays of static tuples).
//
// It deliberately has no go-ethereum dependency, holds no keys and cannot
// send transactions: there is no eth_sendRawTransaction, no signer and no
// method that writes state.
package evm
