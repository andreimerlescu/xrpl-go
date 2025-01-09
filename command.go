package xrpl

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"

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

func (c *Client) SignAndSubmitRequest(req BaseRequest, privateKey []byte) (BaseResponse, error) {
	txJSON, ok := req["tx_json"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("tx_json field missing or invalid in request")
	}

	pubKey := ed25519.PrivateKey(privateKey).Public()
	txJSON["SigningPubKey"] = hex.EncodeToString(pubKey.(ed25519.PublicKey))

	message, err := json.Marshal(txJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transaction for signing: %w", err)
	}

	signature, err := c.sign(string(message), hex.EncodeToString(privateKey))
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
