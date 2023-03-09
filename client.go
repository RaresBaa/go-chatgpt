package main

import (
	"context"
	"fmt"
	"github.com/PullRequestInc/go-gpt3"
	"github.com/gorilla/websocket"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	// Max wait time when writing message to peer
	writeWait = 10 * time.Second

	// Max time till next pong from peer
	pongWait = 60 * time.Second

	// Send ping interval, must be less then pong wait time
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 10000
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

var apiKey string

// Client represents the websocket client at the server
type Client struct {
	// The actual websocket connection.
	conn     *websocket.Conn
	wsServer *WsServer
	send     chan []byte
}

func newClient(conn *websocket.Conn, wsServer *WsServer) *Client {
	return &Client{
		conn:     conn,
		wsServer: wsServer,
		send:     make(chan []byte, 256),
	}

}

func (client *Client) readPump() {
	defer func() {
		client.disconnect()
	}()

	client.conn.SetReadLimit(maxMessageSize)
	client.conn.SetReadDeadline(time.Now().Add(pongWait))
	client.conn.SetPongHandler(func(string) error { client.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	for {
		_, jsonMessage, err := client.conn.ReadMessage()
		log.Println("Client message: ", string(jsonMessage))
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("unexpected close error: %v", err)
			}
			break
		}
		client.wsServer.broadcast <- jsonMessage

		// Send message to ChatGPT API for processing
		response, err := sendToChatGPTAPI(string(jsonMessage))
		if err != nil {
			log.Printf("Failed to send message to ChatGPT API:", err)
		}

		// Send response back to client
		err = client.conn.WriteMessage(websocket.TextMessage, []byte(response))
		if err != nil {
			// Handle error
		}
	}
}

func (client *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		client.conn.Close()
	}()
	for {
		select {
		case message, ok := <-client.send:
			client.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The WsServer closed the channel.
				client.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := client.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			log.Println("writePump: ", string(message))

			// Attach queued chat messages to the current websocket message.
			n := len(client.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-client.send)
			}
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			client.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (client *Client) disconnect() {
	client.wsServer.unregister <- client
	close(client.send)
	client.conn.Close()
}

func ServeWs(wsServer *WsServer, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	client := newClient(conn, wsServer)
	go client.writePump()
	go client.readPump()
	wsServer.register <- client
}

func sendToChatGPTAPI(message string) (string, error) {
	// Read API key from file
	keyBytes, err := ioutil.ReadFile("api-key.txt")
	if err != nil {
		log.Fatalln("Error reading API key:", err)
	}
	apiKey = string(keyBytes)

	ctx := context.Background()
	client := gpt3.NewClient(apiKey)

	//get only the second part from the message
	message = strings.Split(message, ":")[1]
	//and remove the \n
	message = strings.TrimSuffix(message, "\n")

	resp, err := client.Completion(ctx, gpt3.CompletionRequest{
		Prompt:    []string{message},
		MaxTokens: gpt3.IntPtr(30),
		Stop:      []string{"."},
		Echo:      true,
	})

	//create a response var like this:  {"message":"hello\n"}
	response := fmt.Sprintf("{\"message\":\"%s\"}", resp.Choices[0].Text)

	if err != nil {
		return "Chat-GPT response error:", err
	}
	log.Println("Called sendToChatGPTAPI with message: ", message)
	log.Println("Chat-GPT response: ", response)
	log.Println("end")
	return response, nil
}
