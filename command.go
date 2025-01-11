package xrpl

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/gorilla/websocket"
)

func (c *Client) Subscribe(streams []string) (BaseResponse, error) {
	req := BaseRequest{
		"command": "subscribe",
		"streams": streams,
	}
	res, err := c.Request(req)
	if err != nil {
		return nil, err
	}

	c.mutex.Lock()
	for _, stream := range streams {
		c.StreamSubscriptions[stream] = true
	}
	c.mutex.Unlock()

	return res, nil
}

func (c *Client) Unsubscribe(streams []string) (BaseResponse, error) {
	req := BaseRequest{
		"command": "unsubscribe",
		"streams": streams,
	}
	res, err := c.Request(req)
	if err != nil {
		return nil, err
	}

	c.mutex.Lock()
	for _, stream := range streams {
		delete(c.StreamSubscriptions, stream)
	}
	c.mutex.Unlock()

	return res, nil
}

// Send a websocket request. This method takes a BaseRequest object and automatically adds
// incremental request ID to it.
//
// Example usage:
//
//	req := BaseRequest{
//		"command": "account_info",
//		"account": "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
//		"ledger_index": "current",
//	}
//
//	err := client.Request(req, func(){})
func (c *Client) Request(req BaseRequest) (BaseResponse, error) {
	requestId := c.NextID()
	req["id"] = requestId
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	ch := make(chan BaseResponse, 1)

	c.mutex.Lock()
	c.requestQueue[requestId] = ch
	err = c.connection.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		return nil, err
	}
	c.mutex.Unlock()

	res := <-ch
	return res, nil
}

// XRPLBase58Alphabet is the specific alphabet used by XRPL
const XRPLBase58Alphabet = "rpshnaf39wBUDNEGHJKLM4PQRST7VWXYZ2bcdeCg65jkm8oFqi1tuvAxyz"

var (
	familySeedPrefix    = []byte{0x21}
	accountPublicPrefix = []byte{0x23}
	nodePublicPrefix    = []byte{0x1C}
)

// Base58 encoding specific to XRPL
type Base58 struct {
	alphabet     string
	alphabetMap  map[rune]int
	baseMultiple *big.Int
}

// NewBase58 creates a new Base58 encoder using the XRPL alphabet
func NewBase58() *Base58 {
	b58 := &Base58{
		alphabet:     XRPLBase58Alphabet,
		alphabetMap:  make(map[rune]int),
		baseMultiple: big.NewInt(58),
	}
	for i, char := range b58.alphabet {
		b58.alphabetMap[char] = i
	}
	return b58
}

// Encode converts bytes to a base58 string
func (b58 *Base58) Encode(input []byte) string {
	x := new(big.Int)
	x.SetBytes(input)

	// Convert to base58
	var result []byte
	mod := new(big.Int)
	zero := big.NewInt(0)

	for x.Cmp(zero) > 0 {
		x.DivMod(x, b58.baseMultiple, mod)
		result = append(result, b58.alphabet[mod.Int64()])
	}

	// Add leading zeros
	for _, b := range input {
		if b != 0 {
			break
		}
		result = append(result, b58.alphabet[0])
	}

	// Reverse the result
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return string(result)
}

// Decode converts a base58 string to bytes
func (b58 *Base58) Decode(input string) ([]byte, error) {
	result := big.NewInt(0)
	multi := big.NewInt(1)

	// Process the string from right to left
	for i := len(input) - 1; i >= 0; i-- {
		digit, ok := b58.alphabetMap[rune(input[i])]
		if !ok {
			return nil, fmt.Errorf("invalid character '%c' in base58 string", input[i])
		}

		val := big.NewInt(int64(digit))
		val.Mul(val, multi)
		result.Add(result, val)
		multi.Mul(multi, b58.baseMultiple)
	}

	// Convert to bytes
	resultBytes := result.Bytes()

	// Add leading zeros
	var numZeros int
	for i := 0; i < len(input) && input[i] == b58.alphabet[0]; i++ {
		numZeros++
	}

	// Prepend zeros
	if numZeros > 0 {
		zeros := make([]byte, numZeros)
		resultBytes = append(zeros, resultBytes...)
	}

	return resultBytes, nil
}

// EncodeCheck encodes with version byte and checksum
func (b58 *Base58) EncodeCheck(version byte, payload []byte) string {
	// Add version byte
	data := append([]byte{version}, payload...)

	// Add checksum
	checksum := sha256.Sum256(data)
	checksum = sha256.Sum256(checksum[:])
	data = append(data, checksum[:4]...)

	return b58.Encode(data)
}

// DecodeCheck decodes and verifies checksum
func (b58 *Base58) DecodeCheck(input string) (version byte, payload []byte, err error) {
	decoded, err := b58.Decode(input)
	if err != nil {
		return 0, nil, err
	}

	if len(decoded) < 5 {
		return 0, nil, fmt.Errorf("invalid decoded length")
	}

	// Split version, payload, and checksum
	version = decoded[0]
	payload = decoded[1 : len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	// Verify checksum
	toCheck := decoded[:len(decoded)-4]
	hash1 := sha256.Sum256(toCheck)
	hash2 := sha256.Sum256(hash1[:])

	if !bytes.Equal(hash2[:4], checksum) {
		return 0, nil, fmt.Errorf("checksum mismatch")
	}

	return version, payload, nil
}

// DecodeFamilySeed converts an XRPL family seed (starting with 's') to ed25519 private key bytes
func DecodeFamilySeed(seed string) ([]byte, error) {
	if !strings.HasPrefix(seed, "s") {
		return nil, fmt.Errorf("invalid family seed format: must start with 's'")
	}

	b58 := NewBase58()
	version, seedBytes, err := b58.DecodeCheck(seed)
	if err != nil {
		return nil, fmt.Errorf("failed to decode seed: %w", err)
	}

	if version != familySeedPrefix[0] {
		return nil, fmt.Errorf("invalid family seed version byte")
	}

	// Generate ed25519 private key from seed
	hash := sha512.Sum512(seedBytes)
	privateKey := ed25519.NewKeyFromSeed(hash[:32])

	return privateKey, nil
}

// sign implements the XRPL transaction signing logic using a family seed
func (c *Client) sign(msg, familySeed string) (string, error) {
	privateKey, err := DecodeFamilySeed(familySeed)
	if err != nil {
		return "", fmt.Errorf("failed to decode family seed: %w", err)
	}
	msgHash := sha512.Sum512([]byte(msg))
	signature := ed25519.Sign(ed25519.PrivateKey(privateKey), msgHash[:])
	return strings.ToUpper(hex.EncodeToString(signature)), nil
}

// SignAndSubmitRequest signs a transaction using a family seed and submits it to the network
func (c *Client) SignAndSubmitRequest(req BaseRequest, familySeed string) (BaseResponse, error) {
	txJSON, ok := req["tx_json"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("tx_json field missing or invalid in request")
	}

	privateKey, err := DecodeFamilySeed(familySeed)
	if err != nil {
		return nil, fmt.Errorf("failed to decode family seed: %w", err)
	}

	pubKey := ed25519.PrivateKey(privateKey).Public()
	txJSON["SigningPubKey"] = hex.EncodeToString(pubKey.(ed25519.PublicKey))

	message, err := json.Marshal(txJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transaction for signing: %w", err)
	}

	signature, err := c.sign(string(message), familySeed)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	txJSON["TxnSignature"] = signature

	submitReq := BaseRequest{
		"command": "submit",
		"tx_json": txJSON,
	}

	return c.Request(submitReq)
}

// DeriveAddress derives an XRPL address from a public key
func DeriveAddress(publicKey []byte) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid public key length")
	}

	// Hash the public key
	hash := sha512.Sum512(publicKey)
	ripemd160Hash := hash[:20] // Use first 20 bytes

	// Create XRPL address using base58check encoding
	b58 := NewBase58()
	address := b58.EncodeCheck(accountPublicPrefix[0], ripemd160Hash)

	return address, nil
}
